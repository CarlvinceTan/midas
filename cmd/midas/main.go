package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CarlvinceTan/midas/internal/chat"
	"github.com/CarlvinceTan/midas/internal/goal"
	"github.com/CarlvinceTan/midas/internal/instructions"
	"github.com/CarlvinceTan/midas/pkg/mcp"
	"github.com/CarlvinceTan/midas/internal/profiles"
	providerpkg "github.com/CarlvinceTan/midas/pkg/provider"
	midasremote "github.com/CarlvinceTan/midas/internal/remote"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/internal/skills"
	internalstats "github.com/CarlvinceTan/midas/internal/stats"
	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/internal/tui"
	"github.com/CarlvinceTan/midas/internal/voice"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
	providerauth "github.com/CarlvinceTan/midas/pkg/ai/auth"
	providercatalog "github.com/CarlvinceTan/midas/pkg/ai/catalog"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "midas:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	_ = stderr
	workingDirectory, err := os.Getwd()
	if err != nil {
		workingDirectory = "."
	}
	cli, err := parseCLI(args, workingDirectory)
	if err != nil {
		return err
	}
	if cli.version {
		fmt.Fprintln(stdout, buildVersion())
		return nil
	}
	if cli.help {
		writeUsage(stdout)
		return nil
	}
	// User-defined agents come from settings.json, and they have to be registered
	// before any name is resolved, so `--agent`, `/agents`, and the startup summary
	// all see them.
	startupSettings := midassettings.New(storage.ConfigDir())
	if err := profiles.LoadCustom(startupSettings.Object(midassettings.Agents)); err != nil {
		return err
	}
	profile, err := profiles.ResolveProfile(cli.agent)
	if err != nil {
		return err
	}
	if !profiles.CanEnter(profile.Name) {
		if profile.Reserved || profiles.IsUtilityAgent(profile.Name) {
			return fmt.Errorf("agent %q is a utility agent Midas runs on its own behalf, so a session cannot start as it", profile.Name)
		}
		return fmt.Errorf("agent %q is a subagent: start a session as a main agent and let it invoke %s", profile.Name, profile.Name)
	}
	if cli.listAgents {
		for _, item := range profiles.Profiles() {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.Name, item.Mode, item.Description)
		}
		return nil
	}
	useTUI := !cli.print && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	authStore := providerauth.New(storage.ConfigDir())
	environmentProvider := strings.TrimSpace(getenv("MIDAS_PROVIDER"))
	providerID := cli.provider
	if providerID == "" {
		providerID = environmentProvider
	}
	modelID := cli.model
	if modelID == "" {
		modelID = getenv("MIDAS_MODEL")
	}
	// An explicit provider or model on the command line is a choice for this run and
	// outranks the agent's own configuration; a provider remembered from last time
	// is only a fallback and does not count.
	chosenModel := strings.TrimSpace(cli.provider) != "" || strings.TrimSpace(cli.model) != "" ||
		environmentProvider != "" || strings.TrimSpace(getenv("MIDAS_MODEL")) != ""
	// Compatible-provider model IDs commonly contain a slash. Only treat that
	// slash as provider/model shorthand when no provider was explicitly chosen.
	if strings.TrimSpace(cli.provider) == "" && environmentProvider == "" {
		providerID, modelID = splitModel(providerID, modelID)
	}
	if providerID == "" && modelID != "" {
		providerID = "openai"
	}
	if providerID == "" && useTUI {
		providerID = authStore.LastProvider()
	}
	baseURL := cli.baseURL
	if baseURL == "" {
		baseURL = getenv("MIDAS_BASE_URL")
	}
	if cli.listModels {
		return printModels(ctx, stdout, providerID, modelID, cli.api, baseURL, getenv)
	}
	if !useTUI && strings.TrimSpace(cli.prompt) == "" {
		return nil
	}
	if strings.TrimSpace(modelID) == "" && !useTUI {
		return errors.New("model is required (--model or MIDAS_MODEL)")
	}
	store := storage.New("")
	settingsStore := startupSettings
	appSettings := settingsStore.Load()
	agentModels := tui.LoadAgentModelConfig(settingsStore)
	// The active agent's own model and reasoning outrank the provider default: a
	// /agents pin (or a model named in settings.json) applies to a new session and
	// to a resumed one alike, and an entry agent with neither keeps the model it
	// last used.
	// The fallback is whatever the command line, the environment, or the provider
	// last used gives, which is what a session starts on when the agent names no
	// model or names one that cannot authenticate here.
	fallbackProviderID, fallbackModelID := providerID, modelID
	providerID, modelID = startupModel(chosenModel, providerID, modelID, agentModels, profile.Name)
	var provider ai.Provider
	var model ai.Model
	var key string
	startupNotice := ""
	if !chosenModel {
		if ref := strings.TrimSpace(agentModels.Ref(profile.Name, "")); ref != "" {
			resolvedProvider := splitProvider(ref, fallbackProviderID)
			if !providerUsable(resolvedProvider, getenv) {
				// The agent names a provider nobody has configured on this machine, for
				// example a settings file copied from another one. Fall back instead of
				// starting on a provider that cannot answer.
				startupNotice = fmt.Sprintf("%s is set for %s, but %s has no credentials on this machine; starting on the provider default",
					ref, profile.Name, resolvedProvider)
				providerID, modelID = fallbackProviderID, fallbackModelID
			} else if agentProvider, agentModel, agentKey, agentErr := agentModelRuntime(agentModels, ref, nil, getenv, fallbackProviderID); agentErr == nil {
				provider, model, key, providerID, modelID = agentProvider, agentModel, agentKey, agentModel.Provider, agentModel.ID
			} else {
				startupNotice = fmt.Sprintf("Could not start on %s for %s: %v", ref, profile.Name, agentErr)
				providerID, modelID = fallbackProviderID, fallbackModelID
			}
		}
	}
	if provider == nil {
		if providerID == "" && useTUI {
			provider = providerpkg.DisconnectedProvider{}
		} else {
			provider, model, key, err = providerpkg.ConfiguredProvider(providerID, modelID, cli.api, baseURL, getenv)
			if err != nil {
				if startupNotice != "" {
					return fmt.Errorf("%w (%s)", err, startupNotice)
				}
				return err
			}
		}
	}
	absRoot, err := filepath.Abs(cli.cwd)
	if err != nil {
		return err
	}
	instructionEntries, err := instructions.Load(absRoot, storage.ConfigDir())
	if err != nil {
		return err
	}
	goals := goal.NewStore(filepath.Join(storage.ConfigDir(), "goals.json"))
	mcpConfigs, err := mcp.LoadConfigs(storage.ConfigDir(), absRoot)
	if err != nil {
		return err
	}
	mcpManager, err := mcp.New(mcpConfigs)
	if err != nil {
		return err
	}
	defer func() { _ = mcpManager.Close() }()
	connectContext, cancelConnect := context.WithTimeout(ctx, 15*time.Second)
	_ = mcpManager.ConnectAll(connectContext)
	cancelConnect()
	codingMode, additionalTools, err := profileRuntime(absRoot, profile)
	if err != nil {
		return err
	}
	skillEntries := skills.Discover(absRoot, storage.ConfigDir())
	// The level is the agent's own choice (from /agents or its definition) when the
	// model supports reasoning, and the model's own default otherwise.
	reasoning := ai.ThinkingOff
	if model.Reasoning {
		// The agent's own level (a /agents choice, its definition, or the level the
		// model was last used with) applies; medium is only the fallback for an agent
		// that has never chosen, so an explicit "off" stays off.
		reasoning = agentModels.Level(profile.Name, model.Provider+"/"+model.ID, ai.ThinkingMedium)
	}
	// The same instruction and skill text is appended to every agent's prompt, so a
	// subagent spawned later works under the project's rules too.
	systemSuffix := composeSystemPrompt("", instructionEntries, skillEntries)
	session, err := chat.New(chat.Options{
		Root: absRoot, SessionID: strings.TrimSpace(cli.session), Provider: provider, Model: model,
		Agent:         profile.Name,
		SystemPrompt:  composeSystemPrompt(profile.SystemPrompt, instructionEntries, skillEntries),
		StreamOptions: ai.StreamOptions{APIKey: key, MaxTokens: cli.maxTokens, Reasoning: reasoning, CacheRetention: configuredCacheRetention(getenv), MaxRetries: 2, MaxRetryDelay: 2 * time.Second},
		CacheWarming:  configuredCacheWarming(getenv),
		SystemSuffix:  systemSuffix,
		TitleMaxWords: appSettings.TitleMaxWords,
		Sessions:      store, Goals: goals, MCP: mcpManager,
		CodingTools: codingMode, AdditionalTools: additionalTools,
		Compaction: compactionSettings(appSettings.Compaction),
	})
	if err != nil {
		return err
	}
	// Delegation works in every mode. The TUI replaces this resolver with one that
	// knows the live model catalog and the current thinking level; a headless run
	// resolves against the session's own state.
	session.SetAgentRuntime(func(agentName, inherited string) (chat.AgentRuntime, error) {
		ref := strings.TrimSpace(agentModels.Ref(agentName, inherited))
		if ref == "" {
			ref = strings.TrimSpace(inherited)
		}
		childProvider, childModel, childKey, resolveErr := agentModelRuntime(agentModels, ref, nil, getenv, session.Model().Provider)
		if resolveErr != nil {
			return chat.AgentRuntime{}, resolveErr
		}
		level := ai.ThinkingOff
		if childModel.Reasoning {
			level = agentModels.Level(agentName, ref, session.Reasoning())
			if level == "" {
				level = ai.ThinkingMedium
			}
		}
		return chat.AgentRuntime{Provider: childProvider, Model: childModel, APIKey: childKey, Reasoning: level}, nil
	})
	if useTUI {
		return runInteractive(ctx, stdout, session, goals, mcpManager, store, settingsStore, provider, model, profile, instructionEntries, skillEntries, getenv, startupNotice)
	}
	if startupNotice != "" {
		fmt.Fprintln(stderr, "midas: "+startupNotice)
	}
	return runPrompt(ctx, stdout, session, cli.prompt)
}

// splitProvider returns the provider a model reference names, falling back to the
// provider last used when the reference is a bare model ID.
func splitProvider(ref, fallback string) string {
	providerID, _ := splitModel(fallback, ref)
	return strings.TrimSpace(providerID)
}

// providerUsable reports whether a provider can authenticate on this machine: it
// has a stored credential, or one of the environment variables the catalog names
// for it. A provider nobody has configured must not become a session's startup
// model just because an agent names it, so Midas falls back and says why. A
// provider the catalog does not know is left alone: a local endpoint may need no
// key at all, and that cannot be proven from here.
func providerUsable(providerID string, getenv func(string) string) bool {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return false
	}
	if _, ok := providerauth.New(storage.ConfigDir()).Get(providerID); ok {
		return true
	}
	spec, known := providercatalog.Lookup(providerID)
	if !known {
		return true
	}
	if key := strings.TrimSpace(getenv("MIDAS_API_KEY")); key != "" {
		return true
	}
	for _, envName := range spec.EnvVars {
		if strings.TrimSpace(getenv(envName)) != "" {
			return true
		}
	}
	return len(spec.EnvVars) == 0
}

// startupModel picks the provider and model a session starts with: the active
// agent's own model (a /agents pin, its settings.json definition, or — for an
// entry agent — the model it last used), unless the command line named one, which
// is a choice for this run and outranks it.
func startupModel(chosen bool, providerID, modelID string, config tui.AgentModelConfig, agent string) (string, string) {
	if chosen {
		return providerID, modelID
	}
	ref := strings.TrimSpace(config.Ref(agent, ""))
	if ref == "" {
		return providerID, modelID
	}
	resolvedProvider, resolvedModel := splitModel("", ref)
	if resolvedProvider == "" || resolvedModel == "" {
		return providerID, modelID
	}
	return resolvedProvider, resolvedModel
}

func buildVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func profileRuntime(root string, profile profiles.Profile) (chat.CodingToolsMode, []agent.Tool, error) {
	switch profiles.ToolsMode(profile.Name) {
	case profiles.ToolsNone:
		// A user-defined agent may run with no tools at all, for example a pure
		// reasoning advisor.
		return chat.CodingToolsNone, nil, nil
	case profiles.ToolsReadOnly:
		return chat.CodingToolsReadOnly, nil, nil
	}
	if profile.ReadOnly {
		return chat.CodingToolsReadOnly, nil, nil
	}
	return chat.CodingToolsAll, nil, nil
}

func runPrompt(ctx context.Context, stdout io.Writer, session *chat.Chat, prompt string) error {
	stream, err := session.Prompt(ctx, prompt)
	if err != nil {
		return err
	}
	wrote := false
	for {
		event, ok, nextErr := stream.Next(ctx)
		if nextErr != nil {
			return nextErr
		}
		if !ok {
			break
		}
		if event.AssistantEvent != nil && event.AssistantEvent.Type == ai.EventTextDelta {
			if _, err := io.WriteString(stdout, event.AssistantEvent.Delta); err != nil {
				return err
			}
			wrote = true
		}
	}
	messages, err := stream.Result(ctx)
	if err != nil {
		return err
	}
	final, ok := finalAssistant(messages)
	if !ok {
		return errors.New("provider returned no assistant message")
	}
	if final.StopReason == ai.StopError || final.StopReason == ai.StopAborted {
		return errors.New(final.ErrorMessage)
	}
	if !wrote {
		if _, err := io.WriteString(stdout, textContent(final)); err != nil {
			return err
		}
	}
	_, err = io.WriteString(stdout, "\n")
	return err
}

type appChatBackend struct{ chat *chat.Chat }

func (b appChatBackend) Start(ctx context.Context, prompt string) (tui.ChatRun, error) {
	return b.chat.Prompt(ctx, prompt)
}
func (b appChatBackend) Steer(_ context.Context, prompt string) error { return b.chat.Steer(prompt) }
func (b appChatBackend) Abort(cause error) bool                       { return b.chat.Abort(cause) }

func (b appChatBackend) CompactContext(ctx context.Context) ([]ai.Message, bool, error) {
	return b.chat.Compact(ctx)
}

type connectField struct {
	Key         string
	Title       string
	Prompt      string
	Placeholder string
	Secret      bool
}

func runInteractive(ctx context.Context, output io.Writer, session *chat.Chat, goals *goal.Store, mcps *mcp.Manager, store *storage.Store, settingsStore *midassettings.Store, provider ai.Provider, model ai.Model, initialProfile profiles.Profile, instructionEntries []instructions.Entry, skillEntries []skills.Entry, getenv func(string) string, startupNotice string) error {
	interactiveSettings := settingsStore.Load()
	tui.SetPadding(interactiveSettings.Padding)
	localTerminal := tui.NewProcessTerminal()
	terminal := midasremote.NewTerminal(localTerminal)
	screen := tui.NewTuiAltScreen(terminal, tui.TuiAltScreenOptions{
		ShowHardwareCursor: true,
		CopySelection: func(value string) bool {
			terminal.Write(osc52(value))
			return true
		},
	})
	terminal.SetRequestRender(func() { screen.RequestRender(false) })
	modelsContext, cancelModels := context.WithTimeout(ctx, 10*time.Second)
	models := providerpkg.DiscoverModels(modelsContext, provider, model, getenv)
	cancelModels()
	model = providerpkg.DisplayModelDetails(model, models)
	var modelsMu sync.Mutex
	modelSnapshot := func() []ai.Model {
		modelsMu.Lock()
		defer modelsMu.Unlock()
		return append([]ai.Model(nil), models...)
	}
	replaceProviderModels := func(providerID string, discovered []ai.Model) {
		modelsMu.Lock()
		defer modelsMu.Unlock()
		next := make([]ai.Model, 0, len(models)+len(discovered))
		for _, candidate := range models {
			if candidate.Provider != providerID {
				next = append(next, candidate)
			}
		}
		for _, candidate := range discovered {
			if candidate.ID != "" {
				next = append(next, candidate)
			}
		}
		providerpkg.ApplyCatalogCapabilities(next)
		slices.SortStableFunc(next, func(a, b ai.Model) int {
			if a.Provider == b.Provider {
				return cmp.Compare(a.ID, b.ID)
			}
			return cmp.Compare(a.Provider, b.Provider)
		})
		models = next
	}
	command := localCommand(goals, mcps, store, session.Root(), model)
	initialDraft, _ := store.ReadDraft(session.ID())
	initialState, _ := store.ReadSessionState(session.ID())
	initialQueue := queuedText(initialState.Queue)
	var clientStateMu sync.Mutex
	// reportPersist surfaces a failed write instead of losing it silently; it is
	// replaced with the toast once the view exists, and the in-memory session stays
	// correct either way.
	reportPersist := func(error) {}
	persistQueue := func(queue []string, held bool) {
		clientStateMu.Lock()
		defer clientStateMu.Unlock()
		state, _ := store.ReadSessionState(session.ID())
		state.CWD = session.Root()
		state.Queue = make([]storage.QueuedPrompt, len(queue))
		for index, text := range queue {
			state.Queue[index] = storage.QueuedPrompt{Text: text}
		}
		state.QueueHold = held
		reportPersist(store.WriteSessionState(session.ID(), state))
	}
	currentProfile := initialProfile.Name
	entryAgent := "main"
	if profiles.IsEntryAgent(currentProfile) {
		entryAgent = currentProfile
	}
	agentModels := tui.LoadAgentModelConfig(settingsStore)
	// The model each agent ran with in this session outranks its persisted
	// /agents setting, exactly as a session /model choice does.
	sessionAgentModels := map[string]string{}
	currentModelRef := func() string {
		model := session.Model()
		if strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.ID) == "" {
			return ""
		}
		return model.Provider + "/" + model.ID
	}
	sessionAgentModels[currentProfile] = currentModelRef()
	thinking := session.Reasoning()
	var thinkingMu sync.Mutex
	currentThinking := func() ai.ThinkingLevel {
		thinkingMu.Lock()
		defer thinkingMu.Unlock()
		return thinking
	}
	setThinking := func(level ai.ThinkingLevel) {
		thinkingMu.Lock()
		thinking = level
		thinkingMu.Unlock()
	}
	var view *tui.Chat
	var mountDock func(tui.Component) func()
	providerCredentials := providerauth.New(storage.ConfigDir())
	var showOptions func(string, []tui.Option, func(string))
	// Dialog titles read as the path the user took, so a menu inside another menu
	// says where it is instead of stacking a second frame with a second title.
	crumb := func(parts ...string) string {
		kept := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				kept = append(kept, trimmed)
			}
		}
		return strings.Join(kept, " > ")
	}
	agentsCrumb := func(agent, step string) string {
		return crumb("Agents", tui.CapitalizeAgentName(agent), step)
	}
	var showSearchOptions func(string, []tui.Option, func(string))
	var promptField func(connectField, func(string))
	var collectFields func([]connectField, map[string]string, func(map[string]string))
	var showConnect func()
	var showSettings func()
	var showAgents func(string)
	var showAgentModel func(string)
	var stopRemote func(bool)
	var toggleRemote func(string)
	var remoteMu sync.Mutex
	var remoteService *midasremote.Service
	var remoteStartCancel context.CancelFunc
	var remoteGeneration uint64
	stopRemote = func(notify bool) {
		remoteMu.Lock()
		remoteGeneration++
		cancel := remoteStartCancel
		service := remoteService
		remoteStartCancel = nil
		remoteService = nil
		remoteMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if service != nil {
			_ = service.Stop(context.Background())
		}
		if view != nil {
			view.SetRemote("", nil)
			if notify {
				view.ShowToast("Remote off", tui.ToastSuccess, 2500*time.Millisecond)
			}
		}
	}
	toggleRemote = func(argument string) {
		mode := strings.ToLower(strings.TrimSpace(argument))
		if mode != "" && mode != "on" && mode != "start" && mode != "off" && mode != "stop" {
			view.ShowToast("Usage: /remote [on|off]", tui.ToastWarning, 2500*time.Millisecond)
			return
		}
		remoteMu.Lock()
		active := remoteService != nil || remoteStartCancel != nil
		service := remoteService
		remoteMu.Unlock()
		if mode == "off" || mode == "stop" || (mode == "" && active) {
			stopRemote(active)
			return
		}
		if active {
			if service != nil && service.URL() != "" {
				terminal.Write(osc52(service.URL()))
				view.ShowToast("Remote link copied", tui.ToastSuccess, 2500*time.Millisecond)
			}
			return
		}
		password := settingsStore.Load().RemotePassword
		if strings.TrimSpace(password) == "" {
			view.ShowToast("Set a remote password in /settings first", tui.ToastWarning, 4*time.Second)
			return
		}
		remoteContext, cancel := context.WithCancel(ctx)
		remoteMu.Lock()
		remoteGeneration++
		generation := remoteGeneration
		remoteStartCancel = cancel
		remoteMu.Unlock()
		view.ShowToast("Starting remote…", tui.ToastWarning, 30*time.Second)
		go func() {
			service, startErr := midasremote.Start(remoteContext, midasremote.Options{Terminal: terminal, Password: password})
			remoteMu.Lock()
			stale := generation != remoteGeneration
			if !stale {
				remoteStartCancel = nil
				if startErr == nil {
					remoteService = service
				}
			}
			remoteMu.Unlock()
			if stale {
				if service != nil {
					_ = service.Stop(context.Background())
				}
				return
			}
			if startErr != nil {
				if !errors.Is(startErr, context.Canceled) {
					view.ShowToast("Could not start remote: "+startErr.Error(), tui.ToastError, 6*time.Second)
				}
				return
			}
			url := service.URL()
			view.SetRemote(url, func() {
				terminal.Write(osc52(url))
				view.ShowToast("Remote link copied", tui.ToastSuccess, 2500*time.Millisecond)
			})
			terminal.Write(osc52(url))
			view.ShowToast("Remote link copied", tui.ToastSuccess, 2500*time.Millisecond)
		}()
	}
	refreshProviderModels := func(providerID string) {
		go func() {
			provider, _, _, providerErr := providerpkg.ConfiguredProvider(providerID, "", "", "", getenv)
			if providerErr != nil {
				view.ShowToast(providerErr.Error(), tui.ToastError, 4*time.Second)
				return
			}
			refreshContext, cancelRefresh := context.WithTimeout(ctx, 15*time.Second)
			defer cancelRefresh()
			discovered, modelErr := provider.Models(refreshContext)
			if modelErr != nil {
				view.ShowToast("Connected, but models could not be loaded.", tui.ToastWarning, 4*time.Second)
				return
			}
			replaceProviderModels(providerID, discovered)
		}()
	}
	activateCredential := func(providerID string, credential providerauth.Credential) {
		var err error
		if credential.Kind == providerauth.KindAPIKey {
			credential, err = providerCredentials.AddAPIKey(providerID, credential)
		} else {
			err = providerCredentials.Set(providerID, credential)
		}
		if err != nil {
			view.ShowToast("Could not save provider: "+err.Error(), tui.ToastError, 4*time.Second)
			return
		}
		view.SetProvider(providerID)
		current := session.Model()
		if current.Provider == providerID && current.ID != "" {
			selectedProvider, _, key, configureErr := providerpkg.ConfiguredProvider(providerID, current.ID, current.API, current.BaseURL, getenv)
			if configureErr != nil {
				view.ShowToast("API key saved, but the provider could not be refreshed: "+configureErr.Error(), tui.ToastWarning, 5*time.Second)
				return
			}
			if runtimeErr := session.SetRuntime(selectedProvider, current, key); runtimeErr != nil {
				view.ShowToast("API key saved, but the active session could not be refreshed: "+runtimeErr.Error(), tui.ToastWarning, 5*time.Second)
				return
			}
			view.SetModelDetails(current.ID, current.Name, current.ContextWindow)
		} else {
			view.SetModelDetails("", "", 0)
		}
		message := "Connected to " + credential.Name + "."
		if len(credential.APIKeys) > 1 {
			message = fmt.Sprintf("Connected to %s with %d saved keys.", credential.Name, len(credential.APIKeys))
		}
		view.ShowToast(message, tui.ToastSuccess, 2500*time.Millisecond)
		refreshProviderModels(providerID)
	}
	showOptions = func(title string, options []tui.Option, onSelect func(string)) {
		var closeDock func()
		picker := tui.NewOptionPicker(options, func(value string) {
			if closeDock != nil {
				closeDock()
			}
			onSelect(value)
		}, func() {
			if closeDock != nil {
				closeDock()
			}
		})
		closeDock = mountDock(tui.Dialog(title, picker))
	}
	// Long provider catalogs get the same `> ` search row as the model picker.
	showSearchOptions = func(title string, options []tui.Option, onSelect func(string)) {
		var closeDock func()
		picker := tui.NewSearchOptionPicker(options, func(value string) {
			if closeDock != nil {
				closeDock()
			}
			onSelect(value)
		}, func() {
			if closeDock != nil {
				closeDock()
			}
		})
		closeDock = mountDock(tui.Dialog(title, picker))
	}
	promptField = func(field connectField, onSubmit func(string)) {
		var closeDock func()
		input := tui.NewInput(tui.InputOptions{
			Prompt: field.Prompt, Placeholder: field.Placeholder, Secret: field.Secret,
			PlaceholderStyle: func(value string) string { return tui.CurrentTheme().FG("dim", value) },
			OnSubmit: func(value string) {
				if closeDock != nil {
					closeDock()
				}
				onSubmit(strings.TrimSpace(value))
			},
			OnEscape: func() {
				if closeDock != nil {
					closeDock()
				}
			},
		})
		closeDock = mountDock(tui.Dialog(field.Title, input))
	}
	collectFields = func(fields []connectField, values map[string]string, done func(map[string]string)) {
		if len(fields) == 0 {
			done(values)
			return
		}
		field := fields[0]
		promptField(field, func(value string) {
			next := make(map[string]string, len(values)+1)
			for key, current := range values {
				next[key] = current
			}
			next[field.Key] = value
			collectFields(fields[1:], next, done)
		})
	}
	showConnect = func() {
		options := []tui.Option{
			{Label: "API key", Description: "Built-in cloud providers or a compatible endpoint", Value: "api-key"},
			{Label: "Account", Description: "Provider account or subscription", Value: "oauth"},
		}
		if len(providerCredentials.ProviderIDs()) > 0 || view.Provider() != "" {
			options = append(options, tui.Option{Label: "Disconnect", Description: "Remove a saved provider", Value: "disconnect"})
		}
		showOptions("Connect", options, func(choice string) {
			switch choice {
			case "api-key":
				var promptNewProviderKey func(providercatalog.Provider)
				var manageProviderKeys func(providercatalog.Provider)
				promptNewProviderKey = func(spec providercatalog.Provider) {
					fields := make([]connectField, 0, len(spec.Fields)+1)
					for _, field := range spec.Fields {
						fields = append(fields, connectField{Key: field.Key, Title: crumb("Connect", "API key", spec.Name, field.Label), Prompt: field.Label + ": ", Placeholder: field.Placeholder, Secret: field.Secret})
					}
					fields = append(fields, connectField{Key: "key", Title: crumb("Connect", "API key", spec.Name, "API key"), Prompt: "API key: ", Placeholder: "Paste API key", Secret: true})
					collectFields(fields, map[string]string{}, func(values map[string]string) {
						if values["key"] == "" {
							view.ShowToast("API key is required.", tui.ToastError, 4*time.Second)
							return
						}
						credential, credentialErr := providerpkg.CatalogCredential(spec, values)
						if credentialErr != nil {
							view.ShowToast(credentialErr.Error(), tui.ToastError, 4*time.Second)
							return
						}
						activateCredential(spec.ID, credential)
					})
				}
				manageProviderKeys = func(spec providercatalog.Provider) {
					credential, connected := providerCredentials.Get(spec.ID)
					if !connected || credential.Kind != providerauth.KindAPIKey || len(credential.APIKeys) == 0 {
						promptNewProviderKey(spec)
						return
					}
					keyOptions := []tui.Option{{Label: "Add API key", Description: "Save another key for this provider", Value: "add"}}
					for index, key := range credential.APIKeys {
						description := "saved"
						if key == credential.APIKey {
							description = "active"
						}
						keyOptions = append(keyOptions, tui.Option{Label: providerpkg.MaskedAPIKey(key), Description: description, Value: "use:" + strconv.Itoa(index)})
					}
					keyOptions = append(keyOptions, tui.Option{Label: "Remove API key", Description: "Remove one saved key", Value: "remove"})
					showOptions(crumb("Connect", "API key", spec.Name), keyOptions, func(action string) {
						switch {
						case action == "add":
							promptField(connectField{Key: "key", Title: crumb("Connect", "API key", spec.Name, "Add key"), Prompt: "API key: ", Placeholder: "Paste another API key", Secret: true}, func(key string) {
								if key == "" {
									view.ShowToast("API key is required.", tui.ToastError, 4*time.Second)
									return
								}
								credential.APIKey = key
								credential.APIKeys = nil
								activateCredential(spec.ID, credential)
							})
						case strings.HasPrefix(action, "use:"):
							index, indexErr := strconv.Atoi(strings.TrimPrefix(action, "use:"))
							if indexErr != nil || index < 0 || index >= len(credential.APIKeys) {
								view.ShowToast("Saved API key is unavailable.", tui.ToastError, 4*time.Second)
								return
							}
							if _, keyErr := providerCredentials.SetActiveAPIKey(spec.ID, credential.APIKeys[index]); keyErr != nil {
								view.ShowToast("Could not select API key: "+keyErr.Error(), tui.ToastError, 4*time.Second)
								return
							}
							if current := session.Model(); current.Provider == spec.ID && current.ID != "" {
								selectedProvider, _, key, configureErr := providerpkg.ConfiguredProvider(spec.ID, current.ID, current.API, current.BaseURL, getenv)
								if configureErr != nil {
									view.ShowToast("Could not activate API key: "+configureErr.Error(), tui.ToastError, 4*time.Second)
									return
								}
								if runtimeErr := session.SetRuntime(selectedProvider, current, key); runtimeErr != nil {
									view.ShowToast(runtimeErr.Error(), tui.ToastError, 4*time.Second)
									return
								}
							}
							view.SetProvider(spec.ID)
							view.ShowToast("Active API key changed for "+spec.Name+".", tui.ToastSuccess, 2500*time.Millisecond)
						case action == "remove":
							removeOptions := make([]tui.Option, 0, len(credential.APIKeys))
							for index, key := range credential.APIKeys {
								description := "saved"
								if key == credential.APIKey {
									description = "active"
								}
								removeOptions = append(removeOptions, tui.Option{Label: providerpkg.MaskedAPIKey(key), Description: description, Value: strconv.Itoa(index)})
							}
							showOptions(crumb("Connect", "API key", spec.Name, "Remove"), removeOptions, func(value string) {
								index, indexErr := strconv.Atoi(value)
								if indexErr != nil || index < 0 || index >= len(credential.APIKeys) {
									view.ShowToast("Saved API key is unavailable.", tui.ToastError, 4*time.Second)
									return
								}
								remaining, providerDeleted, removeErr := providerCredentials.DeleteAPIKey(spec.ID, credential.APIKeys[index])
								if removeErr != nil {
									view.ShowToast("Could not remove API key: "+removeErr.Error(), tui.ToastError, 4*time.Second)
									return
								}
								if providerDeleted {
									replaceProviderModels(spec.ID, nil)
									if view.Provider() == spec.ID {
										view.SetProvider("")
										view.SetModelDetails("", "", 0)
									}
									view.ShowToast("Removed the final API key for "+spec.Name+".", tui.ToastSuccess, 2500*time.Millisecond)
									return
								}
								if current := session.Model(); current.Provider == spec.ID && current.ID != "" {
									selectedProvider, _, key, configureErr := providerpkg.ConfiguredProvider(spec.ID, current.ID, current.API, current.BaseURL, getenv)
									if configureErr != nil {
										view.ShowToast("API key removed, but the provider could not be refreshed: "+configureErr.Error(), tui.ToastWarning, 5*time.Second)
										return
									}
									if runtimeErr := session.SetRuntime(selectedProvider, current, key); runtimeErr != nil {
										view.ShowToast("API key removed, but the active session could not be refreshed: "+runtimeErr.Error(), tui.ToastWarning, 5*time.Second)
										return
									}
								}
								view.ShowToast(fmt.Sprintf("Removed API key. %d saved for %s.", len(remaining.APIKeys), spec.Name), tui.ToastSuccess, 2500*time.Millisecond)
							})
						}
					})
				}
				providerOptions := []tui.Option{{Label: "Add provider manually", Description: "OpenAI-compatible endpoint", Value: "compatible"}}
				providerSpecs := make(map[string]providercatalog.Provider)
				for _, spec := range providercatalog.APIKeyProviders() {
					description := spec.Description
					if credential, connected := providerCredentials.Get(spec.ID); connected {
						if len(credential.APIKeys) > 1 {
							description = fmt.Sprintf("connected • %d keys • %s", len(credential.APIKeys), description)
						} else {
							description = "connected • " + description
						}
					}
					providerSpecs[spec.ID] = spec
					providerOptions = append(providerOptions, tui.Option{Label: spec.Name, Description: description, Value: spec.ID})
				}
				for _, providerID := range providerCredentials.ProviderIDs() {
					if _, known := providerSpecs[providerID]; known {
						continue
					}
					credential, saved := providerCredentials.Get(providerID)
					if !saved || credential.Kind != providerauth.KindAPIKey {
						continue
					}
					spec := providercatalog.Provider{ID: providerID, Name: credential.Name, Description: "Custom OpenAI-compatible provider", API: credential.API, BaseURL: credential.BaseURL}
					providerSpecs[providerID] = spec
					description := "connected"
					if len(credential.APIKeys) > 1 {
						description = fmt.Sprintf("connected • %d keys", len(credential.APIKeys))
					}
					providerOptions = append(providerOptions, tui.Option{Label: credential.Name, Description: description, Value: providerID})
				}
				showSearchOptions(crumb("Connect", "API key"), providerOptions, func(providerID string) {
					if providerID == "compatible" {
						collectFields([]connectField{
							{Key: "name", Title: "Provider name", Prompt: "Name: ", Placeholder: "Provider name"},
							{Key: "base", Title: "Base URL", Prompt: "URL: ", Placeholder: "https://example.com/v1"},
							{Key: "key", Title: "API key", Prompt: "Key: ", Placeholder: "Optional", Secret: true},
						}, map[string]string{}, func(values map[string]string) {
							if values["name"] == "" || values["base"] == "" {
								view.ShowToast("Provider name and base URL are required.", tui.ToastError, 4*time.Second)
								return
							}
							id := providerpkg.ProviderSlug(values["name"])
							activateCredential(id, providerauth.Credential{Kind: providerauth.KindAPIKey, Name: values["name"], APIKey: values["key"], BaseURL: values["base"], API: "openai-completions"})
						})
						return
					}
					spec, exists := providerSpecs[providerID]
					if !exists {
						view.ShowToast("Unknown provider.", tui.ToastError, 4*time.Second)
						return
					}
					manageProviderKeys(spec)
				})
			case "oauth":
				oauthOptions := []tui.Option{
					{Label: "Anthropic", Description: "Claude Pro or Max", Value: "anthropic"},
					{Label: "GitHub Copilot", Description: "Copilot subscription", Value: "github-copilot"},
					{Label: "OpenAI Codex", Description: "ChatGPT Plus or Pro", Value: "openai-codex"},
					{Label: "OpenRouter", Description: "Sign in with OpenRouter", Value: "openrouter"},
				}
				for _, device := range providerauth.DeviceProviders() {
					oauthOptions = append(oauthOptions, tui.Option{Label: device.Name, Description: device.Description, Value: device.ID})
				}
				oauthOptions = append(oauthOptions,
					tui.Option{Label: "Google Gemini", Description: "Sign in with a Google Cloud OAuth client", Value: "google"},
					tui.Option{Label: "Compatible", Description: "Configure a standards-based OAuth provider", Value: "compatible"},
				)
				showSearchOptions(crumb("Connect", "Account"), oauthOptions, func(providerType string) {
					if providerType == "github-copilot" {
						view.ShowToast("Starting GitHub Copilot sign-in…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 15*time.Minute)
							defer cancelAuthorize()
							oauthCredential, copilotBaseURL, loginErr := providerauth.LoginGitHubCopilot(authorizeContext, nil, func(code providerauth.DeviceCode) error {
								view.ShowToast("Enter code "+code.UserCode+" in the browser.", tui.ToastWarning, code.ExpiresIn)
								return providerpkg.OpenExternalURL(code.VerificationURL)
							})
							if loginErr != nil {
								view.ShowToast("GitHub Copilot sign-in failed: "+loginErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							activateCredential("github-copilot", providerauth.Credential{Kind: providerauth.KindOAuth, Name: "GitHub Copilot", BaseURL: copilotBaseURL, API: "openai-completions", OAuth: &oauthCredential})
						}()
						return
					}
					if providerType == "openai-codex" {
						view.ShowToast("Opening OpenAI sign-in…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 5*time.Minute)
							defer cancelAuthorize()
							oauthCredential, accountID, loginErr := providerauth.LoginOpenAICodex(authorizeContext, providerpkg.OpenExternalURL)
							if loginErr != nil {
								view.ShowToast("OpenAI sign-in failed: "+loginErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							activateCredential("openai-codex", providerauth.Credential{Kind: providerauth.KindOAuth, Name: "OpenAI Codex", BaseURL: "https://chatgpt.com/backend-api/codex", API: "openai-responses", Env: map[string]string{"CHATGPT_ACCOUNT_ID": accountID}, OAuth: &oauthCredential})
						}()
						return
					}
					if providerType == "anthropic" {
						view.ShowToast("Opening Anthropic sign-in…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 5*time.Minute)
							defer cancelAuthorize()
							oauthCredential, loginErr := providerauth.LoginAnthropic(authorizeContext, nil, providerpkg.OpenExternalURL)
							if loginErr != nil {
								view.ShowToast("Anthropic sign-in failed: "+loginErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							activateCredential("anthropic", providerauth.Credential{Kind: providerauth.KindOAuth, Name: "Anthropic", API: "anthropic-messages", OAuth: &oauthCredential})
						}()
						return
					}
					if providerType == "openrouter" {
						view.ShowToast("Opening OpenRouter sign-in…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 5*time.Minute)
							defer cancelAuthorize()
							key, loginErr := providerauth.LoginOpenRouter(authorizeContext, nil, providerpkg.OpenExternalURL)
							if loginErr != nil {
								view.ShowToast("OpenRouter sign-in failed: "+loginErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							activateCredential("openrouter", providerauth.Credential{Kind: providerauth.KindAPIKey, Name: "OpenRouter", APIKey: key, BaseURL: "https://openrouter.ai/api/v1", API: "openai-completions"})
						}()
						return
					}
					if device, ok := providerauth.DeviceProviderByID(providerType); ok {
						view.ShowToast("Starting "+device.Name+" sign-in…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 15*time.Minute)
							defer cancelAuthorize()
							oauthCredential, loginErr := providerauth.LoginDevice(authorizeContext, device, nil, func(code providerauth.DeviceCode) error {
								view.ShowToast("Enter code "+code.UserCode+" in the browser.", tui.ToastWarning, code.ExpiresIn)
								return providerpkg.OpenExternalURL(code.VerificationURL)
							})
							if loginErr != nil {
								view.ShowToast(device.Name+" sign-in failed: "+loginErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							activateCredential(device.ID, providerauth.Credential{Kind: providerauth.KindOAuth, Name: device.Name, BaseURL: device.BaseURL, API: device.API, OAuth: &oauthCredential})
						}()
						return
					}
					var fields []connectField
					if providerType == "google" {
						fields = []connectField{
							{Key: "client", Title: "Google OAuth", Prompt: "Client ID: ", Placeholder: "Desktop OAuth client ID"},
							{Key: "secret", Title: "Google OAuth", Prompt: "Client secret: ", Placeholder: "Client secret", Secret: true},
							{Key: "project", Title: "Google OAuth", Prompt: "Project ID: ", Placeholder: "Google Cloud project ID"},
						}
					} else {
						fields = []connectField{
							{Key: "name", Title: "OAuth provider", Prompt: "Name: ", Placeholder: "Provider name"},
							{Key: "base", Title: "Base URL", Prompt: "API URL: ", Placeholder: "https://example.com/v1"},
							{Key: "auth", Title: "Authorization URL", Prompt: "Auth URL: ", Placeholder: "https://example.com/oauth/authorize"},
							{Key: "token", Title: "Token URL", Prompt: "Token URL: ", Placeholder: "https://example.com/oauth/token"},
							{Key: "client", Title: "OAuth client", Prompt: "Client ID: ", Placeholder: "Client ID"},
							{Key: "secret", Title: "OAuth client", Prompt: "Client secret: ", Placeholder: "Optional", Secret: true},
							{Key: "scopes", Title: "OAuth scopes", Prompt: "Scopes: ", Placeholder: "Space-separated scopes"},
						}
					}
					collectFields(fields, map[string]string{}, func(values map[string]string) {
						providerID := "google"
						credential := providerauth.Credential{Kind: providerauth.KindOAuth, Name: "Google Gemini", API: "google-generative-ai", ProjectID: values["project"]}
						oauthCredential := providerauth.OAuthCredential{
							AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
							ClientID: values["client"], ClientSecret: values["secret"],
							Scopes: []string{"https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/generative-language.retriever"},
						}
						if providerType == "compatible" {
							providerID = providerpkg.ProviderSlug(values["name"])
							credential.Name, credential.BaseURL, credential.API = values["name"], values["base"], "openai-completions"
							oauthCredential = providerauth.OAuthCredential{
								AuthorizationURL: values["auth"], TokenURL: values["token"], ClientID: values["client"], ClientSecret: values["secret"], Scopes: strings.Fields(values["scopes"]),
							}
						}
						if credential.Name == "" || oauthCredential.AuthorizationURL == "" || oauthCredential.TokenURL == "" || oauthCredential.ClientID == "" || providerType == "compatible" && credential.BaseURL == "" {
							view.ShowToast("OAuth provider details are incomplete.", tui.ToastError, 4*time.Second)
							return
						}
						view.ShowToast("Opening the browser to connect…", tui.ToastWarning, 4*time.Second)
						go func() {
							authorizeContext, cancelAuthorize := context.WithTimeout(ctx, 5*time.Minute)
							defer cancelAuthorize()
							authorized, authorizeErr := providerpkg.AuthorizeOAuth(authorizeContext, oauthCredential, providerpkg.OpenExternalURL)
							if authorizeErr != nil {
								view.ShowToast("OAuth failed: "+authorizeErr.Error(), tui.ToastError, 5*time.Second)
								return
							}
							credential.OAuth = &authorized
							activateCredential(providerID, credential)
						}()
					})
				})
			case "disconnect":
				ids := providerCredentials.ProviderIDs()
				activeProvider := view.Provider()
				activeSaved := false
				for _, id := range ids {
					activeSaved = activeSaved || id == activeProvider
				}
				if activeProvider != "" && !activeSaved {
					ids = append(ids, activeProvider)
					slices.Sort(ids)
				}
				disconnectOptions := make([]tui.Option, 0, len(ids))
				for _, id := range ids {
					credential, saved := providerCredentials.Get(id)
					label := canonicalProviderName(id, credential.Name)
					description := id
					if !saved {
						description = "current session"
					}
					disconnectOptions = append(disconnectOptions, tui.Option{Label: label, Description: description, Value: id})
				}
				showOptions("Disconnect", disconnectOptions, func(providerID string) {
					credential, saved := providerCredentials.Get(providerID)
					if saved {
						if err := providerCredentials.Delete(providerID); err != nil {
							view.ShowToast("Could not disconnect provider: "+err.Error(), tui.ToastError, 4*time.Second)
							return
						}
					}
					replaceProviderModels(providerID, nil)
					if view.Provider() == providerID {
						view.SetProvider("")
						view.SetModelDetails("", "", 0)
					}
					name := canonicalProviderName(providerID, credential.Name)
					view.ShowToast("Disconnected "+name+".", tui.ToastSuccess, 2500*time.Millisecond)
				})
			}
		})
	}
	voiceController := voice.NewParakeet(voice.ParakeetOptions{
		ConfigDir: storage.ConfigDir(),
		OnText: func(committed, partial string) {
			if view != nil {
				view.SetVoiceText(committed, partial)
			}
		},
		OnReady: func() {
			if view != nil {
				view.SetVoiceReady(true)
			}
		},
		OnError: func(message string) {
			if view != nil {
				view.SetVoiceReady(false)
				view.EndVoice(true)
				view.ShowToast("Voice: "+message, tui.ToastError, 4*time.Second)
			}
		},
		OnStop: func() {
			if view != nil && view.VoiceActive() {
				view.SetVoiceReady(false)
				view.EndVoice(true)
				view.ShowToast("Voice input stopped; captured text was retained.", tui.ToastWarning, 4*time.Second)
			}
		},
	})
	// stopVoice commits the dictated text and releases the speech model, so the
	// recognizer only occupies memory while dictation is active.
	stopVoice := func() {
		go func() {
			if voiceController != nil {
				voiceController.PauseAndWait(15 * time.Second)
			}
			if view != nil {
				view.EndVoice(true)
			}
			if voiceController != nil {
				voiceController.Stop()
			}
		}()
	}
	setTerminalTitle := func(title string) {
		if !settingsStore.Load().TerminalTitle {
			terminal.SetTitle("")
			return
		}
		title = strings.TrimSpace(title)
		if title == "" {
			terminal.SetTitle("Midas")
			return
		}
		terminal.SetTitle("Midas · " + title)
	}
	showSettings = func() {
		values := settingsStore.Load()
		state := func(enabled bool) string {
			if enabled {
				return "on"
			}
			return "off"
		}
		options := []tui.Option{
			{Label: "Auto model rotation", Description: state(values.AutoModelRotation) + " • retry another model from the same provider", Value: midassettings.AutoModelRotation},
			{Label: "Auto provider rotation", Description: state(values.AutoProviderRotation) + " • retry through another connected provider", Value: midassettings.AutoProviderRotation},
			{Label: "Terminal title", Description: state(values.TerminalTitle) + " • update the terminal window title", Value: midassettings.TerminalTitle},
			{Label: "Compact header", Description: state(values.CompactHeader) + " • show only the session title and path", Value: midassettings.CompactHeader},
			{Label: "Padding", Description: strconv.Itoa(values.Padding) + " • spaces Midas keeps from the terminal borders", Value: midassettings.Padding},
			{Label: "Title max words", Description: fmt.Sprintf("%d • maximum words in generated session titles", values.TitleMaxWords), Value: midassettings.TitleMaxWords},
			{Label: "Summary max words", Description: fmt.Sprintf("%d • maximum words in the generated live summary", values.StatusMaxWords), Value: midassettings.StatusMaxWords},
			{Label: "Compaction", Description: state(values.Compaction.Enabled) + " • summarize older context when the model window fills", Value: midassettings.Compaction},
			{Label: "Compaction reserve tokens", Description: fmt.Sprintf("%d • response room kept below the model window", values.Compaction.ReserveTokens), Value: midassettings.Compaction + ".reserve"},
			{Label: "Compaction keep recent tokens", Description: fmt.Sprintf("%d • recent context every compaction keeps", values.Compaction.KeepRecentTokens), Value: midassettings.Compaction + ".keep"},
			{Label: "Remote password", Description: map[bool]string{true: "set", false: "not set"}[values.RemotePassword != ""] + " • protects the public browser link", Value: midassettings.RemotePassword},
		}
		showOptions("Settings", options, func(key string) {
			current := settingsStore.Load()
			if key == midassettings.RemotePassword {
				promptField(connectField{Key: "password", Title: crumb("Settings", "Remote password"), Prompt: "Password: ", Placeholder: "Leave empty to clear", Secret: true}, func(value string) {
					if err := settingsStore.Set(midassettings.RemotePassword, value); err != nil {
						view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
						return
					}
					remoteMu.Lock()
					remoteActive := remoteService != nil || remoteStartCancel != nil
					remoteMu.Unlock()
					if remoteActive {
						stopRemote(false)
					}
					if value == "" {
						view.ShowToast("Remote password cleared", tui.ToastSuccess, 2500*time.Millisecond)
					} else {
						view.ShowToast("Remote password saved", tui.ToastSuccess, 2500*time.Millisecond)
					}
					showSettings()
				})
				return
			}
			if key == midassettings.Compaction {
				next := current.Compaction
				next.Enabled = !next.Enabled
				if err := settingsStore.SetCompaction(next); err != nil {
					view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
					return
				}
				showSettings()
				return
			}
			if key == midassettings.Compaction+".reserve" || key == midassettings.Compaction+".keep" {
				reserve := key == midassettings.Compaction+".reserve"
				title, prompt, placeholder := "Compaction keep recent tokens", "Keep recent tokens (>= 1024): ", current.Compaction.KeepRecentTokens
				if reserve {
					title, prompt, placeholder = "Compaction reserve tokens", "Reserve tokens (>= 1024): ", current.Compaction.ReserveTokens
				}
				promptField(connectField{Key: "tokens", Title: crumb("Settings", title), Prompt: prompt, Placeholder: strconv.Itoa(placeholder)}, func(value string) {
					tokens, parseErr := strconv.Atoi(value)
					if parseErr != nil || tokens < 1024 || tokens > 1_000_000 {
						view.ShowToast("Tokens must be between 1024 and 1000000.", tui.ToastWarning, 3*time.Second)
						showSettings()
						return
					}
					next := current.Compaction
					if reserve {
						next.ReserveTokens = tokens
					} else {
						next.KeepRecentTokens = tokens
					}
					if err := settingsStore.SetCompaction(next); err != nil {
						view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
						return
					}
					showSettings()
				})
				return
			}
			if key == midassettings.Padding {
				promptField(connectField{Key: "padding", Title: crumb("Settings", "Padding"), Prompt: "Padding (0–4): ", Placeholder: strconv.Itoa(current.Padding)}, func(value string) {
					padding, parseErr := strconv.Atoi(value)
					if parseErr != nil || padding < 0 || padding > 4 {
						view.ShowToast("Padding must be between 0 and 4.", tui.ToastWarning, 3*time.Second)
						showSettings()
						return
					}
					if err := settingsStore.Set(midassettings.Padding, padding); err != nil {
						view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
						return
					}
					view.SetPadding(padding)
					showSettings()
				})
				return
			}
			if key == midassettings.TitleMaxWords || key == midassettings.StatusMaxWords {
				maximum, title, prompt, placeholder := 20, "Title max words", "Words (1–20): ", current.TitleMaxWords
				if key == midassettings.StatusMaxWords {
					maximum, title, prompt, placeholder = 12, "Summary max words", "Words (1–12): ", current.StatusMaxWords
				}
				promptField(connectField{Key: "words", Title: crumb("Settings", title), Prompt: prompt, Placeholder: strconv.Itoa(placeholder)}, func(value string) {
					words, parseErr := strconv.Atoi(value)
					if parseErr != nil || words < 1 || words > maximum {
						view.ShowToast(fmt.Sprintf("%s must be between 1 and %d.", title, maximum), tui.ToastWarning, 3*time.Second)
						showSettings()
						return
					}
					if err := settingsStore.Set(key, words); err != nil {
						view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
						return
					}
					if key == midassettings.TitleMaxWords {
						session.SetTitleMaxWords(words)
						view.SetTitleMaxWords(words)
					} else {
						view.SetStatusMaxWords(words)
					}
					showSettings()
				})
				return
			}
			enabled := false
			switch key {
			case midassettings.AutoModelRotation:
				enabled = !current.AutoModelRotation
			case midassettings.AutoProviderRotation:
				enabled = !current.AutoProviderRotation
			case midassettings.TerminalTitle:
				enabled = !current.TerminalTitle
			case midassettings.CompactHeader:
				enabled = !current.CompactHeader
			default:
				return
			}
			if err := settingsStore.Set(key, enabled); err != nil {
				view.ShowToast("Could not save settings: "+err.Error(), tui.ToastError, 4*time.Second)
				return
			}
			if key == midassettings.TerminalTitle {
				setTerminalTitle(sessionTitle(store, session.ID()))
			}
			if key == midassettings.CompactHeader {
				view.SetCompactHeader(enabled)
			}
			showSettings()
		})
	}
	refreshEditorBorder := func() {
		if view == nil {
			return
		}
		level := currentThinking()
		if level == "" {
			level = ai.ThinkingOff
		}
		view.SetEditorBorderStyle(func(value string) string { return tui.CurrentTheme().Thinking(level, value) })
	}
	// agentModelRef resolves the model an agent runs with: this session's model
	// for the agent, then a /agents choice, then — for entry agents — the model
	// that agent last used, and otherwise its caller's model.
	agentModelRef := func(name string) string {
		name = strings.TrimSpace(name)
		if ref := strings.TrimSpace(sessionAgentModels[name]); ref != "" {
			return ref
		}
		if parent := tui.ParentAgent(name, entryAgent); parent != "" {
			// A subagent inherits the model its caller is running in this session.
			if ref := strings.TrimSpace(sessionAgentModels[parent]); ref != "" {
				return agentModels.Ref(name, ref)
			}
		}
		return agentModels.Ref(name, agentModels.InheritedRef(name, entryAgent, currentModelRef()))
	}
	// applyAgentModel switches the live runtime when the active agent's resolved
	// model differs from the one the session is running.
	applyAgentModel := func(name string) {
		if name != currentProfile || session.Busy() {
			return
		}
		ref := agentModelRef(name)
		if ref == "" {
			return
		}
		if ref == currentModelRef() {
			sessionAgentModels[name] = ref
			if level := agentModels.Level(name, ref, currentThinking()); level != currentThinking() {
				if err := session.SetReasoning(level); err == nil {
					setThinking(level)
					view.SetThinking(level)
					refreshEditorBorder()
				}
			}
			return
		}
		provider, model, key, err := agentModelRuntime(agentModels, ref, modelSnapshot(), getenv, session.Model().Provider)
		if err != nil {
			view.Notice("Error: " + err.Error())
			return
		}
		if err := session.SetRuntime(provider, model, key); err != nil {
			view.Notice("Error: " + err.Error())
			return
		}
		level := agentModels.Level(name, ref, currentThinking())
		if !model.Reasoning {
			level = ai.ThinkingOff
		} else if level == "" || level == ai.ThinkingOff {
			level = ai.ThinkingMedium
		}
		if err := session.SetReasoning(level); err != nil {
			view.Notice("Error: " + err.Error())
			return
		}
		sessionAgentModels[name] = ref
		setThinking(level)
		view.SetProvider(model.Provider)
		view.SetModelDetails(model.ID, model.Name, model.ContextWindow)
		view.SetModelReasoning(model.Reasoning)
		view.SetThinking(level)
		refreshEditorBorder()
	}
	applyProfile := func(profile profiles.Profile) error {
		if session.Busy() {
			return errors.New("finish or abort the active run before changing agent profile")
		}
		mode, additional, err := profileRuntime(session.Root(), profile)
		if err != nil {
			return err
		}
		if err := session.SetProfile(profile.Name, composeSystemPrompt(profile.SystemPrompt, instructionEntries, skillEntries), mode, additional); err != nil {
			return err
		}
		currentProfile = profile.Name
		if profiles.IsEntryAgent(profile.Name) {
			entryAgent = profile.Name
		}
		view.SetAgent(profile.Name)
		refreshEditorBorder()
		applyAgentModel(profile.Name)
		return nil
	}
	// /agents configures each agent's model. Entry agents (main) default to the
	// model and reasoning level they last used, while every other agent inherits
	// the model of the agent that invokes it.
	showAgents = func(target string) {
		if target != "" {
			profile, resolveErr := profiles.ResolveProfile(strings.ToLower(target))
			if resolveErr != nil {
				view.Notice("Error: " + resolveErr.Error())
				return
			}
			if !profiles.CanEnter(profile.Name) {
				view.Notice("Error: " + tui.CapitalizeAgentName(profile.Name) + " is a " + profile.Mode + " agent: another agent runs it, so a session cannot switch to it.")
				return
			}
			if profile.Reserved {
				view.ShowToast("Reserved worker agents are not interactive profiles.", tui.ToastWarning, 3*time.Second)
				return
			}
			if profileErr := applyProfile(profile); profileErr != nil {
				view.Notice("Error: " + profileErr.Error())
				return
			}
			view.Notice("Agent profile changed to " + profile.Name + ".")
			return
		}
		catalog := modelSnapshot()
		showSearchOptions("Agents", tui.AgentPanelOptions(profiles.Profiles(), currentProfile, agentModels, catalog), func(value string) {
			showAgentModel(value)
		})
	}
	showAgentModel = func(name string) {
		showSearchOptions(agentsCrumb(name, "Model"), tui.ModelChoiceOptions(name, modelSnapshot()), func(ref string) {
			if err := agentModels.SetModel(name, ref); err != nil {
				view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
				return
			}
			if ref == "" {
				delete(sessionAgentModels, name)
				applyAgentModel(name)
				showAgents("")
				return
			}
			sessionAgentModels[name] = ref
			// Choosing a model then chooses its reasoning level for the agent.
			var closeDock func()
			picker := tui.NewThinkingPicker(tui.ThinkingPickerOptions{
				Model:   tui.ModelForRef(ref, modelSnapshot()),
				Current: agentModels.Level(name, ref, ai.ThinkingMedium),
				OnSelect: func(level ai.ThinkingLevel) {
					if err := agentModels.SetLevel(name, level); err != nil {
						view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
					}
					applyAgentModel(name)
					if closeDock != nil {
						closeDock()
					}
					showAgents("")
				},
				OnCancel: func() {
					if closeDock != nil {
						closeDock()
					}
					applyAgentModel(name)
					showAgents("")
				},
			})
			closeDock = mountDock(tui.Dialog(agentsCrumb(name, "Thinking"), picker))
		})
	}
	// environmentFor reports what a working directory contributes to a run: the
	// instruction and skill text the model must see if the directory changes, and
	// a fingerprint of those inputs, so an unchanged environment costs nothing.
	environmentFor := func(root string) (chat.Environment, error) {
		rootEntries, err := instructions.Load(root, storage.ConfigDir())
		if err != nil {
			return chat.Environment{}, err
		}
		rootSkills := skills.Discover(root, storage.ConfigDir())
		fingerprint := environmentFingerprint(root, rootEntries, rootSkills)
		block := "The working directory changed to " + root + ". These instructions and skills replace the previous project's:\n\n" +
			skills.AppendPrompt(instructions.AppendPrompt("", rootEntries), rootSkills)
		return chat.Environment{Fingerprint: fingerprint, Block: block}, nil
	}
	if err := session.UseEnvironment(environmentFor); err != nil {
		return err
	}

	view, err := tui.NewChat(tui.ChatOptions{
		Backend: appChatBackend{chat: session}, Context: ctx, Provider: model.Provider, Model: model.ID, ModelName: model.Name, ModelContext: model.ContextWindow,
		Thinking: currentThinking(), Agent: initialProfile.Name, SessionID: session.ID(), SessionTitle: sessionTitle(store, session.ID()),
		CWD: session.Root(), Branch: currentBranch(session.Root()), ContextPaths: instructions.DisplayPaths(instructionEntries, session.Root()),
		AgentGroups: visibleAgentGroups(), SkillGroups: visibleSkillGroups(skillEntries), MCPNames: mcpNames(mcps),
		InitialMessages: session.Messages(), InitialShell: shellEntries(initialState.Bash), InitialDraft: initialDraft.Text,
		InitialQueue: initialQueue, QueueHeld: initialState.QueueHold,
		CompactHeader: interactiveSettings.CompactHeader, StatusMaxWords: interactiveSettings.StatusMaxWords,
		StatusGenerator: chat.NewStatusGenerator(session.Model, modelSnapshot, getenv), Rows: terminal.Rows,
		TitleGenerator: chat.NewTitleGenerator(func() string { return agentModelRef("title") }, modelSnapshot, getenv),
		TitleMaxWords:  interactiveSettings.TitleMaxWords,
		OnTitle: func(title string) {
			title = strings.TrimSpace(title)
			if title == "" {
				return
			}
			reportPersist(store.UpsertSession(storage.SessionUpdate{ID: session.ID(), CWD: session.Root(), Title: &title}))
			setTerminalTitle(title)
		},
		ModelReasoning: model.Reasoning,
		CostFormatter:  internalstats.NewCostFormatter(storage.ConfigDir(), settingsStore.Load, func() { screen.RequestRender(false) }),
		RequestRender:  func() { screen.RequestRender(false) },
		OnDraftChange:  func(value string) { reportPersist(store.WriteDraft(session.ID(), storage.Draft{Text: value})) },
		OnQueueChange:  persistQueue,
		OnDirectoryChange: func(path string) error {
			if err := session.ChangeDirectory(path); err != nil {
				return err
			}
			reloadedInstructions, err := instructions.Load(path, storage.ConfigDir())
			if err != nil {
				return err
			}
			reloadedSkills := skills.Discover(path, storage.ConfigDir())
			instructionEntries, skillEntries = reloadedInstructions, reloadedSkills
			// Project-scoped MCP servers belong to the directory: reconnect them so
			// the new project's tools follow. This is the one change a cached prefix
			// cannot absorb, because tools sit before messages in the request.
			if configs, err := mcp.LoadConfigs(storage.ConfigDir(), path); err == nil {
				if replacement, err := mcp.New(configs); err == nil {
					connectContext, cancel := context.WithTimeout(ctx, 15*time.Second)
					_ = replacement.ConnectAll(connectContext)
					cancel()
					if err := session.SetMCP(replacement); err != nil {
						_ = replacement.Close()
						return err
					}
					previous := mcps
					mcps = replacement
					_ = previous.Close()
				}
			}
			view.SetEnvironment(tui.Environment{
				CWD: path, Branch: currentBranch(path),
				ContextPaths: instructions.DisplayPaths(instructionEntries, path),
				SkillGroups:  visibleSkillGroups(skillEntries), MCPNames: mcpNames(mcps),
			})
			state, _ := store.ReadSessionState(session.ID())
			state.CWD = path
			reportPersist(store.WriteSessionState(session.ID(), state))
			title := sessionTitle(store, session.ID())
			reportPersist(store.UpsertSession(storage.SessionUpdate{ID: session.ID(), CWD: path, Title: &title}))
			return nil
		},
		OnShellComplete: func(entry tui.ShellEntry) {
			clientStateMu.Lock()
			defer clientStateMu.Unlock()
			state, _ := store.ReadSessionState(session.ID())
			state.CWD = session.Root()
			state.Bash = append(state.Bash, sessionShellEntry(entry))
			reportPersist(store.WriteSessionState(session.ID(), state))
		},
		OnVoiceStop: stopVoice,
		OnCycleThinking: func() {
			next := tui.NextThinkingLevel(session.Model(), currentThinking())
			if thinkingErr := session.SetReasoning(next); thinkingErr != nil {
				view.ShowToast("Could not change thinking: "+thinkingErr.Error(), tui.ToastError, 3*time.Second)
				return
			}
			setThinking(next)
			view.SetThinking(next)
			refreshEditorBorder()
			if err := agentModels.SetModelLevel(currentModelRef(), next); err != nil {
				view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
			}
			view.ShowToast("Thinking "+string(next), tui.ToastSuccess, 2*time.Second)
		},
		Command: command,
		OverlayCommand: func(name, args string) bool {
			if name == "remote" {
				toggleRemote(args)
				return true
			}
			if name == "settings" {
				if strings.TrimSpace(args) != "" {
					view.ShowToast("Usage: /settings", tui.ToastWarning, 2500*time.Millisecond)
					return true
				}
				showSettings()
				return true
			}
			if name == "new" {
				if strings.TrimSpace(args) != "" {
					view.Notice("Error: usage: /new")
					return true
				}
				selectedID, messages, switchErr := session.SwitchSession("")
				if switchErr != nil {
					view.Notice("Error: " + switchErr.Error())
					return true
				}
				view.SetSession(selectedID, messages, nil, "", nil, false)
				view.SetTitle("")
				setTerminalTitle("")
				view.Notice("Started a new session.")
				return true
			}
			if name == "title" {
				title := normalizeSessionTitle(args)
				if title == "" {
					view.Notice("Error: usage: /title <name>")
					return true
				}
				reportPersist(store.UpsertSession(storage.SessionUpdate{ID: session.ID(), CWD: session.Root(), Title: &title}))
				view.SetTitle(title)
				setTerminalTitle(title)
				view.Notice("Session renamed to " + title + ".")
				return true
			}
			if name == "copy" {
				if strings.TrimSpace(args) != "" {
					view.Notice("Error: usage: /copy")
					return true
				}
				text := view.LastAssistantText()
				if text == "" {
					view.Notice("Nothing to copy yet.")
					return true
				}
				terminal.Write(osc52(text))
				view.Notice("Copied the last assistant response.")
				return true
			}
			if name == "voice" {
				mode := strings.ToLower(strings.TrimSpace(args))
				if mode != "" && mode != "on" && mode != "start" && mode != "off" && mode != "stop" {
					view.Notice("Error: usage: /voice [on|off]")
					return true
				}
				stop := mode == "off" || mode == "stop" || (mode == "" && view.VoiceActive())
				if stop {
					if view.BeginVoiceStop() {
						stopVoice()
					}
					return true
				}
				view.StartVoice()
				view.SetVoiceReady(voiceController.Ready())
				voiceController.Listen()
				return true
			}
			if name == "connect" {
				if strings.TrimSpace(args) != "" {
					view.ShowToast("Usage: /connect", tui.ToastWarning, 2500*time.Millisecond)
					return true
				}
				showConnect()
				return true
			}
			if strings.TrimSpace(args) != "" {
				return false
			}
			if name == "sessions" {
				listed := store.ReadResumableSessions(session.ID())
				projectSessions := make([]storage.Session, 0, len(listed))
				for _, candidate := range listed {
					if filepath.Clean(candidate.CWD) == filepath.Clean(session.Root()) {
						projectSessions = append(projectSessions, candidate)
					}
				}
				var closeDock func()
				switchSession := func(id string) {
					selectedID, messages, switchErr := session.SwitchSession(id)
					if switchErr != nil {
						view.Notice("Error: " + switchErr.Error())
						return
					}
					draft, _ := store.ReadDraft(selectedID)
					state, _ := store.ReadSessionState(selectedID)
					view.SetSession(selectedID, messages, shellEntries(state.Bash), draft.Text, queuedText(state.Queue), state.QueueHold)
					title := sessionTitle(store, selectedID)
					view.SetTitle(title)
					setTerminalTitle(title)
					if closeDock != nil {
						closeDock()
					}
				}
				picker := tui.NewSessionPicker(tui.SessionPickerOptions{
					Sessions: projectSessions, CurrentID: session.ID(), OnNew: func() { switchSession("") },
					OnSelect: func(session storage.Session) { switchSession(session.ID) },
					OnCancel: func() {
						if closeDock != nil {
							closeDock()
						}
					},
				})
				panel := tui.Dialog("Sessions", picker)
				closeDock = mountDock(panel)
				return true
			}
			if name == "reload" {
				result, reloadErr := reloadSession(ctx, storage.ConfigDir(), session.Root(), currentProfile, settingsStore, &agentModels, session)
				if reloadErr != nil {
					view.Notice("Error: " + reloadErr.Error())
					return true
				}
				// Apply what the session cannot: the view's own chrome, the MCP
				// manager swap, and the active agent's model if its pin changed.
				interactiveSettings = result.Settings
				view.SetPadding(result.Settings.Padding)
				view.SetCompactHeader(result.Settings.CompactHeader)
				view.SetStatusMaxWords(result.Settings.StatusMaxWords)
				if result.MCPChanged {
					previous := mcps
					mcps = result.Manager
					_ = previous.Close()
				}
				view.SetEnvironment(tui.Environment{
					CWD: session.Root(), Branch: currentBranch(session.Root()),
					ContextPaths: instructions.DisplayPaths(result.Instructions, session.Root()),
					SkillGroups:  visibleSkillGroups(result.Skills), MCPNames: mcpNames(mcps),
				})
				applyAgentModel(currentProfile)
				view.Notice(reloadNotice(result))
				return true
			}
			if name == "agents" {
				showAgents(strings.TrimSpace(args))
				return true
			}
			if name == "thinking" {
				var closeDock func()
				picker := tui.NewThinkingPicker(tui.ThinkingPickerOptions{
					Model: session.Model(), Current: currentThinking(),
					OnSelect: func(level ai.ThinkingLevel) {
						if thinkingErr := session.SetReasoning(level); thinkingErr != nil {
							view.Notice("Error: " + thinkingErr.Error())
							return
						}
						setThinking(level)
						view.SetThinking(level)
						refreshEditorBorder()
						if err := agentModels.SetModelLevel(currentModelRef(), level); err != nil {
							view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
						}
						view.Notice("Thinking level changed to " + string(level) + ".")
						if closeDock != nil {
							closeDock()
						}
					},
					OnCancel: func() {
						if closeDock != nil {
							closeDock()
						}
					},
				})
				closeDock = mountDock(tui.Dialog("Thinking", picker))
				return true
			}
			if name != "model" {
				titles := map[string]string{"stats": "Stats", "goal": "Goal", "mcps": "MCPs", "mcp": "MCPs", "skills": "Skills"}
				title, supported := titles[name]
				if !supported {
					return false
				}
				text := formatSkills(skillEntries)
				if name != "skills" {
					var commandErr error
					text, commandErr = command(ctx, name, "")
					if commandErr != nil {
						text = "Error: " + commandErr.Error()
					}
				}
				var closeDock func()
				body := &tui.TextView{Text: text, OnCancel: func() {
					if closeDock != nil {
						closeDock()
					}
				}}
				panel := tui.Dialog(title, body)
				closeDock = mountDock(panel)
				return true
			}
			var closeDock func()
			pickerModels := modelSnapshot()
			activeProvider := view.Provider()
			hasActiveModels := false
			for _, candidate := range pickerModels {
				hasActiveModels = hasActiveModels || candidate.Provider == activeProvider
			}
			const manualModelID = "__midas_enter_model__"
			if activeProvider != "" && !hasActiveModels {
				pickerModels = append([]ai.Model{{ID: manualModelID, Name: "Enter model ID…", Provider: activeProvider}}, pickerModels...)
			}
			selectModel := func(selected ai.Model) {
				selectedProvider, _, key, err := providerpkg.ConfiguredProvider(selected.Provider, selected.ID, selected.API, selected.BaseURL, getenv)
				if err != nil {
					view.Notice("Error: " + err.Error())
					return
				}
				if err := session.SetRuntime(selectedProvider, selected, key); err != nil {
					view.Notice("Error: " + err.Error())
					return
				}
				nextThinking := currentThinking()
				if !selected.Reasoning {
					nextThinking = ai.ThinkingOff
				} else if nextThinking == "" || nextThinking == ai.ThinkingOff {
					nextThinking = ai.ThinkingMedium
				}
				if err := session.SetReasoning(nextThinking); err != nil {
					view.Notice("Error: " + err.Error())
					return
				}
				// The choice belongs to the active agent: it becomes that agent's
				// session model, its Last Used default, and the level this model uses.
				ref := selected.Provider + "/" + selected.ID
				sessionAgentModels[currentProfile] = ref
				if err := agentModels.SetLastUsed(currentProfile, ref); err != nil {
					view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
				}
				if err := agentModels.SetModelLevel(ref, nextThinking); err != nil {
					view.ShowToast(err.Error(), tui.ToastError, 4*time.Second)
				}
				setThinking(nextThinking)
				view.SetProvider(selected.Provider)
				view.SetModelDetails(selected.ID, selected.Name, selected.ContextWindow)
				view.SetModelReasoning(selected.Reasoning)
				view.SetThinking(nextThinking)
				refreshEditorBorder()
			}
			picker := tui.NewModelPicker(tui.ModelPickerOptions{
				Models: pickerModels,
				OnSelect: func(selected ai.Model) {
					if closeDock != nil {
						closeDock()
					}
					if selected.ID == manualModelID {
						promptField(connectField{Key: "model", Title: crumb("Model", "Custom ID"), Prompt: "Model ID: ", Placeholder: "Provider model ID"}, func(value string) {
							if value == "" {
								view.ShowToast("Model ID is required.", tui.ToastError, 3*time.Second)
								return
							}
							_, model, _, configureErr := providerpkg.ConfiguredProvider(selected.Provider, value, "", "", getenv)
							if configureErr != nil {
								view.Notice("Error: " + configureErr.Error())
								return
							}
							selectModel(model)
						})
						return
					}
					selectModel(selected)
				},
				OnCancel: func() {
					if closeDock != nil {
						closeDock()
					}
				},
			})
			closeDock = mountDock(tui.Dialog("Model", picker))
			return true
		},
	})
	if err != nil {
		return err
	}
	// Delegation resolves a subagent's runtime from the agent's own configuration,
	// falling back to the model of the agent that invoked it. Installing it here is
	// what makes the task tool available, and the task tool is what enforces the
	// agent mode and maxDepth rules at run time.
	session.SetAgentRuntime(func(agentName, inherited string) (chat.AgentRuntime, error) {
		ref := agentModels.Ref(agentName, inherited)
		if strings.TrimSpace(ref) == "" {
			ref = strings.TrimSpace(inherited)
		}
		provider, agentModel, key, resolveErr := agentModelRuntime(agentModels, ref, modelSnapshot(), getenv, session.Model().Provider)
		if resolveErr != nil {
			return chat.AgentRuntime{}, resolveErr
		}
		level := agentModels.Level(agentName, ref, currentThinking())
		if !agentModel.Reasoning {
			level = ai.ThinkingOff
		}
		return chat.AgentRuntime{Provider: provider, Model: agentModel, APIKey: key, Reasoning: level}, nil
	})
	if startupNotice != "" {
		view.ShowToast(startupNotice, tui.ToastWarning, 6*time.Second)
	}
	reportPersist = func(persistErr error) {
		if persistErr != nil {
			view.ShowToast("Could not save the session: "+persistErr.Error(), tui.ToastError, 5*time.Second)
		}
	}
	session.SetOnPersistError(reportPersist)
	session.SetFailover(func(failoverContext context.Context, failed ai.Model, failure error, attempted []ai.Model) (chat.Runtime, bool) {
		if !isAvailabilityFailure(failure) {
			return chat.Runtime{}, false
		}
		values := settingsStore.Load()
		candidates := rotationCandidates(failed, attempted, modelSnapshot(), values, isAuthenticationFailure(failure))
		for _, candidate := range candidates {
			if failoverContext.Err() != nil {
				return chat.Runtime{}, false
			}
			selectedProvider, _, key, configureErr := providerpkg.ConfiguredProvider(candidate.Provider, candidate.ID, candidate.API, candidate.BaseURL, getenv)
			if configureErr == nil {
				return chat.Runtime{Provider: selectedProvider, Model: candidate, APIKey: key}, true
			}
		}
		return chat.Runtime{}, false
	}, func(previous, next ai.Model, _ error) {
		level := currentThinking()
		if !next.Reasoning {
			level = ai.ThinkingOff
			setThinking(level)
		}
		view.SetProvider(next.Provider)
		view.SetModelDetails(next.ID, next.Name, next.ContextWindow)
		view.SetThinking(level)
		refreshEditorBorder()
		view.ShowToast(fmt.Sprintf("%s unavailable; switched to %s/%s.", modelDisplayName(previous), next.Provider, modelDisplayName(next)), tui.ToastWarning, 5*time.Second)
	})
	mountDock = func(component tui.Component) func() {
		view.SetDockComponent(component)
		screen.SetFocus(component)
		closed := false
		return func() {
			if closed {
				return
			}
			closed = true
			if view.ClearDockComponent(component) {
				screen.SetFocus(view)
			}
		}
	}
	refreshEditorBorder()
	viewport := tui.NewChatViewport(view)
	screen.SetLayoutRoot(viewport.Root)
	screen.SetFocus(view)
	setTerminalTitle(sessionTitle(store, session.ID()))
	// The resume hint belongs on the restored screen, so it is deferred before
	// screen.Stop: defers run last-in-first-out, which leaves this print after
	// the alternate screen has been torn down.
	defer printExitSummary(output, store, session)
	screen.Start()
	defer screen.Stop(tui.StopOptions{PreserveScreen: true})
	defer stopRemote(false)
	// The speech model stays on disk and loads on the first /voice, so a session
	// that never dictates never pays for it in memory.
	defer voiceController.Stop()
	// A 25 ms tick eases the t/s reading and advances the activity spinner, so
	// the footer fills in between provider samples exactly like the pre-port UI.
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			view.Close()
			return ctx.Err()
		case <-view.Done():
			return localTerminal.LastError()
		case now := <-tick.C:
			if view.Advance(now) {
				screen.RequestRender(false)
			}
		}
	}
}

func formatSkills(entries []skills.Entry) string {
	if len(entries) == 0 {
		return "No skills discovered."
	}
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, fmt.Sprintf("%d available skills", len(entries)))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s  %s", entry.Name, entry.Scope))
	}
	return strings.Join(lines, "\n")
}

func rotationCandidates(failed ai.Model, attempted, available []ai.Model, values midassettings.Values, authenticationFailure bool) []ai.Model {
	attemptedKeys := make(map[string]bool, len(attempted))
	for _, model := range attempted {
		attemptedKeys[model.Provider+"\x00"+model.ID] = true
	}
	compatible := func(model ai.Model) bool {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Provider) == "" || attemptedKeys[model.Provider+"\x00"+model.ID] {
			return false
		}
		if len(model.Input) == 0 {
			return true
		}
		for _, modality := range model.Input {
			if modality == ai.ModalityText {
				return true
			}
		}
		return false
	}
	score := func(model ai.Model) int {
		value := 0
		if model.API == failed.API {
			value += 4
		}
		if model.Reasoning == failed.Reasoning {
			value += 2
		}
		if model.ContextWindow >= failed.ContextWindow {
			value++
		}
		return value
	}
	ordered := append([]ai.Model(nil), available...)
	slices.SortStableFunc(ordered, func(a, b ai.Model) int {
		left, right := score(a), score(b)
		if left != right {
			return cmp.Compare(right, left)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	candidates := make([]ai.Model, 0, 8)
	if values.AutoModelRotation && !authenticationFailure {
		for _, candidate := range ordered {
			if candidate.Provider == failed.Provider && compatible(candidate) {
				candidates = append(candidates, candidate)
				if len(candidates) == 8 {
					return candidates
				}
			}
		}
	}
	if values.AutoProviderRotation {
		seenProviders := make(map[string]bool)
		for _, candidate := range ordered {
			if candidate.Provider == failed.Provider || seenProviders[candidate.Provider] || !compatible(candidate) {
				continue
			}
			seenProviders[candidate.Provider] = true
			candidates = append(candidates, candidate)
			if len(candidates) == 8 {
				break
			}
		}
	}
	return candidates
}

func isAvailabilityFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var response *ai.HTTPError
	if errors.As(err, &response) {
		return response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status == http.StatusNotFound || response.Status == http.StatusRequestTimeout || response.Status == http.StatusConflict || response.Status == http.StatusTooManyRequests || response.Status >= 500
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"unavailable", "overloaded", "capacity", "rate limit", "timed out", "timeout", "connection refused", "connection reset", "model not found", "no such model"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func isAuthenticationFailure(err error) bool {
	var response *ai.HTTPError
	return errors.As(err, &response) && (response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden)
}

func modelDisplayName(model ai.Model) string {
	if strings.TrimSpace(model.Name) != "" {
		return model.Name
	}
	if strings.TrimSpace(model.ID) != "" {
		return model.ID
	}
	return "model"
}

func canonicalProviderName(providerID, fallback string) string {
	if provider, ok := providercatalog.Lookup(providerID); ok && strings.TrimSpace(provider.Name) != "" {
		return provider.Name
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return providerID
}

// printExitSummary leaves a resume hint on the restored screen so a closed
// session can be picked up again, mirroring the pre-port client.
func printExitSummary(output io.Writer, store *storage.Store, session *chat.Chat) {
	if output == nil || session == nil {
		return
	}
	id := session.ID()
	title, createdAt := sessionSummary(store, id)
	writeExitSummary(output, id, title, createdAt, session.Messages())
}

// writeExitSummary prints the two aligned resume lines, framed by blank lines so
// they read as one block on the restored terminal. Labels stay dim and values
// bold, and the labels share a column so "Session" and "Continue" line up.
func writeExitSummary(output io.Writer, id, title string, createdAt int64, messages []ai.Message) {
	if id == "" || !hasUserPrompt(messages) {
		return
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "New session - " + exitTimestamp(createdAt)
	}
	const labelWidth = len("Continue")
	dim := func(value string) string { return "\x1b[2m" + value + "\x1b[0m" }
	bold := func(value string) string { return "\x1b[1m" + value + "\x1b[0m" }
	label := func(value string) string {
		return dim(value + strings.Repeat(" ", labelWidth-len(value)))
	}
	fmt.Fprint(output, "\n")
	fmt.Fprintf(output, "  %s  %s\n", label("Session"), bold(title))
	fmt.Fprintf(output, "  %s  %s\n", label("Continue"), bold("midas -s "+id))
	fmt.Fprint(output, "\n")
}

// exitTimestamp names an untitled session the way the pre-port client did.
func exitTimestamp(createdAt int64) string {
	at := time.Now()
	if createdAt > 0 {
		at = time.UnixMilli(createdAt)
	}
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// sessionSummary returns a stored session's title and creation time.
func sessionSummary(store *storage.Store, id string) (string, int64) {
	if store == nil || id == "" {
		return "", 0
	}
	for _, session := range store.ReadSessions() {
		if session.ID == id {
			return session.Title, session.CreatedAt
		}
	}
	return "", 0
}

func hasUserPrompt(messages []ai.Message) bool {
	for _, message := range messages {
		switch value := message.(type) {
		case ai.UserMessage:
			if !value.Synthetic {
				return true
			}
		case *ai.UserMessage:
			if value != nil && !value.Synthetic {
				return true
			}
		}
	}
	return false
}

func sessionTitle(store *storage.Store, id string) string {
	title, _ := sessionSummary(store, id)
	return title
}

func currentBranch(root string) string {
	branch, err := gitOutput(root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return branch
}

// environmentFingerprint identifies everything a working directory contributes to
// a run, so a reload only happens when something actually changed. Content, not
// just paths, because editing an AGENTS.md should reach the model too.
func environmentFingerprint(root string, instructionEntries []instructions.Entry, skillEntries []skills.Entry) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "root:%s\n", root)
	for _, entry := range instructionEntries {
		fmt.Fprintf(digest, "instructions:%s:%s\n", entry.Path, entry.Content)
	}
	for _, entry := range skillEntries {
		fmt.Fprintf(digest, "skill:%s:%s\n", entry.Name, entry.Path)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func composeSystemPrompt(base string, instructionEntries []instructions.Entry, skillEntries []skills.Entry) string {
	return skills.AppendPrompt(instructions.AppendPrompt(base, instructionEntries), skillEntries)
}

func visibleSkillGroups(entries []skills.Entry) [][]string {
	global, local := []string{}, []string{}
	for _, entry := range entries {
		if entry.Scope == skills.ScopeGlobal {
			global = append(global, entry.Name)
		} else {
			local = append(local, entry.Name)
		}
	}
	groups := make([][]string, 0, 2)
	if len(global) > 0 {
		groups = append(groups, global)
	}
	if len(local) > 0 {
		groups = append(groups, local)
	}
	return groups
}

// visibleAgentGroups groups the agent catalog for the startup summary: entry
// points, then subagents, then the utility helpers (title and summary), which
// are listed apart because they are never entered or delegated to by hand.
func visibleAgentGroups() [][]string {
	entryPoints, subagents, utilities := []string{}, []string{}, []string{}
	for _, profile := range profiles.Profiles() {
		switch {
		case profiles.IsUtilityAgent(profile.Name):
			utilities = append(utilities, profile.Name)
		case profile.Name == "task" || profile.Mode != "primary":
			subagents = append(subagents, profile.Name)
		default:
			entryPoints = append(entryPoints, profile.Name)
		}
	}
	groups := make([][]string, 0, 3)
	for _, group := range [][]string{entryPoints, subagents, utilities} {
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

func mcpNames(manager *mcp.Manager) []string {
	if manager == nil {
		return nil
	}
	statuses := manager.Statuses()
	result := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if status.State != mcp.StateDisabled {
			result = append(result, status.Name)
		}
	}
	return result
}

func queuedText(queue []storage.QueuedPrompt) []string {
	result := make([]string, 0, len(queue))
	for _, prompt := range queue {
		if strings.TrimSpace(prompt.Text) != "" {
			result = append(result, prompt.Text)
		}
	}
	return result
}

func shellEntries(entries []storage.BashEntry) []tui.ShellEntry {
	result := make([]tui.ShellEntry, len(entries))
	for index, entry := range entries {
		result[index] = tui.ShellEntry{
			Command: entry.Command, Output: entry.Output, Exclude: entry.Exclude,
			Status: entry.Status, ExitCode: entry.ExitCode, At: entry.At,
		}
	}
	return result
}

func sessionShellEntry(entry tui.ShellEntry) storage.BashEntry {
	return storage.BashEntry{
		Command: entry.Command, Output: entry.Output, Exclude: entry.Exclude,
		Status: entry.Status, ExitCode: entry.ExitCode, At: entry.At,
	}
}

func normalizeSessionTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:79]) + "…"
	}
	return value
}

func osc52(value string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(value)) + "\x07"
}

func localCommand(goals *goal.Store, mcps *mcp.Manager, sessionStore *storage.Store, root string, model ai.Model) tui.LocalCommand {
	statsDirectory := ""
	if sessionStore != nil {
		statsDirectory = sessionStore.Directory()
	}
	statsService := internalstats.New(internalstats.Options{Directory: statsDirectory, Scan: internalstats.ScanMidas(sessionStore)})
	return func(ctx context.Context, name, args string) (string, error) {
		switch name {
		case "goal":
			return runGoalCommand(ctx, goals, args)
		case "mcps", "mcp":
			return runMCPCommand(ctx, mcps, args)
		case "stats":
			if strings.TrimSpace(args) != "" {
				return "", errors.New("usage: /stats")
			}
			summary, err := statsService.Read(ctx)
			if err != nil {
				return "", err
			}
			return formatStats(summary), nil
		case "sessions":
			if strings.TrimSpace(args) != "" {
				return "", errors.New("usage: /sessions")
			}
			return formatSessions(sessionStore), nil
		case "agents":
			if strings.TrimSpace(args) != "" {
				return "", errors.New("usage: /agents")
			}
			return formatAgents(), nil
		case "model":
			if strings.TrimSpace(args) != "" {
				return "", errors.New("usage: /model")
			}
			return fmt.Sprintf("Model\n%s/%s\nAPI: %s", model.Provider, model.ID, model.API), nil
		default:
			return "", fmt.Errorf("unknown command /%s", name)
		}
	}
}

// gitOutput runs one git command inside root and returns its trimmed output.
func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func formatStats(summary internalstats.Summary) string {
	lines := []string{
		"Midas stats",
		fmt.Sprintf("Sessions: %d", summary.Sessions),
		fmt.Sprintf("Model calls: %d", summary.Calls),
		fmt.Sprintf("Tokens: %d input, %d output, %d cache read, %d cache write",
			summary.Tokens.Input, summary.Tokens.Output, summary.Tokens.CacheRead, summary.Tokens.CacheWrite),
		fmt.Sprintf("Cost: $%.4f", summary.Cost),
		fmt.Sprintf("Cache: %.1f%% of %d prompt tokens (%d read / %d written / %d uncached)",
			summary.HitRate()*100, summary.Tokens.PromptTokens(), summary.Tokens.CacheRead, summary.Tokens.CacheWrite, summary.Tokens.Input),
	}
	if ranked := statsSessionsByCache(summary.SessionCache); len(ranked) > 0 {
		lines = append(lines, "Sessions by cache:")
		for _, session := range ranked {
			title := strings.TrimSpace(session.Title)
			if title == "" {
				title = "New session"
			}
			lines = append(lines, fmt.Sprintf("  %s  %.1f%%  %d calls  %s", session.ID, session.HitRate()*100, session.Calls, title))
		}
	}
	return strings.Join(lines, "\n")
}

// statsSessionsByCache ranks sessions by how many calls they made, so the
// sessions that actually shaped the lifetime cache rate are listed first.
func statsSessionsByCache(entries []internalstats.SessionCache) []internalstats.SessionCache {
	ranked := append([]internalstats.SessionCache(nil), entries...)
	slices.SortStableFunc(ranked, func(a, b internalstats.SessionCache) int { return cmp.Compare(b.Calls, a.Calls) })
	const limit = 5
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func formatSessions(store *storage.Store) string {
	if store == nil {
		return "No Midas storage."
	}
	listed := store.ReadSessions()
	if len(listed) == 0 {
		return "No Midas storage."
	}
	lines := []string{"Midas sessions"}
	limit := min(len(listed), 20)
	for _, session := range listed[:limit] {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "New session"
		}
		lines = append(lines, fmt.Sprintf("%s  %s", session.ID, title))
	}
	if len(listed) > limit {
		lines = append(lines, fmt.Sprintf("… and %d more", len(listed)-limit))
	}
	return strings.Join(lines, "\n")
}

func formatAgents() string {
	lines := []string{"Midas agents"}
	for _, profile := range profiles.Profiles() {
		lines = append(lines, fmt.Sprintf("%s  [%s]  %s", profile.Name, profile.Mode, profile.Description))
	}
	return strings.Join(lines, "\n")
}

func runGoalCommand(ctx context.Context, store *goal.Store, args string) (string, error) {
	if store == nil {
		return "", errors.New("goal store is unavailable")
	}
	command, rest := splitCommand(args)
	switch command {
	case "", "show", "status":
		current, err := store.Get(ctx)
		if errors.Is(err, goal.ErrNoGoal) {
			return "No current goal. Create one with /goal create <objective>.", nil
		}
		if err != nil {
			return "", err
		}
		return formatGoal(current), nil
	case "create":
		result, err := store.Create(ctx, rest, goal.Limits{})
		if err != nil {
			return "", err
		}
		if result.Reused {
			return "Goal already active.\n" + formatGoal(result.Goal), nil
		}
		return "Goal created.\n" + formatGoal(result.Goal), nil
	case "pause":
		current, err := store.SetStatus(ctx, goal.StatusPaused, rest)
		if err != nil {
			return "", err
		}
		return formatGoal(current), nil
	case "resume":
		current, err := store.SetStatus(ctx, goal.StatusActive, rest)
		if err != nil {
			return "", err
		}
		return formatGoal(current), nil
	case "complete":
		current, err := store.SetStatus(ctx, goal.StatusComplete, rest)
		if err != nil {
			return "", err
		}
		return formatGoal(current), nil
	case "block", "blocked":
		current, err := store.SetStatus(ctx, goal.StatusBlocked, rest)
		if err != nil {
			return "", err
		}
		return formatGoal(current), nil
	case "clear":
		if err := store.Clear(ctx); err != nil {
			return "", err
		}
		return "Goal cleared.", nil
	default:
		return "", fmt.Errorf("usage: /goal [show|create <objective>|pause|resume|complete <evidence>|block <reason>|clear]")
	}
}

func formatGoal(current goal.Goal) string {
	result := fmt.Sprintf("Goal %s [%s]\n%s\nTurns: %d", current.ID, current.Status, current.Objective, current.TurnsUsed)
	if current.TokenBudget > 0 {
		result += fmt.Sprintf("\nTokens: %d/%d", current.TokensUsed, current.TokenBudget)
	}
	if current.Evidence != "" {
		result += "\nEvidence: " + current.Evidence
	}
	if current.Blocker != "" {
		result += "\nBlocker: " + current.Blocker
	}
	return result
}

func runMCPCommand(ctx context.Context, manager *mcp.Manager, args string) (string, error) {
	if manager == nil {
		return "", errors.New("MCP manager is unavailable")
	}
	command, name := splitCommand(args)
	switch command {
	case "", "show", "status", "list":
	case "connect":
		if name == "" {
			return "", errors.New("usage: /mcps connect <name>")
		}
		if err := manager.Connect(ctx, name); err != nil {
			return "", err
		}
	case "disconnect":
		if name == "" {
			return "", errors.New("usage: /mcps disconnect <name>")
		}
		if err := manager.Disconnect(name); err != nil {
			return "", err
		}
	case "connect-all":
		if err := manager.ConnectAll(ctx); err != nil {
			return "", err
		}
	default:
		return "", errors.New("usage: /mcps [list|connect <name>|disconnect <name>|connect-all]")
	}
	statuses := manager.Statuses()
	if len(statuses) == 0 {
		return "No MCP servers configured. Add mcpServers to ~/.midas/settings.json or .midas/settings.json.", nil
	}
	lines := []string{"MCP servers:"}
	for _, status := range statuses {
		line := fmt.Sprintf("%s  %s", status.Name, status.State)
		if status.Tools > 0 {
			line += fmt.Sprintf("  %d tools", status.Tools)
		}
		if status.Error != "" {
			line += "  " + status.Error
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func splitCommand(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if index := strings.IndexAny(value, " \t\n"); index >= 0 {
		return strings.ToLower(value[:index]), strings.TrimSpace(value[index:])
	}
	return strings.ToLower(value), ""
}

func printModels(ctx context.Context, output io.Writer, providerID, modelID, api, baseURL string, getenv func(string) string) error {
	provider, selected, _, err := providerpkg.ConfiguredProvider(providerID, modelID, api, baseURL, getenv)
	if err != nil {
		return err
	}
	models, err := provider.Models(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 && selected.ID != "" {
		models = []ai.Model{selected}
	}
	if len(models) == 0 {
		return fmt.Errorf("provider %s returned no models; set MIDAS_MODEL or pass --model", providerID)
	}
	for _, model := range models {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		fmt.Fprintf(output, "%s/%s\t%s\n", model.Provider, model.ID, name)
	}
	return nil
}

func agentModelRuntime(config tui.AgentModelConfig, ref string, catalog []ai.Model, getenv func(string) string, fallbackProvider string) (ai.Provider, ai.Model, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ai.Model{}, "", fmt.Errorf("no model configured")
	}
	providerID, modelID := splitModel(fallbackProvider, ref)
	if providerID == "" || modelID == "" {
		return nil, ai.Model{}, "", fmt.Errorf("invalid model %q", ref)
	}
	provider, model, key, err := providerpkg.ConfiguredProvider(providerID, modelID, "", "", getenv)
	if err != nil {
		return nil, ai.Model{}, "", err
	}
	return provider, providerpkg.DisplayModelDetails(model, catalog), key, nil
}

func configuredCacheRetention(getenv func(string) string) ai.CacheRetention {
	switch strings.ToLower(strings.TrimSpace(getenv("MIDAS_CACHE_RETENTION"))) {
	case "none":
		return ai.CacheNone
	case "long":
		return ai.CacheLong
	default:
		return ai.CacheShort
	}
}

// compactionSettings maps the stored compaction object onto the agent's own
// settings type.
func compactionSettings(value midassettings.CompactionSettings) agent.CompactionSettings {
	return agent.CompactionSettings{
		Enabled:          value.Enabled,
		ReserveTokens:    value.ReserveTokens,
		KeepRecentTokens: value.KeepRecentTokens,
	}
}

func configuredCacheWarming(getenv func(string) string) agent.CacheWarmingMode {
	switch strings.ToLower(strings.TrimSpace(getenv("MIDAS_CACHE_WARMING"))) {
	case "off":
		return agent.CacheWarmingOff
	case "idle":
		return agent.CacheWarmingIdle
	default:
		return agent.CacheWarmingStreaming
	}
}

func finalAssistant(messages []ai.Message) (ai.AssistantMessage, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		switch message := messages[index].(type) {
		case ai.AssistantMessage:
			return message, true
		case *ai.AssistantMessage:
			if message != nil {
				return *message, true
			}
		}
	}
	return ai.AssistantMessage{}, false
}

func textContent(message ai.AssistantMessage) string {
	var output strings.Builder
	for _, content := range message.Content {
		switch content := content.(type) {
		case ai.TextContent:
			output.WriteString(content.Text)
		case *ai.TextContent:
			if content != nil {
				output.WriteString(content.Text)
			}
		}
	}
	return output.String()
}
