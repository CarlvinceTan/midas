// Package chat wires Midas' reusable Go packages into an application session.
// Terminal rendering remains in package tui; this layer owns no UI code.
package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/goal"
	"github.com/CarlvinceTan/midas/pkg/mcp"
	"github.com/CarlvinceTan/midas/pkg/storage"
	codingtools "github.com/CarlvinceTan/midas/internal/tools"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var (
	ErrBusy       = errors.New("midas: session is already running")
	ErrNotRunning = errors.New("midas: session is not running")
)

type CodingToolsMode string

const (
	CodingToolsAll      CodingToolsMode = "all"
	CodingToolsReadOnly CodingToolsMode = "read-only"
	CodingToolsNone     CodingToolsMode = "none"
)

type Options struct {
	Root          string
	SessionID     string
	Agent         string
	SystemPrompt  string
	Provider      ai.Streamer
	Model         ai.Model
	StreamOptions ai.StreamOptions
	ToolExecution agent.ToolExecutionMode
	CacheWarming  agent.CacheWarmingMode
	TitleMaxWords int
	// Environment reports what the working directory contributes to a run: the
	// text the model must see when it changes, and a fingerprint of its inputs.
	// Only a change appends anything, so ordinary turns stay byte-identical and
	// keep hitting the provider's prompt cache.
	Environment func(root string) (Environment, error)
	OnCacheWarm func(ai.Usage)
	// OnPersistError reports a transcript or session-metadata write that failed,
	// so a full disk or a read-only config directory is visible instead of
	// silently dropping history. Runs continue either way.
	OnPersistError  func(error)
	Sessions        *storage.Store
	Goals           *goal.Store
	MCP             *mcp.Manager
	AdditionalTools []agent.Tool
	CodingTools     CodingToolsMode
	// Compaction replaces older context with a checkpoint when a run would
	// otherwise exceed the model's window.
	Compaction agent.CompactionSettings
	// SystemSuffix is the instruction and skill text every agent's prompt carries.
	// A subagent's prompt is its own plus this suffix, so a child works under the
	// same project rules as the agent that spawned it.
	SystemSuffix string
	// AgentRuntime resolves the runtime a named agent runs with when another agent
	// invokes it. Nil disables delegation for the session.
	AgentRuntime func(agent string, inherited string) (AgentRuntime, error)
}

// SetAgentRuntime installs the resolver delegation uses. It is set once the
// session can resolve agent models, which is after the command has loaded the
// configured agents and their credentials.
func (c *Chat) SetAgentRuntime(resolve func(agent string, inherited string) (AgentRuntime, error)) {
	c.mu.Lock()
	c.options.AgentRuntime = resolve
	c.mu.Unlock()
}

// Environment is the working-directory context a run needs beyond tools and
// history. Block is what the model is told when the directory changes;
// Fingerprint identifies the inputs so an unchanged environment appends nothing.
type Environment struct {
	Fingerprint string
	Block       string
}

// Runtime is a fully configured provider/model pair selected after an
// availability failure. APIKey is copied into the next attempt's stream
// options and may be empty for OAuth-backed providers.
type Runtime struct {
	Provider ai.Streamer
	Model    ai.Model
	APIKey   string
}

type FailoverFunc func(ctx context.Context, failed ai.Model, failure error, attempted []ai.Model) (Runtime, bool)
type FailoverNotice func(previous, next ai.Model, failure error)

type Chat struct {
	options   Options
	baseTools []agent.Tool
	kit       *codingtools.Toolkit
	root      string
	id        string
	agent     string
	// environmentFingerprint is the last environment this conversation was told
	// about, so a reload appends its block exactly once.
	environmentFingerprint string

	mu         sync.Mutex
	messages   []ai.Message
	running    bool
	cancel     context.CancelCauseFunc
	steering   *agent.SteeringQueue
	warmer     *agent.CacheWarmer
	failover   FailoverFunc
	onFailover FailoverNotice
}

func New(options Options) (*Chat, error) {
	if options.Provider == nil {
		return nil, errors.New("midas: provider is required")
	}
	root := options.Root
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("midas: resolve root: %w", err)
	}
	kit, err := codingtools.New(abs)
	if err != nil {
		return nil, err
	}
	available, err := profileTools(kit, options.CodingTools, options.Goals, options.AdditionalTools)
	if err != nil {
		return nil, err
	}
	if err := uniqueTools(available); err != nil {
		return nil, err
	}
	id := options.SessionID
	if id == "" {
		id, err = sessionID()
		if err != nil {
			return nil, err
		}
	}
	if options.SystemPrompt == "" {
		options.SystemPrompt = "You are Midas, a concise coding agent. Inspect before editing, preserve unrelated work, use the smallest sufficient toolset, and verify changes before reporting completion. Write temporary or generated scratch files under the system temp directory ($TMPDIR, usually /tmp), never in the working tree."
	}
	if options.ToolExecution == "" {
		options.ToolExecution = agent.ToolExecutionParallel
	}
	if options.StreamOptions.SessionID == "" {
		options.StreamOptions.SessionID = id
	}
	if options.StreamOptions.CacheRetention == "" {
		options.StreamOptions.CacheRetention = ai.CacheShort
	}
	profile := strings.TrimSpace(options.Agent)
	if profile == "" {
		profile = "main"
	}
	chat := &Chat{options: options, baseTools: available, kit: kit, root: abs, id: id, agent: profile}
	if options.CacheWarming != agent.CacheWarmingOff {
		chat.warmer = agent.NewCacheWarmer(options.Provider, options.OnCacheWarm)
	}
	if options.Sessions != nil {
		messages, loadErr := options.Sessions.ReadTranscript(id)
		if loadErr != nil {
			return nil, loadErr
		}
		chat.messages = messages
		if err := options.Sessions.UpsertSession(storage.SessionUpdate{ID: id, CWD: abs}); err != nil && options.OnPersistError != nil {
			options.OnPersistError(err)
		}
	}
	return chat, nil
}

func (c *Chat) ID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

func (c *Chat) Root() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.root
}

// SetOnPersistError installs the hook called when a transcript or session
// metadata write fails. It may be called after construction, once the UI that
// shows the failure exists.
func (c *Chat) SetOnPersistError(report func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.options.OnPersistError = report
}

// Tools lists the tool definitions a run would send. A duplicate or nil
// definition is reported rather than hidden behind an empty list.
func (c *Chat) Tools() ([]ai.Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tools, err := c.availableToolsLocked()
	if err != nil {
		return nil, err
	}
	definitions := make([]ai.Tool, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, tool.Definition())
	}
	return definitions, nil
}

func (c *Chat) Messages() []ai.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ai.Message(nil), c.messages...)
}

func (c *Chat) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// SetModel changes the model used by subsequent turns. Active runs keep the
// model snapshot they started with and cannot be retargeted midway through.
func (c *Chat) SetModel(model ai.Model) error {
	if strings.TrimSpace(model.ID) == "" {
		return errors.New("midas: model is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return ErrBusy
	}
	c.options.Model = model
	if c.warmer != nil {
		c.warmer.Stop()
	}
	return nil
}

// SetRuntime atomically changes both provider and model for subsequent turns.
// This is used when the interactive model picker crosses provider boundaries.
func (c *Chat) SetRuntime(provider ai.Streamer, model ai.Model, apiKey string) error {
	if provider == nil {
		return errors.New("midas: provider is required")
	}
	if strings.TrimSpace(model.ID) == "" {
		return errors.New("midas: model is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return ErrBusy
	}
	c.options.Provider = provider
	c.options.Model = model
	c.options.StreamOptions.APIKey = apiKey
	if c.warmer != nil {
		c.warmer.Close()
		c.warmer = agent.NewCacheWarmer(provider, c.options.OnCacheWarm)
	}
	return nil
}

// UseEnvironment installs the working-directory environment provider. It is set
// after construction because the provider needs the session it belongs to.
func (c *Chat) UseEnvironment(provider func(root string) (Environment, error)) error {
	if provider == nil {
		return errors.New("midas: an environment provider is required")
	}
	c.mu.Lock()
	c.options.Environment = provider
	c.mu.Unlock()
	return nil
}

// MCPConfigs describes the MCP servers this session is running with, so a reload
// can tell whether the configuration on disk names different ones.
func (c *Chat) MCPConfigs() map[string]mcp.Config {
	c.mu.Lock()
	manager := c.options.MCP
	c.mu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Configs()
}

// SetSystemSuffix replaces the instruction and skill text appended to every
// agent's prompt, which is what a reload does after re-reading them.
func (c *Chat) SetSystemSuffix(suffix string) {
	c.mu.Lock()
	c.options.SystemSuffix = suffix
	c.mu.Unlock()
}

// SetMCP replaces the MCP manager whose tools a run exposes. A directory change
// may bring its own project-scoped servers, and their tools have to follow.
func (c *Chat) SetMCP(manager *mcp.Manager) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	available, err := profileTools(c.kit, c.options.CodingTools, c.options.Goals, c.options.AdditionalTools)
	if err != nil {
		return err
	}
	c.options.MCP = manager
	c.baseTools = available
	return nil
}

// ChangeDirectory moves the conversation to another working directory. The
// coding tools follow it, and the next turn is told what the new directory
// contributes. History and the system prompt are left alone, which is what keeps
// the provider's cached prefix intact across the move.
func (c *Chat) ChangeDirectory(root string) error {
	absolute, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return fmt.Errorf("midas: resolve %s: %w", root, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return fmt.Errorf("midas: %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("midas: %s is not a directory", absolute)
	}
	kit, err := codingtools.New(absolute)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("midas: finish the current run before changing directory")
	}
	available, err := profileTools(kit, c.options.CodingTools, c.options.Goals, c.options.AdditionalTools)
	if err != nil {
		return err
	}
	c.root = absolute
	c.kit = kit
	c.options.Root = absolute
	// The coding tools resolve paths against the directory they were built with,
	// so they are rebuilt here: leaving them would keep writes pointing at the
	// directory the session started in.
	c.baseTools = available
	return nil
}

// environmentNotice returns the message that tells the model its working
// directory changed, or reports that nothing did. It is consulted once per turn.
func (c *Chat) environmentNotice() (ai.Message, bool) {
	c.mu.Lock()
	environmentFn := c.options.Environment
	root := c.root
	c.mu.Unlock()
	if environmentFn == nil {
		return nil, false
	}
	environment, err := environmentFn(root)
	if err != nil {
		return nil, false
	}
	c.mu.Lock()
	unchanged := environment.Fingerprint == c.environmentFingerprint
	first := c.environmentFingerprint == ""
	c.environmentFingerprint = environment.Fingerprint
	c.mu.Unlock()
	if unchanged || first || strings.TrimSpace(environment.Block) == "" {
		// The opening turn already carries the environment in its system prompt.
		return nil, false
	}
	return ai.NewSyntheticMessage(environment.Block, time.Now()), true
}

// SetProfile atomically replaces the system prompt and profile-scoped tools.
func (c *Chat) SetProfile(name, systemPrompt string, mode CodingToolsMode, additional []agent.Tool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	available, err := profileTools(c.kit, mode, c.options.Goals, additional)
	if err != nil {
		return err
	}
	if c.options.MCP != nil {
		if err := uniqueTools(append(append([]agent.Tool(nil), available...), c.options.MCP.Tools()...)); err != nil {
			return err
		}
	}
	if c.running {
		return ErrBusy
	}
	c.options.SystemPrompt = systemPrompt
	c.options.CodingTools = mode
	c.options.AdditionalTools = append([]agent.Tool(nil), additional...)
	c.baseTools = available
	c.agent = strings.TrimSpace(name)
	if c.agent == "" {
		c.agent = "main"
	}
	if c.warmer != nil {
		c.warmer.Stop()
	}
	return nil
}

func (c *Chat) Model() ai.Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.options.Model
}

func (c *Chat) Reasoning() ai.ThinkingLevel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.options.StreamOptions.Reasoning
}

func (c *Chat) SetReasoning(level ai.ThinkingLevel) error {
	switch level {
	case ai.ThinkingOff, ai.ThinkingMinimal, ai.ThinkingLow, ai.ThinkingMedium, ai.ThinkingHigh, ai.ThinkingXHigh, ai.ThinkingMax:
	default:
		return fmt.Errorf("midas: unknown thinking level %q", level)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.options.StreamOptions.Reasoning = level
	return nil
}

func (c *Chat) SetTitleMaxWords(value int) {
	if value < 1 {
		value = 1
	}
	if value > 20 {
		value = 20
	}
	c.mu.Lock()
	c.options.TitleMaxWords = value
	c.mu.Unlock()
}

// SetFailover configures availability-only runtime selection. The callback is
// consulted after the provider's own retries are exhausted and only before
// any assistant content or tool execution has been exposed.
func (c *Chat) SetFailover(selectRuntime FailoverFunc, notice FailoverNotice) {
	c.mu.Lock()
	c.failover = selectRuntime
	c.onFailover = notice
	c.mu.Unlock()
}

// SwitchSession replaces the current native Midas transcript while retaining
// the configured provider, profile, tools, and working directory. An empty id
// creates a fresh session.
func (c *Chat) SwitchSession(id string) (string, []ai.Message, error) {
	if c.options.Sessions == nil {
		return "", nil, errors.New("midas: session store is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		var err error
		id, err = sessionID()
		if err != nil {
			return "", nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return "", nil, ErrBusy
	}
	messages, err := c.options.Sessions.ReadTranscript(id)
	if err != nil {
		return "", nil, err
	}
	c.id = id
	c.messages = append([]ai.Message(nil), messages...)
	c.options.StreamOptions.SessionID = id
	if c.warmer != nil {
		c.warmer.Stop()
	}
	if err := c.options.Sessions.UpsertSession(storage.SessionUpdate{ID: id, CWD: c.root}); err != nil && c.options.OnPersistError != nil {
		c.options.OnPersistError(err)
	}
	return id, append([]ai.Message(nil), messages...), nil
}

type Run struct {
	*ai.EventStream[agent.Event, []ai.Message]
	cancel context.CancelCauseFunc
}

func (r *Run) Cancel(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	r.cancel(cause)
}

func (c *Chat) Prompt(ctx context.Context, text string) (*Run, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("midas: prompt must not be empty")
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, ErrBusy
	}
	if strings.TrimSpace(c.options.Model.ID) == "" {
		c.mu.Unlock()
		return nil, errors.New("midas: select a model with /model before sending a prompt")
	}
	c.running = true
	history := append([]ai.Message(nil), c.messages...)
	tools, toolsErr := c.availableToolsLocked()
	if toolsErr != nil {
		c.running = false
		c.mu.Unlock()
		return nil, toolsErr
	}
	runContext, cancel := context.WithCancelCause(ctx)
	c.cancel = cancel
	steering := agent.NewSteeringQueue()
	c.steering = steering
	runtime := Runtime{Provider: c.options.Provider, Model: c.options.Model, APIKey: c.options.StreamOptions.APIKey}
	streamOptions := c.options.StreamOptions
	promptAgent := c.agent
	promptThinking := streamOptions.Reasoning
	loopContext := agent.Context{
		SystemPrompt: c.options.SystemPrompt,
		Messages:     history,
		Tools:        tools,
	}
	loopConfig := agent.Config{
		Provider:      runtime.Provider,
		Model:         runtime.Model,
		StreamOptions: streamOptions,
		ToolExecution: c.options.ToolExecution,
		Steering:      steering,
		CacheWarming:  c.options.CacheWarming,
		CacheWarmer:   c.warmer,
		OnCacheWarm:   c.options.OnCacheWarm,
	}
	selectFailover := c.failover
	onFailover := c.onFailover
	c.mu.Unlock()

	stream := &Run{
		EventStream: ai.NewEventStream(func(event agent.Event) ([]ai.Message, bool) {
			return event.Messages, event.Type == agent.EventAgentEnd
		}),
		cancel: cancel,
	}
	go c.forward(runContext, text, promptAgent, promptThinking, loopContext, loopConfig, selectFailover, onFailover, stream)
	return stream, nil
}

// Compact summarizes the older part of the conversation now and returns the
// rewritten history. It reports false when there is nothing old enough to
// replace, and refuses while a run is active.
func (c *Chat) Compact(ctx context.Context) ([]ai.Message, bool, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, false, ErrBusy
	}
	history := append([]ai.Message(nil), c.messages...)
	provider, model := c.options.Provider, c.options.Model
	streamOptions := c.options.StreamOptions
	settings := c.options.Compaction
	c.mu.Unlock()
	compacted, done, err := agent.Compact(ctx, provider, model, streamOptions, history, settings.Normalize())
	if err != nil || !done {
		return nil, done, err
	}
	c.replaceMessages(compacted)
	return compacted, true, nil
}

// recordGoalTurn counts a finished turn's tokens against the active goal and
// returns the notice to show when that exhausts one of the goal's limits. A run
// that hits a limit stops: the caller returns after pushing the notice, so a
// budget is never silently overspent. No goal, or a goal that is no longer
// active, records nothing.
func (c *Chat) recordGoalTurn(ctx context.Context, messages []ai.Message) string {
	c.mu.Lock()
	goals := c.options.Goals
	c.mu.Unlock()
	if goals == nil {
		return ""
	}
	var tokens int64
	for _, message := range messages {
		assistant, ok := message.(ai.AssistantMessage)
		if !ok {
			continue
		}
		tokens += int64(assistant.Usage.Input + assistant.Usage.Output + assistant.Usage.CacheRead + assistant.Usage.CacheWrite)
	}
	_, reason, err := goals.RecordTurn(ctx, tokens)
	if err != nil {
		return ""
	}
	switch reason {
	case goal.LimitTokens:
		return "Goal token budget reached; stopping this run."
	case goal.LimitTurns:
		return "Goal turn limit reached; stopping this run."
	case goal.LimitDuration:
		return "Goal duration limit reached; stopping this run."
	}
	return ""
}

// replaceMessages swaps the session's conversation and persists it, which is how
// a compaction or revert becomes durable. A persistence failure is reported to
// OnPersistError; the in-memory conversation is still correct.
func (c *Chat) replaceMessages(messages []ai.Message) {
	c.mu.Lock()
	c.messages = append([]ai.Message(nil), messages...)
	history := append([]ai.Message(nil), c.messages...)
	sessions, id, report := c.options.Sessions, c.id, c.options.OnPersistError
	c.mu.Unlock()
	if sessions == nil {
		return
	}
	if err := sessions.WriteTranscript(id, history); err != nil && report != nil {
		report(err)
	}
}

// Steer admits a user message into the active run. A successful return means
// the agent loop owns the message and will process it at its next step boundary.
func (c *Chat) Steer(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("midas: steer prompt must not be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running || c.steering == nil {
		return ErrNotRunning
	}
	message := ai.NewUserMessage(text, time.Now())
	message.Agent = c.agent
	message.Thinking = c.options.StreamOptions.Reasoning
	if !c.steering.Push(message) {
		return ErrNotRunning
	}
	return nil
}

func (c *Chat) availableToolsLocked() ([]agent.Tool, error) {
	available := append([]agent.Tool(nil), c.baseTools...)
	// The session's entry agent is depth zero, so its task tool starts the chain.
	if task := c.taskToolForLocked(c.agent, 0); task != nil {
		available = append(available, task)
	}
	if c.options.MCP != nil {
		available = append(available, c.options.MCP.Tools()...)
	}
	if err := uniqueTools(available); err != nil {
		return nil, err
	}
	return available, nil
}

func profileTools(kit *codingtools.Toolkit, mode CodingToolsMode, goals *goal.Store, additional []agent.Tool) ([]agent.Tool, error) {
	if mode == "" {
		mode = CodingToolsAll
	}
	var available []agent.Tool
	switch mode {
	case CodingToolsAll:
		available = kit.All()
		if goals != nil {
			available = append(available, goal.Tools(goals)...)
		}
	case CodingToolsReadOnly:
		available = kit.ReadOnly()
	case CodingToolsNone:
		if goals != nil {
			available = append(available, goal.Tools(goals)...)
		}
	default:
		return nil, fmt.Errorf("midas: unknown coding tools mode %q", mode)
	}
	available = append(available, additional...)
	if err := uniqueTools(available); err != nil {
		return nil, err
	}
	return available, nil
}

func (c *Chat) Abort(cause error) bool {
	c.mu.Lock()
	cancel, running := c.cancel, c.running
	c.mu.Unlock()
	if running && cancel != nil {
		if cause == nil {
			cause = context.Canceled
		}
		cancel(cause)
	}
	return running
}

func (c *Chat) forward(ctx context.Context, prompt, promptAgent string, promptThinking ai.ThinkingLevel, loopContext agent.Context, config agent.Config, selectFailover FailoverFunc, onFailover FailoverNotice, output *Run) {
	// finish releases the run. It is idempotent, and it is called before the
	// terminal event is pushed: the stream closes on that event, and a consumer
	// that sees the run end must not still find the chat busy.
	finish := func() {
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.steering = nil
		c.mu.Unlock()
	}
	defer finish()
	promptMessage := ai.NewUserMessage(prompt, time.Now())
	promptMessage.Agent = promptAgent
	promptMessage.Thinking = promptThinking
	// A changed working directory is reported as a synthetic message at the end of
	// the conversation rather than by rebuilding the system prompt: everything
	// before it stays byte-identical, so the provider's cached prefix survives the
	// move and only the block itself is new.
	if notice, appended := c.environmentNotice(); appended {
		loopContext.Messages = append(loopContext.Messages, notice)
		c.appendMessages("", []ai.Message{notice})
	}
	attempted := []ai.Model{config.Model}
	firstAttempt := true
	// Auto-compaction runs before the first turn so the request that follows
	// already fits the model's window, exactly like Pi's pre-turn compaction.
	if model := config.Model; config.Provider != nil && model.ContextWindow > 0 {
		settings := c.options.Compaction
		before := agent.ContextTokens(loopContext.Messages)
		if agent.ShouldCompact(before, model.ContextWindow, settings) {
			compacted, done, compactErr := agent.Compact(ctx, config.Provider, model, config.StreamOptions, loopContext.Messages, settings)
			switch {
			case compactErr != nil:
				output.Push(agent.Event{Type: agent.EventCompaction, Err: compactErr})
			case done:
				c.replaceMessages(compacted)
				loopContext.Messages = compacted
				output.Push(agent.Event{
					Type:         agent.EventCompaction,
					TokensBefore: before,
					TokensAfter:  agent.ContextTokens(compacted),
				})
			}
		}
	}
	for {
		upstream := agent.Run(ctx, []ai.Message{promptMessage}, loopContext, config)
		buffered := make([]agent.Event, 0, 4)
		meaningful := false
		for {
			event, ok, err := upstream.Next(ctx)
			if err != nil {
				message := ai.AssistantMessage{Role: ai.RoleAssistant, Provider: config.Model.Provider, Model: config.Model.ID, StopReason: ai.StopAborted, ErrorMessage: err.Error(), Timestamp: ai.UnixMillis(time.Now())}
				messages := []ai.Message{promptMessage, message}
				c.appendMessages(prompt, messages)
				finish()
				output.Push(agent.Event{Type: agent.EventAgentEnd, Messages: messages, Err: err})
				return
			}
			if !ok {
				return
			}

			if event.Type == agent.EventAgentEnd {
				if event.Err != nil && !meaningful && selectFailover != nil && ctx.Err() == nil {
					next, selected := selectFailover(ctx, config.Model, event.Err, append([]ai.Model(nil), attempted...))
					if selected && next.Provider != nil && strings.TrimSpace(next.Model.ID) != "" {
						previous := config.Model
						attempted = append(attempted, next.Model)
						config = c.applyFailoverRuntime(config, next)
						if onFailover != nil {
							onFailover(previous, next.Model, event.Err)
						}
						firstAttempt = false
						break
					}
				}
				for _, pending := range buffered {
					output.Push(pending)
				}
				c.appendMessages(prompt, event.Messages)
				finish()
				output.Push(event)
				if notice := c.recordGoalTurn(ctx, event.Messages); notice != "" {
					output.Push(agent.Event{Type: agent.EventNotice, Notice: notice})
				}
				return
			}

			if !firstAttempt && isAttemptPrefix(event) {
				continue
			}
			if meaningfulEvent(event) {
				meaningful = true
				for _, pending := range buffered {
					output.Push(pending)
				}
				buffered = buffered[:0]
				output.Push(event)
				continue
			}
			if meaningful {
				output.Push(event)
			} else if isFailureCandidateEvent(event) {
				buffered = append(buffered, event)
			} else {
				output.Push(event)
			}
		}
	}
}

func (c *Chat) applyFailoverRuntime(config agent.Config, runtime Runtime) agent.Config {
	c.mu.Lock()
	c.options.Provider = runtime.Provider
	c.options.Model = runtime.Model
	c.options.StreamOptions.APIKey = runtime.APIKey
	if !runtime.Model.Reasoning {
		c.options.StreamOptions.Reasoning = ai.ThinkingOff
	}
	if c.warmer != nil {
		c.warmer.Close()
		c.warmer = agent.NewCacheWarmer(runtime.Provider, c.options.OnCacheWarm)
	}
	config.Provider = runtime.Provider
	config.Model = runtime.Model
	config.StreamOptions = c.options.StreamOptions
	config.CacheWarmer = c.warmer
	c.mu.Unlock()
	return config
}

func isAttemptPrefix(event agent.Event) bool {
	if event.Type == agent.EventAgentStart || event.Type == agent.EventTurnStart {
		return true
	}
	if event.Type != agent.EventMessageStart && event.Type != agent.EventMessageEnd {
		return false
	}
	_, user := event.Message.(ai.UserMessage)
	return user
}

func meaningfulEvent(event agent.Event) bool {
	switch event.Type {
	case agent.EventMessageStart:
		message, ok := event.Message.(ai.AssistantMessage)
		return ok && len(message.Content) > 0
	case agent.EventMessageUpdate, agent.EventToolExecutionStart, agent.EventToolExecutionUpdate, agent.EventToolExecutionEnd:
		return true
	case agent.EventMessageEnd:
		message, ok := event.Message.(ai.AssistantMessage)
		return ok && message.StopReason != ai.StopError && message.StopReason != ai.StopAborted
	default:
		return false
	}
}

func isFailureCandidateEvent(event agent.Event) bool {
	switch event.Type {
	case agent.EventMessageStart, agent.EventMessageEnd, agent.EventTurnEnd:
		_, assistant := event.Message.(ai.AssistantMessage)
		return assistant
	default:
		return false
	}
}

// RevertLastPrompt removes the last user prompt and everything after it,
// returning the removed prompt text and the remaining conversation. It reports
// false when the session has no prompt left to undo.
func (c *Chat) RevertLastPrompt() (string, []ai.Message, bool, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return "", nil, false, ErrBusy
	}
	index := -1
	for position := len(c.messages) - 1; position >= 0; position-- {
		user, ok := c.messages[position].(ai.UserMessage)
		if ok && !user.Synthetic {
			index = position
			break
		}
	}
	if index < 0 {
		c.mu.Unlock()
		return "", nil, false, nil
	}
	prompt := userPromptText(c.messages[index])
	c.messages = append([]ai.Message(nil), c.messages[:index]...)
	remaining := append([]ai.Message(nil), c.messages...)
	c.mu.Unlock()
	if c.options.Sessions == nil {
		return prompt, remaining, true, nil
	}
	if err := c.options.Sessions.WriteTranscript(c.id, remaining); err != nil {
		return prompt, remaining, true, err
	}
	return prompt, remaining, true, nil
}

// userPromptText renders the text a stored user message carried, ignoring
// attachments and chips.
func userPromptText(message ai.Message) string {
	user, ok := message.(ai.UserMessage)
	if !ok {
		return ""
	}
	text, _ := user.Content.Text()
	return strings.TrimSpace(text)
}

func (c *Chat) appendMessages(prompt string, messages []ai.Message) {
	c.mu.Lock()
	c.messages = append(c.messages, messages...)
	history := append([]ai.Message(nil), c.messages...)
	titleMaxWords := c.options.TitleMaxWords
	sessions, id, root, report := c.options.Sessions, c.id, c.root, c.options.OnPersistError
	c.mu.Unlock()
	if sessions == nil {
		return
	}
	title := generatedTitle(prompt, titleMaxWords)
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:79]) + "…"
	}
	if err := sessions.UpsertSession(storage.SessionUpdate{ID: id, CWD: root, Title: &title}); err != nil && report != nil {
		report(err)
	}
	if err := sessions.WriteTranscript(id, history); err != nil && report != nil {
		report(err)
	}
}

// generatedTitle is the placeholder session title written before the title
// helper answers: the opening request's first words, capitalized like the
// generated titles so the session picker reads consistently.
func generatedTitle(prompt string, maximum int) string {
	words := strings.Fields(prompt)
	if maximum <= 0 {
		maximum = 8
	}
	if len(words) > maximum {
		words = words[:maximum]
	}
	title := strings.Join(words, " ")
	first := []rune(title)
	if len(first) == 0 {
		return title
	}
	return strings.ToUpper(string(first[0])) + string(first[1:])
}

func uniqueTools(available []agent.Tool) error {
	seen := make(map[string]bool, len(available))
	for _, tool := range available {
		if tool == nil {
			return errors.New("midas: nil tool")
		}
		name := tool.Definition().Name
		if name == "" {
			return errors.New("midas: tool name is required")
		}
		if seen[name] {
			return fmt.Errorf("midas: duplicate tool %q", name)
		}
		seen[name] = true
	}
	return nil
}

func sessionID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("midas: generate session id: %w", err)
	}
	return "ses_" + hex.EncodeToString(data), nil
}
