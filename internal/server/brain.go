package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// MidasBrain answers a message with the Midas agent loop, using the shared tool
// pool plus the bus tools. An agent that can ask another agent, and create one
// when no suitable one exists, is the whole point of the environment, so those
// tools are part of every brain rather than an add-on.
//
// There is deliberately no delegation tool here: an agent in a server environment
// never runs a subagent inside its own run. It uses the bus, so the work stays
// visible to the operator, persisted, and reachable by the other agents.
type MidasBrain struct {
	provider ai.Streamer
	model    ai.Model
	apiKey   string
	system   string
	// Pool supplies the shared MCP tools.
	Pool *Pool

	mu      sync.Mutex
	history map[string][]ai.Message
	creator AgentCreator
}

// SetCreator gives this brain the environment its agent may add agents to.
func (b *MidasBrain) SetCreator(creator AgentCreator) {
	b.mu.Lock()
	b.creator = creator
	b.mu.Unlock()
}

// NewBrain builds a brain for one agent.
func NewBrain(provider ai.Streamer, model ai.Model, apiKey, system string, pool *Pool) *MidasBrain {
	return &MidasBrain{provider: provider, model: model, apiKey: apiKey, system: system, Pool: pool, history: map[string][]ai.Message{}}
}

// Respond runs one turn and returns its text. The conversation it keeps is keyed
// by group when there is one and by peer otherwise, so an agent's memory of a
// conversation is the conversation, not the sender.
func (b *MidasBrain) Respond(ctx context.Context, request Request) (string, error) {
	if b.provider == nil {
		return "", fmt.Errorf("server: %s has no model configured", request.Agent)
	}
	tools := append([]agent.Tool(nil), b.busTools(request)...)
	if b.Pool != nil {
		shared, err := b.Pool.Tools(ctx)
		if err != nil {
			return "", err
		}
		tools = append(tools, shared...)
	}

	key := b.conversationKey(request)
	b.mu.Lock()
	history := append([]ai.Message(nil), b.history[key]...)
	b.mu.Unlock()

	run := agent.Run(ctx, []ai.Message{ai.NewUserMessage(b.prompt(request), time.Now())}, agent.Context{
		SystemPrompt: b.systemPrompt(request),
		Messages:     history,
		Tools:        tools,
	}, agent.Config{
		Provider:      b.provider,
		Model:         b.model,
		StreamOptions: ai.StreamOptions{APIKey: b.apiKey},
		ToolExecution: agent.ToolExecutionParallel,
	})

	// The run's messages arrive on the agent-end event: the message-end event
	// carries one message, not the set, and reading the wrong field is how an
	// agent ends up answering "no answer" with a perfectly good reply behind it.
	produced := []ai.Message{}
	for {
		event, ok, err := run.Next(ctx)
		if err != nil {
			return "", err
		}
		if !ok {
			break
		}
		if event.Type == agent.EventAgentEnd {
			produced = append(produced, event.Messages...)
		}
	}
	if _, err := run.Result(ctx); err != nil {
		return "", err
	}

	b.mu.Lock()
	b.history[key] = append(b.history[key], produced...)
	b.mu.Unlock()

	answer := strings.TrimSpace(lastAssistantText(produced))
	if answer == "" {
		answer = "no answer"
	}
	return answer, nil
}

// systemPrompt tells the agent who it is and how to reach the others, which is
// what makes delegation discoverable rather than a trick.
func (b *MidasBrain) systemPrompt(request Request) string {
	base := strings.TrimSpace(b.system)
	if base == "" {
		base = "You are Midas, a concise coding agent in a multi-agent environment."
	}
	return base + "\n\nYou are " + request.Agent + " in a shared environment. Use ask_agent to get an answer from another agent, tell_agent to hand over work without waiting, and create_agent to add an agent when none fits the work. You do not run subagents yourself. Keep answers short: another agent is reading them."
}

// prompt frames who is speaking, since the same text means different things from
// the user and from another agent.
func (b *MidasBrain) prompt(request Request) string {
	switch {
	case request.From == UserAddress:
		return request.Text
	case request.Kind == KindAsk:
		return "Another agent (" + request.From + ") asks: " + request.Text
	default:
		return "Another agent (" + request.From + ") says: " + request.Text
	}
}

// conversationKey is the thing an agent remembers: a group, or a peer.
func (b *MidasBrain) conversationKey(request Request) string {
	if strings.TrimSpace(request.Group) != "" {
		return "group:" + request.Group
	}
	return "peer:" + request.From
}

// busTools binds the bus to one request, so an agent's delegation carries its own
// address and group.
func (b *MidasBrain) busTools(request Request) []agent.Tool {
	b.mu.Lock()
	creator := b.creator
	b.mu.Unlock()
	return []agent.Tool{
		busTool{
			name:        "ask_agent",
			description: "Ask another agent a question and wait for its answer. Use it to delegate work you cannot do yourself, and name the agent you want.",
			execute: func(ctx context.Context, call ai.ToolCall) (agent.ToolResult, error) {
				to := argumentText(call.Arguments, "agent")
				text := argumentText(call.Arguments, "message")
				if to == "" || text == "" {
					return agent.ToolResult{}, fmt.Errorf("ask_agent needs an agent and a message")
				}
				answer, err := request.Ask(ctx, to, text)
				if err != nil {
					return agent.ToolResult{}, err
				}
				return agent.ToolResult{Content: []ai.Content{ai.NewText(answer)}}, nil
			},
		},
		busTool{
			name:        "create_agent",
			description: "Create another agent in this environment when no existing agent fits the work. The new agent is persisted, so it is still there after a restart, and every agent can reach it with ask_agent or tell_agent.",
			execute: func(ctx context.Context, call ai.ToolCall) (agent.ToolResult, error) {
				if creator == nil {
					return agent.ToolResult{}, fmt.Errorf("create_agent is unavailable in this environment")
				}
				entry := AgentConfig{
					Address: argumentText(call.Arguments, "address"),
					Role:    argumentText(call.Arguments, "role"),
					Model:   argumentText(call.Arguments, "model"),
					Prompt:  argumentText(call.Arguments, "prompt"),
				}
				status, err := creator.CreateAgent(ctx, entry)
				if err != nil {
					return agent.ToolResult{}, err
				}
				return agent.ToolResult{Content: []ai.Content{ai.NewText(fmt.Sprintf(
					"created %s (role %s). Reach it with ask_agent or tell_agent.", status.Address, status.Role))}}, nil
			},
		},
		busTool{
			name:        "tell_agent",
			description: "Send another agent a message without waiting for an answer, for progress notes and handovers.",
			execute: func(_ context.Context, call ai.ToolCall) (agent.ToolResult, error) {
				to := argumentText(call.Arguments, "agent")
				text := argumentText(call.Arguments, "message")
				if to == "" || text == "" {
					return agent.ToolResult{}, fmt.Errorf("tell_agent needs an agent and a message")
				}
				if err := request.Tell(to, text); err != nil {
					return agent.ToolResult{}, err
				}
				return agent.ToolResult{Content: []ai.Content{ai.NewText("delivered")}}, nil
			},
		},
	}
}

// busTool is one agent-facing tool backed by the bus.
type busTool struct {
	name        string
	description string
	execute     func(ctx context.Context, call ai.ToolCall) (agent.ToolResult, error)
}

func (t busTool) Definition() ai.Tool {
	return ai.Tool{
		Name:        t.name,
		Description: t.description,
		Parameters:  []byte(`{"type":"object","properties":{"agent":{"type":"string","description":"the agent's address"},"message":{"type":"string","description":"what to say"}},"required":["agent","message"]}`),
	}
}

func (t busTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	return t.execute(ctx, call)
}

func argumentText(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return strings.TrimSpace(value)
}

// lastAssistantText returns the text of the newest assistant message.
func lastAssistantText(messages []ai.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		var content []ai.Content
		switch message := messages[index].(type) {
		case ai.AssistantMessage:
			content = message.Content
		case *ai.AssistantMessage:
			if message != nil {
				content = message.Content
			}
		default:
			continue
		}
		text := strings.Builder{}
		for _, part := range content {
			switch value := part.(type) {
			case ai.TextContent:
				text.WriteString(value.Text)
			case *ai.TextContent:
				if value != nil {
					text.WriteString(value.Text)
				}
			}
		}
		if strings.TrimSpace(text.String()) != "" {
			return text.String()
		}
	}
	return ""
}
