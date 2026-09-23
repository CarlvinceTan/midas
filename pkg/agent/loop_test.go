package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

type scriptedProvider struct {
	mu        sync.Mutex
	responses []ai.AssistantMessage
	contexts  []ai.Context
}

type blockingSteerProvider struct {
	mu       sync.Mutex
	contexts []ai.Context
	started  chan int
	release  []chan struct{}
}

func (p *blockingSteerProvider) Stream(ctx context.Context, _ ai.Model, input ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	p.mu.Lock()
	index := len(p.contexts)
	p.contexts = append(p.contexts, input)
	p.mu.Unlock()
	p.started <- index
	select {
	case <-p.release[index]:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	message := assistant(ai.StopComplete, ai.NewText(fmt.Sprintf("reply %d", index+1)))
	stream := ai.NewAssistantStream()
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Message: &message})
	return stream, nil
}

func (p *scriptedProvider) ID() string { return "scripted" }

func (p *scriptedProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }

func (p *scriptedProvider) Stream(_ context.Context, _ ai.Model, input ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.contexts = append(p.contexts, input)
	if len(p.responses) == 0 {
		return nil, errors.New("no scripted response")
	}
	message := p.responses[0]
	p.responses = p.responses[1:]
	stream := ai.NewAssistantStream()
	partial := message
	partial.Content = nil
	partial.StopReason = ai.StopPending
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: message.StopReason, Message: &message})
	return stream, nil
}

type testTool struct {
	name       string
	sequential bool
	execute    func(context.Context, ai.ToolCall, ToolUpdate) (ToolResult, error)
}

func (t testTool) Definition() ai.Tool { return ai.Tool{Name: t.name, Description: t.name} }
func (t testTool) Sequential() bool    { return t.sequential }
func (t testTool) Execute(ctx context.Context, call ai.ToolCall, update ToolUpdate) (ToolResult, error) {
	return t.execute(ctx, call, update)
}

func TestRunExecutesToolAndContinues(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{responses: []ai.AssistantMessage{
		assistant(ai.StopToolUse, ai.NewToolCall("call-1", "echo", map[string]any{"text": "hello"})),
		assistant(ai.StopComplete, ai.NewText("done")),
	}}
	tool := testTool{name: "echo", execute: func(_ context.Context, call ai.ToolCall, update ToolUpdate) (ToolResult, error) {
		update(ToolResult{Content: []ai.Content{ai.NewText("working")}})
		return ToolResult{Content: []ai.Content{ai.NewText(call.Arguments["text"].(string))}}, nil
	}}
	prompt := ai.NewUserMessage("go", time.Unix(1, 0))
	stream := Run(context.Background(), []ai.Message{prompt}, Context{Tools: []Tool{tool}}, Config{
		Provider: provider,
		Model:    ai.Model{ID: "test", Provider: "scripted"},
	})

	events := collectEvents(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 {
		t.Fatalf("new messages = %d, want prompt + two assistant messages + tool result", len(result))
	}
	if len(provider.contexts) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.contexts))
	}
	secondContext := provider.contexts[1].Messages
	if len(secondContext) != 3 {
		t.Fatalf("second provider context has %d messages, want 3", len(secondContext))
	}

	wantOrder := []EventType{
		EventAgentStart, EventTurnStart, EventMessageStart, EventMessageEnd,
		EventMessageStart, EventMessageEnd,
		EventToolExecutionStart, EventToolExecutionUpdate, EventToolExecutionEnd,
		EventMessageStart, EventMessageEnd, EventTurnEnd, EventTurnStart,
		EventMessageStart, EventMessageEnd, EventTurnEnd, EventAgentEnd,
	}
	if len(events) != len(wantOrder) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(wantOrder), eventTypes(events))
	}
	for index, want := range wantOrder {
		if events[index].Type != want {
			t.Fatalf("event %d = %s, want %s; all: %#v", index, events[index].Type, want, eventTypes(events))
		}
	}
}

func TestRunProcessesSteersAtStepBoundary(t *testing.T) {
	t.Parallel()

	provider := &blockingSteerProvider{
		started: make(chan int, 2),
		release: []chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	steering := NewSteeringQueue()
	prompt := ai.NewUserMessage("first", time.Unix(1, 0))
	stream := Run(context.Background(), []ai.Message{prompt}, Context{}, Config{
		Provider: provider,
		Model:    ai.Model{ID: "test", Provider: "steering"},
		Steering: steering,
	})

	if call := <-provider.started; call != 0 {
		t.Fatalf("first provider call = %d", call)
	}
	steer := ai.NewUserMessage("change direction", time.Unix(2, 0))
	if !steering.Push(steer) {
		t.Fatal("active steering queue rejected input")
	}
	close(provider.release[0])
	if call := <-provider.started; call != 1 {
		t.Fatalf("second provider call = %d", call)
	}

	provider.mu.Lock()
	secondContext := append([]ai.Message(nil), provider.contexts[1].Messages...)
	provider.mu.Unlock()
	if len(secondContext) != 3 {
		t.Fatalf("steered context length = %d, want prompt + assistant + steer", len(secondContext))
	}
	steered, ok := secondContext[2].(ai.UserMessage)
	if !ok || testUserText(steered) != "change direction" {
		t.Fatalf("steered context message = %#v", secondContext[2])
	}

	close(provider.release[1])
	events := collectEvents(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 {
		t.Fatalf("steered result = %#v", result)
	}
	if steering.Push(ai.NewUserMessage("too late", time.Now())) {
		t.Fatal("completed run accepted a late steer")
	}

	want := []EventType{EventMessageStart, EventMessageEnd, EventTurnStart}
	found := false
	for index := 0; index+len(want) <= len(events); index++ {
		if events[index].Type == want[0] && events[index+1].Type == want[1] && events[index+2].Type == want[2] {
			if message, ok := events[index].Message.(ai.UserMessage); ok && testUserText(message) == "change direction" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("steer boundary events missing: %#v", eventTypes(events))
	}
}

func testUserText(message ai.UserMessage) string {
	text, _ := message.Content.Text()
	return text
}

func TestRunDoesNotExecuteTruncatedToolCall(t *testing.T) {
	t.Parallel()

	called := false
	tool := testTool{name: "dangerous", execute: func(context.Context, ai.ToolCall, ToolUpdate) (ToolResult, error) {
		called = true
		return ToolResult{}, nil
	}}
	provider := &scriptedProvider{responses: []ai.AssistantMessage{
		assistant(ai.StopLength, ai.NewToolCall("call-1", "dangerous", map[string]any{"path": "/"})),
		assistant(ai.StopComplete, ai.NewText("recovered")),
	}}
	stream := Run(context.Background(), nil, Context{Tools: []Tool{tool}}, Config{Provider: provider})
	collectEvents(t, stream)

	if called {
		t.Fatal("truncated tool call executed")
	}
	if len(provider.contexts) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.contexts))
	}
	toolResult, ok := provider.contexts[1].Messages[1].(ai.ToolResultMessage)
	if !ok || !toolResult.IsError {
		t.Fatalf("tool result = %#v", provider.contexts[1].Messages[1])
	}
}

func TestRunNormalizesProviderSetupError(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{}
	stream := Run(context.Background(), nil, Context{}, Config{Provider: provider, Model: ai.Model{ID: "m"}})
	events := collectEvents(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("result = %#v", result)
	}
	message, ok := result[0].(ai.AssistantMessage)
	if !ok || message.StopReason != ai.StopError || message.ErrorMessage != "no scripted response" {
		t.Fatalf("message = %#v", result[0])
	}
	if got := eventTypes(events); got[len(got)-1] != EventAgentEnd {
		t.Fatalf("last event = %s", got[len(got)-1])
	}
}

func TestParallelToolResultsRemainInSourceOrder(t *testing.T) {
	t.Parallel()

	releaseSlow := make(chan struct{})
	provider := &scriptedProvider{responses: []ai.AssistantMessage{
		assistant(ai.StopToolUse,
			ai.NewToolCall("slow", "slow", nil),
			ai.NewToolCall("fast", "fast", nil),
		),
		assistant(ai.StopComplete, ai.NewText("done")),
	}}
	slow := testTool{name: "slow", execute: func(context.Context, ai.ToolCall, ToolUpdate) (ToolResult, error) {
		<-releaseSlow
		return ToolResult{Content: []ai.Content{ai.NewText("slow")}}, nil
	}}
	fast := testTool{name: "fast", execute: func(context.Context, ai.ToolCall, ToolUpdate) (ToolResult, error) {
		close(releaseSlow)
		return ToolResult{Content: []ai.Content{ai.NewText("fast")}}, nil
	}}
	stream := Run(context.Background(), nil, Context{Tools: []Tool{slow, fast}}, Config{Provider: provider})
	collectEvents(t, stream)

	messages := provider.contexts[1].Messages
	first := messages[1].(ai.ToolResultMessage)
	second := messages[2].(ai.ToolResultMessage)
	if first.ToolCallID != "slow" || second.ToolCallID != "fast" {
		t.Fatalf("tool result order = %s, %s", first.ToolCallID, second.ToolCallID)
	}
}

func assistant(reason ai.StopReason, content ...ai.Content) ai.AssistantMessage {
	return ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    content,
		Provider:   "scripted",
		Model:      "test",
		StopReason: reason,
		Timestamp:  1,
	}
}

func collectEvents(t *testing.T, stream *Stream) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []Event
	for {
		event, ok, err := stream.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return events
		}
		events = append(events, event)
	}
}

func eventTypes(events []Event) []EventType {
	types := make([]EventType, len(events))
	for index, event := range events {
		types[index] = event.Type
	}
	return types
}
