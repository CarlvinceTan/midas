package chat

// This file is the delegation path: the task tool runs another agent as a child
// of the calling run. Delegation is a first-class operation here rather than an
// invitation in a prompt, because the rules have to hold in code: the target must
// be a subagent, an agent that cannot act cannot delegate, and the calling
// agent's maxDepth caps how many levels may run beneath it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CarlvinceTan/midas/internal/profiles"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

const (
	// taskProgressLimit bounds the text streamed back to the caller as the child
	// works, and taskResultLimit bounds what the child's answer contributes to the
	// caller's context.
	taskProgressLimit = 8 * 1024
	taskResultLimit   = 50 * 1024
)

// AgentRuntime is a fully resolved child run: the provider, the model, the key,
// and the reasoning level that model should run with for that agent. It is
// resolved by the caller of the package, which owns the configured agents and
// their credentials.
type AgentRuntime struct {
	Provider  ai.Provider
	Model     ai.Model
	APIKey    string
	Reasoning ai.ThinkingLevel
}

// taskTool runs another agent to completion and returns its answer.
type taskTool struct {
	chat *Chat
	// agent is the agent that runs with this tool, and depth is how far below the
	// session's entry agent it sits. The caller's own maxDepth decides whether depth
	// has room for another level.
	agent string
	depth int
}

func (t taskTool) Definition() ai.Tool {
	return ai.Tool{
		Name:        "task",
		Description: "Run one of the subagents on a bounded piece of work and return its answer. Use it for reconnaissance you do not need to watch, and for independent opinions you would otherwise have to form yourself. The subagent starts with no memory of this conversation, so its prompt must be self-contained.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent": {
      "type": "string",
      "description": "Name of the subagent to run, for example explore or advisor."
    },
    "prompt": {
      "type": "string",
      "description": "The complete instruction for the subagent. It cannot see this conversation."
    }
  },
  "required": ["agent", "prompt"],
  "additionalProperties": false
}`),
	}
}

func (t taskTool) Execute(ctx context.Context, call ai.ToolCall, update agent.ToolUpdate) (agent.ToolResult, error) {
	target := strings.TrimSpace(stringArgument(call.Arguments["agent"]))
	prompt := strings.TrimSpace(stringArgument(call.Arguments["prompt"]))
	if target == "" || prompt == "" {
		return agent.ToolResult{}, errors.New("task: an agent name and a prompt are required")
	}
	// The caller's cap decides whether another level may run beneath it.
	depth := t.depth + 1
	if maxDepth := profiles.MaxDepth(t.agent); t.depth >= maxDepth {
		return agent.ToolResult{}, fmt.Errorf(
			"task: %s is at its subagent depth limit (%d levels), so it cannot run %s", t.agent, maxDepth, target)
	}
	// Only subagents can be delegated to: a primary agent is the session itself and
	// a utility agent is Midas' own helper, not a worker.
	if !profiles.CanDelegateTo(target) {
		if _, err := profiles.ResolveProfile(target); err != nil {
			return agent.ToolResult{}, fmt.Errorf("task: %w", err)
		}
		return agent.ToolResult{}, fmt.Errorf("task: %s is not a subagent, so it cannot be run as one", target)
	}
	if target == t.agent {
		return agent.ToolResult{}, fmt.Errorf("task: %s cannot run itself", target)
	}
	child, err := profiles.ResolveProfile(target)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("task: %w", err)
	}
	runtime, err := t.chat.agentRuntime(target, modelRef(t.chat.Model()))
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("task: %s cannot run: %w", target, err)
	}
	provider, model, key := runtime.Provider, runtime.Model, runtime.APIKey
	childTools, err := t.chat.toolsForAgent(target, depth)
	if err != nil {
		return agent.ToolResult{}, err
	}
	streamOptions := t.chat.options.StreamOptions
	streamOptions.APIKey = key
	streamOptions.Reasoning = runtime.Reasoning
	if !model.Reasoning {
		streamOptions.Reasoning = ai.ThinkingOff
	}
	// A child is short-lived work: it never warms a prefix of its own.
	childConfig := agent.Config{
		Provider:      provider,
		Model:         model,
		StreamOptions: streamOptions,
		ToolExecution: t.chat.options.ToolExecution,
		CacheWarming:  agent.CacheWarmingOff,
	}
	childContext := agent.Context{
		SystemPrompt: t.chat.childSystemPrompt(child),
		Tools:        childTools,
		Messages:     []ai.Message{ai.NewUserMessage(prompt, time.Now())},
	}
	events := agent.Run(ctx, []ai.Message{ai.NewUserMessage(prompt, time.Now())}, childContext, childConfig)
	var (
		answer   strings.Builder
		progress strings.Builder
		usage    ai.Usage
		failure  error
	)
	for {
		event, ok, nextErr := events.Next(ctx)
		if nextErr != nil {
			failure = nextErr
			break
		}
		if !ok {
			break
		}
		if event.AssistantEvent != nil && event.AssistantEvent.Type == ai.EventTextDelta && event.AssistantEvent.Delta != "" {
			appendBounded(&answer, event.AssistantEvent.Delta, taskResultLimit)
			appendBounded(&progress, event.AssistantEvent.Delta, taskProgressLimit)
			if update != nil {
				update(agent.ToolResult{Content: []ai.Content{ai.NewText(progress.String())}})
			}
		}
		if event.Type == agent.EventAgentEnd && event.Err != nil {
			failure = event.Err
		}
	}
	messages, resultErr := events.Result(ctx)
	if resultErr != nil && failure == nil {
		failure = resultErr
	}
	// The answer is the child's own last spoken turn; the delta buffer holds
	// everything it streamed, which is the fallback for a provider that reports a
	// finished message instead of deltas.
	finalAnswer := ""
	for _, message := range messages {
		assistant, ok := message.(ai.AssistantMessage)
		if !ok {
			continue
		}
		usage.Input += assistant.Usage.Input
		usage.Output += assistant.Usage.Output
		usage.CacheRead += assistant.Usage.CacheRead
		usage.CacheWrite += assistant.Usage.CacheWrite
		spoken := ""
		for _, content := range assistant.Content {
			if text, ok := content.(ai.TextContent); ok {
				spoken += text.Text
			}
		}
		if strings.TrimSpace(spoken) != "" {
			finalAnswer = spoken
		}
	}
	text := strings.TrimSpace(finalAnswer)
	if text == "" {
		text = strings.TrimSpace(answer.String())
	}
	if failure != nil {
		if text == "" {
			return agent.ToolResult{}, fmt.Errorf("task: %s failed: %w", target, failure)
		}
		return agent.ToolResult{
			Content: []ai.Content{ai.NewText(text)},
			Details: map[string]any{"agent": target, "depth": depth, "failed": true},
		}, fmt.Errorf("task: %s failed after answering: %w", target, failure)
	}
	if text == "" {
		return agent.ToolResult{}, fmt.Errorf("task: %s returned no answer", target)
	}
	return agent.ToolResult{
		Content: []ai.Content{ai.NewText(text)},
		Details: map[string]any{"agent": target, "depth": depth, "turns": len(messages)},
		Usage:   &usage,
	}, nil
}

// childSystemPrompt is the subagent's own prompt plus the project instructions and
// skills the session already loaded, so a child works under the same rules as the
// agent that spawned it.
func (c *Chat) childSystemPrompt(child profiles.Profile) string {
	c.mu.Lock()
	suffix := c.options.SystemSuffix
	c.mu.Unlock()
	prompt := strings.TrimSpace(child.SystemPrompt)
	if strings.TrimSpace(suffix) == "" {
		return prompt
	}
	return prompt + "\n\n" + strings.TrimSpace(suffix)
}

// agentRuntime resolves the runtime a child agent runs with: its own configuration
// when it has one, otherwise the model of the agent that spawned it.
func (c *Chat) agentRuntime(agent string, inherited string) (AgentRuntime, error) {
	c.mu.Lock()
	resolve := c.options.AgentRuntime
	c.mu.Unlock()
	if resolve == nil {
		return AgentRuntime{}, errors.New("this session cannot run subagents")
	}
	runtime, err := resolve(agent, inherited)
	if err != nil {
		return AgentRuntime{}, err
	}
	if runtime.Provider == nil {
		return AgentRuntime{}, errors.New("no provider for " + agent)
	}
	return runtime, nil
}

// toolsForAgent builds the tools an agent runs with at a given depth: its own
// profile's tools, the session's MCP tools, and the task tool while that agent has
// depth left to delegate.
func (c *Chat) toolsForAgent(name string, depth int) ([]agent.Tool, error) {
	c.mu.Lock()
	kit, goals, additional, mcpManager := c.kit, c.options.Goals, c.options.AdditionalTools, c.options.MCP
	c.mu.Unlock()
	available, err := profileTools(kit, codingModeFor(name), goals, additional)
	if err != nil {
		return nil, err
	}
	if mcpManager != nil {
		available = append(available, mcpManager.Tools()...)
	}
	if task := c.taskToolFor(name, depth); task != nil {
		available = append(available, task)
	}
	if err := uniqueTools(available); err != nil {
		return nil, err
	}
	return available, nil
}

// taskToolFor returns the delegation tool an agent runs with, or nil when that
// agent cannot delegate. The caller holds c.mu.
func (c *Chat) taskToolForLocked(name string, depth int) agent.Tool {
	if c.options.AgentRuntime == nil || !profiles.CanDelegate(name) {
		return nil
	}
	if depth >= profiles.MaxDepth(name) {
		// The cap is enforced here so the tool is not offered when there is no room,
		// and again when it runs, so a tool built by hand still cannot exceed it.
		return nil
	}
	return taskTool{chat: c, agent: name, depth: depth}
}

// taskToolFor is taskToolForLocked for callers that do not hold the lock.
func (c *Chat) taskToolFor(name string, depth int) agent.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.taskToolForLocked(name, depth)
}

// codingModeFor maps an agent's configured tools onto the coding tool surface.
func codingModeFor(name string) CodingToolsMode {
	switch profiles.ToolsMode(name) {
	case profiles.ToolsReadOnly:
		return CodingToolsReadOnly
	case profiles.ToolsNone:
		return CodingToolsNone
	default:
		return CodingToolsAll
	}
}

// appendBounded appends text, keeping at most limit bytes without splitting a
// rune, and marks that it was cut.
func appendBounded(builder *strings.Builder, text string, limit int) {
	if builder.Len() >= limit {
		return
	}
	remaining := limit - builder.Len()
	if len(text) > remaining {
		text = truncateAtRune(text, remaining) + "…"
	}
	builder.WriteString(text)
}

func truncateAtRune(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// stringArgument reads a string argument, rejecting anything else so a model that
// sends the wrong type gets a clear error instead of an empty prompt.
func stringArgument(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// modelRef is the provider/model reference the model resolver is keyed by.
func modelRef(model ai.Model) string {
	if strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.ID) == "" {
		return ""
	}
	return model.Provider + "/" + model.ID
}
