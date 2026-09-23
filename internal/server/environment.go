package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/internal/provider"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// Options configure an environment.
type Options struct {
	// ConfigDir holds the environment's config file.
	ConfigDir string
	// StateDir holds inboxes, group transcripts and browser profile state.
	StateDir string
	// Root is the working directory agents operate in.
	Root string
	// Getenv reads provider credentials.
	Getenv func(string) string
	// Tuning bounds the environment. Zero leaves the defaults in place, and an
	// operator can turn it off entirely with Tuning.Disabled.
	Tuning *Tuning
}

// New prepares an environment: setup, the bus, the shared pool, one long-lived
// agent per configured address, the shared browser, and the API surface. It
// starts no model call and no MCP server until something asks for one.
func New(ctx context.Context, options Options) (*Environment, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	config, err := Setup(options.ConfigDir, options.StateDir)
	if err != nil {
		return nil, err
	}
	broker, err := NewBroker(options.StateDir)
	if err != nil {
		return nil, err
	}
	pool, err := NewPool(options.ConfigDir, options.Root)
	if err != nil {
		return nil, err
	}
	tuning := DefaultTuning()
	if options.Tuning != nil {
		tuning = *options.Tuning
	}
	report, err := ApplyTuning("", tuning)
	if err != nil {
		return nil, err
	}
	environment := &Environment{
		Tuning: tuning, TuningReport: report,
		Config: config, Broker: broker, Registry: NewRegistry(), Leases: NewLeases(5 * time.Minute),
		Pool: pool, VaultMode: config.VaultMode, ConfigDir: options.ConfigDir,
		StartedAt: time.Now(), Feed: NewFeed(), state: newTeamsState(config.Hosts, nil),
		options: options, getenv: getenv, ctx: ctx,
	}
	// Bus traffic is a change like any other, so clients render the environment
	// from one feed instead of watching the bus and the API separately.
	broker.Subscribe(func(envelope Envelope) {
		copied := envelope
		environment.publish(Event{Kind: EventMessage, Action: string(envelope.Kind), At: envelope.At, Message: &copied})
	})

	if err := validateAgentEntries(config.Agents); err != nil {
		return nil, err
	}
	for _, entry := range config.Agents {
		brain, err := environment.brainFor(entry, options, getenv)
		if err != nil {
			return nil, err
		}
		if creator, ok := brain.(interface{ SetCreator(AgentCreator) }); ok {
			creator.SetCreator(environment)
		}
		agent, err := NewAgent(broker, entry.Address, entry.Role, brain)
		if err != nil {
			return nil, err
		}
		environment.Registry.Add(agent)
		environment.rememberAppliedAgent(entry)
		go agent.Run(ctx)
	}
	environment.reapingIdleServers(ctx, 5*time.Minute)
	return environment, nil
}

// brainFor resolves the model and prompt for one agent.
func (e *Environment) brainFor(entry AgentConfig, options Options, getenv func(string) string) (Brain, error) {
	if getenv == nil {
		// An environment built by a test may not read any credentials; a model that
		// needs one then fails to resolve, which is the same answer a real
		// environment gives when the key is missing.
		getenv = func(string) string { return "" }
	}
	ref := strings.TrimSpace(entry.Model)
	if ref == "" {
		ref = strings.TrimSpace(e.Config.Model)
	}
	if ref == "" {
		// Without a model the agent still exists and still answers on the bus; it
		// says it has no model rather than failing to start, so an environment can
		// come up before its credentials do.
		return unconfiguredBrain{}, nil
	}
	providerID, modelID := ai.SplitModelReference("", ref)
	streamer, model, apiKey, err := provider.ConfiguredProvider(providerID, modelID, "", "", getenv)
	if err != nil {
		return nil, fmt.Errorf("server: %s: %w", entry.Address, err)
	}
	return NewBrain(streamer, model, apiKey, entry.Prompt, e.Pool), nil
}

// unconfiguredBrain is an agent with no model: it tells the caller what to set
// rather than failing to start, so an environment can come up before its
// credentials do.
type unconfiguredBrain struct{}

func (unconfiguredBrain) Respond(_ context.Context, request Request) (string, error) {
	return "no model is configured for " + request.Agent + "; set model in the environment config", nil
}

// EnsureBrowser makes the shared browser reachable and returns its endpoint. The
// binary is expected in the image; this only starts it with the profile the
// environment owns, which is what "no configuration" means for an agent.
func (e *Environment) EnsureBrowser(ctx context.Context) (string, error) {
	config := e.Config.Browser
	if strings.TrimSpace(config.Binary) == "" {
		return "", fmt.Errorf("server: no browser is configured; set browser.binary")
	}
	// Reuse the desktop control layer's CDP client: it already knows how to start
	// a Chromium-family browser with a debugging endpoint and wait for it. The
	// tuning's flags are what make it behave on a server rather than a desktop.
	return startBrowser(ctx, config, e.Tuning)
}

// reapingIdleServers stops the shared MCP servers when nothing has used them for
// the idle window. The ticker only exists when there is something to stop, so an
// environment with no MCP configuration runs no timers at all.
func (e *Environment) reapingIdleServers(ctx context.Context, idle time.Duration) {
	if e.Pool == nil || len(e.Pool.Names()) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if stopped := e.Pool.Reap(idle); stopped > 0 {
					e.publish(Event{Kind: EventMCP, Action: "idle-stop", Detail: fmt.Sprintf("%d servers", stopped)})
				}
			}
		}
	}()
}

// Serve runs the API until the context ends.
func (e *Environment) Serve(ctx context.Context, listen string) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("server: listen on %s: %w", listen, err)
	}
	httpServer := &http.Server{
		Handler: e.Handler(), ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout bounds how long a keep-alive connection may sit unused.
		// WriteTimeout is left unset: the change feed streams for as long as a
		// client listens.
		IdleTimeout: 2 * time.Minute,
	}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.Registry.StopAll()
		e.Broker.Close()
		e.Pool.Close()
		if e.Feed != nil {
			e.Feed.Close()
		}
		return httpServer.Shutdown(shutdown)
	}
}
