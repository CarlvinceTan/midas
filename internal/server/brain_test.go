package server

import (
	"context"
	"strings"
	"testing"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// scriptedProvider streams a fixed reply, so the brain can be tested without a
// model. It exists because a brain reading the wrong event field answers "no
// answer" while a perfectly good reply goes by.
type scriptedProvider struct {
	reply string
}

func (scriptedProvider) ID() string                                 { return "scripted" }
func (scriptedProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }

func (p scriptedProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	stream := ai.NewAssistantStream()
	partial := ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	accumulated := ""
	for _, word := range strings.SplitAfter(p.reply, " ") {
		if word == "" {
			continue
		}
		accumulated += word
		partial := ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(accumulated)}, StopReason: ai.StopPending}
		stream.Push(ai.AssistantEvent{Type: ai.EventTextDelta, Delta: word, Partial: &partial})
	}
	message := ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(p.reply)}, StopReason: ai.StopComplete}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	return stream, nil
}

func TestBrainReturnsTheModelsAnswerAndRemembersTheTurn(t *testing.T) {
	brain := NewBrain(scriptedProvider{reply: "the answer is 51"}, ai.Model{ID: "test", Provider: "scripted"}, "", "", nil)
	answer, err := brain.Respond(context.Background(), Request{Agent: "worker", From: UserAddress, Text: "what is 17 times 3?"})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "the answer is 51" {
		t.Fatalf("answer = %q", answer)
	}
	// The conversation keeps both halves, keyed by the group when there is one.
	brain.mu.Lock()
	history := append([]ai.Message(nil), brain.history["peer:"+UserAddress]...)
	brain.mu.Unlock()
	if len(history) < 2 {
		t.Fatalf("history = %#v", history)
	}
	if _, ok := history[0].(ai.UserMessage); !ok {
		t.Fatalf("history does not start with the prompt: %#v", history[0])
	}
	// A different conversation does not see this one.
	brain.mu.Lock()
	_, shared := brain.history["group:release"]
	brain.mu.Unlock()
	if shared {
		t.Fatal("a group conversation appeared out of nowhere")
	}
	if _, err := brain.Respond(context.Background(), Request{Agent: "worker", Group: "release", Text: "and now?"}); err != nil {
		t.Fatal(err)
	}
	brain.mu.Lock()
	_, grouped := brain.history["group:release"]
	brain.mu.Unlock()
	if !grouped {
		t.Fatal("a grouped turn was not remembered under its group")
	}
}

func TestBrainWithoutAModelSaysSo(t *testing.T) {
	brain := NewBrain(nil, ai.Model{}, "", "", nil)
	if _, err := brain.Respond(context.Background(), Request{Agent: "worker"}); err == nil {
		t.Fatal("a brain with no provider answered anyway")
	}
}

// TestBrainToolsCarryTheAgentIdentity checks that the bus tools an agent has are
// bound to the agent that is running, not to whoever asked last.
func TestBrainToolsCarryTheAgentIdentity(t *testing.T) {
	brain := NewBrain(nil, ai.Model{}, "", "", nil)
	asked := ""
	tools := brain.busTools(Request{
		Agent: "orchestrator", Group: "release",
		Ask: func(_ context.Context, to, text string) (string, error) {
			asked = to + ": " + text
			return "51", nil
		},
		Tell: func(string, string) error { return nil },
	})
	// ask_agent, tell_agent, and create_agent: a server agent talks to agents and
	// adds them, and never runs a subagent of its own.
	if len(tools) != 3 {
		t.Fatalf("tools = %#v", tools)
	}
	names := map[string]agent.Tool{}
	for _, tool := range tools {
		names[tool.Definition().Name] = tool
	}
	if _, ok := names["ask_agent"]; !ok {
		t.Fatal("ask_agent is missing: an agent cannot delegate without it")
	}
	result, err := names["ask_agent"].Execute(context.Background(), ai.ToolCall{
		Name: "ask_agent", Arguments: map[string]any{"agent": "worker", "message": "17 times 3?"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "worker: 17 times 3?" {
		t.Fatalf("ask carried %q", asked)
	}
	if text, _ := result.Content[0].(ai.TextContent); text.Text != "51" {
		t.Fatalf("result = %#v", result.Content)
	}
	// A tool call missing its arguments is refused rather than sent as empty.
	if _, err := names["tell_agent"].Execute(context.Background(), ai.ToolCall{
		Name: "tell_agent", Arguments: map[string]any{"agent": "worker"},
	}, nil); err == nil {
		t.Fatal("tell_agent accepted a missing message")
	}
}
