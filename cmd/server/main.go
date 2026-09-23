// Command server is the headless Midas agent: one provider, one model, one
// conversation. It has no terminal UI, no goals, no MCP servers and no
// subagents — it is the smallest program built on the pkg/agent + pkg/ai core,
// and the natural starting point for embedding Midas in something else.
//
// Every turn is reported on stdout. --json switches that to one JSON object per
// line so a caller can consume events, usage, and cache statistics directly.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/CarlvinceTan/midas/internal/api"
	"github.com/CarlvinceTan/midas/internal/chat"
	providerpkg "github.com/CarlvinceTan/midas/internal/provider"
	"github.com/CarlvinceTan/midas/internal/server"
	"github.com/CarlvinceTan/midas/internal/storage"
	"github.com/CarlvinceTan/midas/internal/tools"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/protocol"
)

const defaultSystemPrompt = "You are Midas, a concise coding agent. Inspect before editing, preserve unrelated work, use the smallest sufficient toolset, and verify changes before reporting completion. Write temporary or generated scratch files under the system temp directory ($TMPDIR, usually /tmp), never in the working tree."

type options struct {
	cwd            string
	listen         string
	environmentDir string
	environment    bool
	provider       string
	model          string
	baseURL        string
	session        string
	system         string
	readOnly       bool
	json           bool
	listOnly       bool
	noCompact      bool
	maxTokens      int
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "midas-server:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	parsed, prompt, err := parseFlags(args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		// Asking for help is not a failure: the usage has already been printed.
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" || parsed.environment {
		return serveEnvironment(parsed, getenv, stderr)
	}
	cwd := parsed.cwd
	if strings.TrimSpace(cwd) == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	providerID, modelID := parsed.provider, strings.TrimSpace(parsed.model)
	if providerID == "" {
		// A compatible-provider model ID commonly contains a slash, so only read
		// that slash as provider/model shorthand when no provider was given.
		providerID, modelID = ai.SplitModelReference("", modelID)
	}
	if providerID == "" || modelID == "" {
		return errors.New("--model must be provider/model, or use --provider with a model ID")
	}
	if parsed.listOnly {
		return printModels(stdout, providerID, modelID, parsed.baseURL, getenv)
	}
	if strings.TrimSpace(parsed.listen) != "" {
		return serve(parsed, providerID, modelID, absolute, getenv, stderr)
	}
	if strings.TrimSpace(prompt) == "" {
		prompt, err = readPrompt(os.Stdin, stderr)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("no prompt given; pass it as an argument or on stdin")
	}

	provider, model, apiKey, err := providerpkg.ConfiguredProvider(providerID, modelID, "", parsed.baseURL, getenv)
	if err != nil {
		return err
	}
	kit, err := tools.New(absolute)
	if err != nil {
		return err
	}
	available := kit.All()
	if parsed.readOnly {
		available = kit.ReadOnly()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := storage.New(storage.ConfigDir())
	history, err := loadHistory(store, parsed.session)
	if err != nil {
		return err
	}
	report := newReporter(stdout, parsed.json)
	report.start(runID(parsed.session), parsed.session, model)

	config := agent.Config{
		Provider:      provider,
		Model:         model,
		StreamOptions: ai.StreamOptions{APIKey: apiKey, MaxTokens: parsed.maxTokens, CacheRetention: ai.CacheShort, MaxRetries: 2, MaxRetryDelay: 2 * time.Second},
		ToolExecution: agent.ToolExecutionParallel,
	}
	compaction := agent.DefaultCompactionSettings()
	compaction.Enabled = !parsed.noCompact
	loopContext := agent.Context{SystemPrompt: systemPrompt(parsed.system), Messages: history, Tools: available}
	if agent.ShouldCompact(agent.ContextTokens(history), model.ContextWindow, compaction) {
		compacted, done, compactErr := agent.Compact(ctx, provider, model, config.StreamOptions, history, compaction)
		if compactErr != nil {
			report.notice("compaction failed: " + compactErr.Error())
		} else if done {
			history = compacted
			loopContext.Messages = compacted
			report.notice(fmt.Sprintf("compacted the context to %d messages", len(compacted)))
		}
	}

	message := ai.NewUserMessage(prompt, time.Now())
	stream := agent.Run(ctx, []ai.Message{message}, loopContext, config)
	for {
		event, ok, nextErr := stream.Next(ctx)
		if nextErr != nil {
			return nextErr
		}
		if !ok {
			break
		}
		report.event(event)
	}
	produced, err := stream.Result(ctx)
	if err != nil {
		return err
	}
	if err := persist(store, parsed.session, append(append([]ai.Message(nil), history...), produced...)); err != nil {
		return err
	}
	status := protocol.RunDone
	failure := ""
	if final, ok := finalAssistant(produced); ok {
		switch final.StopReason {
		case ai.StopError:
			status, failure = protocol.RunFailed, final.ErrorMessage
		case ai.StopAborted:
			status, failure = protocol.RunAborted, final.ErrorMessage
		}
	}
	report.finish(status, failure)
	if failure != "" {
		return errors.New(failure)
	}
	return nil
}

// serveEnvironment hosts the multi-agent environment: one process, many
// long-lived agents, a shared tool pool and browser, and the user-transport API.
// This is what the server binary is for; the single-shot path below is kept for
// benchmarking because it is the fastest way to measure one turn.
func serveEnvironment(parsed options, getenv func(string) string, stderr io.Writer) error {
	configDir := strings.TrimSpace(parsed.environmentDir)
	if configDir == "" {
		configDir = storage.ConfigDir()
	}
	stateDir := filepath.Join(configDir, "server")
	listen := strings.TrimSpace(parsed.listen)
	if listen == "" {
		listen = "127.0.0.1:8788"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	environment, err := server.New(ctx, server.Options{
		ConfigDir: configDir, StateDir: stateDir, Root: parsed.cwd,
		Getenv: getenv,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "server %s listening on http://%s (token in %s)\n", "0.1.0", listen, server.Path(configDir))
	// SIGHUP re-reads server.json and applies it: new agents start, changed models
	// take effect on the agent's next message, and removed agents stop. The same is
	// available as POST /v1/reload for an operator who cannot signal the process.
	if signals := reloadSignals(); len(signals) > 0 {
		reload := make(chan os.Signal, 1)
		signal.Notify(reload, signals...)
		defer signal.Stop(reload)
		go func() {
			for range reload {
				report, err := environment.Reload(ctx)
				if err != nil {
					fmt.Fprintf(stderr, "server: reload failed, running agents unchanged: %v\n", err)
					continue
				}
				fmt.Fprintf(stderr, "server: reloaded: %s\n", report.Summary())
			}
		}()
	}
	// A browser that is configured but missing is reported rather than fatal: the
	// agents still work, they just have no browser until the image has one.
	if _, err := environment.EnsureBrowser(ctx); err != nil {
		fmt.Fprintf(stderr, "server: browser unavailable: %v\n", err)
	}
	return environment.Serve(ctx, listen)
}

// serve hosts the HTTP agent API until the process is interrupted. The run
// registry lives in memory: a run is addressable for as long as the server
// keeps it, which is what lets a client attach late or reconnect.
func serve(parsed options, providerID, modelID, root string, getenv func(string) string, stderr io.Writer) error {
	provider, model, apiKey, err := providerpkg.ConfiguredProvider(providerID, modelID, "", parsed.baseURL, getenv)
	if err != nil {
		return err
	}
	codingTools := chat.CodingToolsAll
	if parsed.readOnly {
		codingTools = chat.CodingToolsReadOnly
	}
	compaction := agent.DefaultCompactionSettings()
	compaction.Enabled = !parsed.noCompact
	server, err := api.NewServer(api.Config{
		Root: root, SessionID: parsed.session, Provider: provider, Model: model, APIKey: apiKey,
		StreamOptions: ai.StreamOptions{MaxTokens: parsed.maxTokens, CacheRetention: ai.CacheShort, MaxRetries: 2, MaxRetryDelay: 2 * time.Second},
		CodingTools:   codingTools, Sessions: storage.New(storage.ConfigDir()), Compaction: compaction,
		ResolveModel: func(request protocol.RunRequest) (ai.Streamer, ai.Model, string, error) {
			ref := strings.TrimSpace(request.Model)
			if ref == "" {
				return nil, ai.Model{}, "", nil
			}
			requestedProvider, requestedModel := ai.SplitModelReference(providerID, ref)
			return providerpkg.ConfiguredProvider(requestedProvider, requestedModel, "", parsed.baseURL, getenv)
		},
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", parsed.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", parsed.listen, err)
	}
	fmt.Fprintf(stderr, "midas-server %s listening on http://%s (protocol v%s)\n", buildVersion(), listener.Addr(), protocol.Version)

	httpServer := &http.Server{
		Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout bounds an unused keep-alive connection. WriteTimeout stays
		// unset because agent streams can be long lived.
		IdleTimeout: 2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
		return nil
	}
}

// buildVersion reports the module build, so a client can tell which binary is
// serving.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}

// runID names a run for the protocol stream. It is derived from the session so a
// resumed session reports a stable identity to its client.
func runID(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Sprintf("run_%d", time.Now().UnixNano())
	}
	return "run_" + sessionID
}

func parseFlags(args []string, stdout io.Writer) (options, string, error) {
	var parsed options
	flags := flag.NewFlagSet("midas-server", flag.ContinueOnError)
	flags.SetOutput(stdout)
	flags.StringVar(&parsed.listen, "listen", "", "serve the HTTP agent API on this address instead of running one prompt")
	flags.StringVar(&parsed.cwd, "C", "", "working directory (default: current directory)")
	flags.StringVar(&parsed.cwd, "cwd", "", "working directory (default: current directory)")
	flags.StringVar(&parsed.provider, "provider", "", "provider ID from the Midas catalog")
	flags.StringVar(&parsed.model, "model", "", "model as provider/model, or a model ID with --provider")
	flags.StringVar(&parsed.model, "m", "", "model as provider/model, or a model ID with --provider")
	flags.StringVar(&parsed.baseURL, "base-url", "", "base URL for a compatible provider")
	flags.StringVar(&parsed.session, "session", "", "session ID to resume and persist")
	flags.StringVar(&parsed.session, "s", "", "session ID to resume and persist")
	flags.StringVar(&parsed.system, "system", "", "system prompt (default: the built-in Midas prompt)")
	flags.BoolVar(&parsed.readOnly, "read-only", false, "expose only the inspection tools")
	flags.BoolVar(&parsed.json, "json", false, "emit one JSON object per event instead of prose")
	flags.BoolVar(&parsed.listOnly, "list-models", false, "list the provider's models and exit")
	flags.StringVar(&parsed.environmentDir, "config-dir", "", "configuration directory for the agent environment (default: the agent config dir)")
	flags.BoolVar(&parsed.environment, "serve", false, "host the multi-agent environment (the default when no prompt is given)")
	flags.BoolVar(&parsed.noCompact, "no-compact", false, "never summarize older context")
	flags.IntVar(&parsed.maxTokens, "max-tokens", 0, "maximum output tokens per turn")
	if err := flags.Parse(args); err != nil {
		return options{}, "", err
	}
	return parsed, strings.Join(flags.Args(), " "), nil
}

func systemPrompt(override string) string {
	if value := strings.TrimSpace(override); value != "" {
		return value
	}
	return defaultSystemPrompt
}

func readPrompt(input io.Reader, stderr io.Writer) (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("provide a prompt as an argument")
	}
	_ = stderr
	reader := bufio.NewReader(input)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func loadHistory(store *storage.Store, id string) ([]ai.Message, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	return store.ReadTranscript(id)
}

func persist(store *storage.Store, id string, messages []ai.Message) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return store.WriteTranscript(id, messages)
}

func printModels(output io.Writer, providerID, modelID, baseURL string, getenv func(string) string) error {
	provider, _, _, err := providerpkg.ConfiguredProvider(providerID, modelID, "", baseURL, getenv)
	if err != nil {
		return err
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		return err
	}
	for _, model := range models {
		fmt.Fprintf(output, "%s/%s\t%s\n", model.Provider, model.ID, model.Name)
	}
	return nil
}

func finalAssistant(messages []ai.Message) (ai.AssistantMessage, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if assistant, ok := messages[index].(ai.AssistantMessage); ok {
			return assistant, true
		}
	}
	return ai.AssistantMessage{}, false
}
