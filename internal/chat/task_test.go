package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/internal/profiles"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// scriptedProvider answers a run with the next scripted reply. It exists so a
// delegation test can say "the first call asks for a subagent, the next answers".
type scriptedProvider struct {
	replies []scriptedReply
	calls   int
}

type scriptedReply struct {
	text  string
	tools []ai.ToolCall
}

func (p *scriptedProvider) ID() string { return "scripted" }

func (p *scriptedProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }

func (p *scriptedProvider) Stream(_ context.Context, _ ai.Model, request ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	index := p.calls
	p.calls++
	var reply scriptedReply
	if index < len(p.replies) {
		reply = p.replies[index]
	} else {
		reply = scriptedReply{text: "done"}
	}
	content := make([]ai.Content, 0, len(reply.tools)+1)
	if reply.text != "" {
		content = append(content, ai.NewText(reply.text))
	}
	for _, call := range reply.tools {
		content = append(content, call)
	}
	stream := ai.NewAssistantStream()
	partial := ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stop := ai.StopComplete
	if len(reply.tools) > 0 {
		stop = ai.StopToolUse
	}
	message := ai.AssistantMessage{Role: ai.RoleAssistant, Content: content, StopReason: stop, Usage: ai.Usage{Input: 5, Output: 5}}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: stop, Message: &message})
	_ = request
	_ = options
	return stream, nil
}

// delegationChat builds a chat whose provider script drives a delegation run and
// whose resolver serves every agent from the given model.
func delegationChat(t *testing.T, scripted *scriptedProvider, replies ...scriptedReply) *Chat {
	t.Helper()
	scripted.replies = replies
	var provider ai.Provider = scripted
	chat, err := New(Options{
		Root: t.TempDir(), SessionID: "ses_delegate",
		Provider: provider, Model: ai.Model{ID: "test", Provider: "scripted", Reasoning: true},
		Agent: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetAgentRuntime(func(name, inherited string) (AgentRuntime, error) {
		return AgentRuntime{
			Provider: provider, Model: ai.Model{ID: "test", Provider: "scripted", Reasoning: true},
			Reasoning: ai.ThinkingMedium,
		}, nil
	})
	return chat
}

// TestTaskToolRunsASubagentAndReturnsItsAnswer is the happy path: the entry agent
// delegates, the child runs on its own, and its answer comes back as the tool
// result.
func TestTaskToolRunsASubagentAndReturnsItsAnswer(t *testing.T) {
	provider := &scriptedProvider{}
	chat := delegationChat(t, provider,
		scriptedReply{tools: []ai.ToolCall{ai.NewToolCall("1", "task", map[string]any{
			"agent": "advisor", "prompt": "Advise on the change.",
		})}},
		scriptedReply{text: "Advisor says: keep it small."},
	)
	_, err := chat.Prompt(context.Background(), "ask the advisor")
	if err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, chat)
	transcript := chat.Messages()
	if !strings.Contains(toolResultText(transcript), "Advisor says: keep it small.") {
		t.Fatalf("the child answer never reached the caller: %s", toolResultText(transcript))
	}
}

// waitForIdle blocks until the run releases the chat.
func waitForIdle(t *testing.T, chat *Chat) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !chat.Busy() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run never finished")
}

// toolResultText is the text of every tool result, which is where a child's answer
// lands in the caller's transcript.
func toolResultText(messages []ai.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		result, ok := message.(ai.ToolResultMessage)
		if !ok {
			continue
		}
		for _, content := range result.Content {
			if text, ok := content.(ai.TextContent); ok {
				builder.WriteString(text.Text)
			}
		}
	}
	return builder.String()
}

// TestTaskToolRefusesTargetsThatAreNotSubagents covers the mode rules: a primary
// agent is the session itself and a utility agent is Midas' helper, so neither can
// be delegated to.
func TestTaskToolRefusesTargetsThatAreNotSubagents(t *testing.T) {
	chat := delegationChat(t, &scriptedProvider{})
	tool := chat.taskToolFor("main", 0)
	if tool == nil {
		t.Fatal("the entry agent has no task tool")
	}
	entry, ok := tool.(taskTool)
	if !ok {
		t.Fatalf("task tool type = %T", tool)
	}
	for name, want := range map[string]string{
		"main":    "not a subagent",
		"title":   "not a subagent",
		"summary": "not a subagent",
		"ghost":   "unknown agent",
		"main ":   "not a subagent",
	} {
		_, err := entry.Execute(context.Background(), ai.NewToolCall("1", "task", map[string]any{
			"agent": name, "prompt": "do something",
		}), nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("task %q = %v, want %q", name, err, want)
		}
	}
	// A subagent cannot delegate to itself.
	child := taskTool{chat: chat, agent: "advisor", depth: 1}
	if _, err := child.Execute(context.Background(), ai.NewToolCall("1", "task", map[string]any{
		"agent": "advisor", "prompt": "again",
	}), nil); err == nil || !strings.Contains(err.Error(), "cannot run itself") {
		t.Fatalf("self delegation = %v", err)
	}
	// Arguments are required and must be strings.
	for _, arguments := range []map[string]any{
		{"prompt": "no agent"},
		{"agent": "advisor"},
		{"agent": "advisor", "prompt": 3},
	} {
		if _, err := child.Execute(context.Background(), ai.NewToolCall("1", "task", arguments), nil); err == nil {
			t.Fatalf("task %v was accepted", arguments)
		}
	}
}

// TestMaxDepthStopsTheChain is the enforcement the setting exists for: an agent at
// its cap cannot spawn, and the tool is not even offered.
func TestMaxDepthStopsTheChain(t *testing.T) {
	chat := delegationChat(t, &scriptedProvider{})
	if chat.taskToolFor("main", 0) == nil {
		t.Fatal("the first level has no task tool")
	}
	if chat.taskToolFor("main", 1) == nil {
		t.Fatal("the second level has no task tool")
	}
	// main's default cap is two levels, so the third has none.
	if tool := chat.taskToolFor("main", 2); tool != nil {
		t.Fatalf("the third level was offered a task tool: %T", tool)
	}
	// A tool built by hand still refuses, so the cap holds even if the list was
	// wrong.
	hand := taskTool{chat: chat, agent: "main", depth: 2}
	_, err := hand.Execute(context.Background(), ai.NewToolCall("1", "task", map[string]any{
		"agent": "advisor", "prompt": "keep going",
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("depth cap = %v", err)
	}

	// An agent may set its own, shallower cap.
	if err := profiles.LoadCustom(map[string]json.RawMessage{
		"narrow": json.RawMessage(`{"mode":"primary","maxDepth":1,"prompt":"narrow"}`),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profiles.LoadCustom(nil) })
	if chat.taskToolFor("narrow", 0) == nil {
		t.Fatal("a depth-1 agent has no first level")
	}
	if tool := chat.taskToolFor("narrow", 1); tool != nil {
		t.Fatal("a depth-1 agent spawned a second level")
	}
}

// TestDelegationIsUnavailableToReadOnlyAgents: reconnaissance never grows into a
// tree of its own, so a read-only agent has no task tool.
func TestDelegationIsUnavailableToReadOnlyAgents(t *testing.T) {
	chat := delegationChat(t, &scriptedProvider{})
	for _, name := range []string{"advisor", "explore"} {
		if tool := chat.taskToolFor(name, 0); tool != nil {
			t.Fatalf("%s was offered delegation: %T", name, tool)
		}
	}
	// A session that cannot resolve agent runtimes has no delegation at all.
	plain, err := New(Options{
		Root: t.TempDir(), Provider: &scriptedProvider{}, Model: ai.Model{ID: "test", Provider: "scripted"},
		Agent: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool := plain.taskToolFor("main", 0); tool != nil {
		t.Fatalf("a chat without an agent runtime was offered delegation: %T", tool)
	}
}

// TestChildRunsWithItsOwnToolsAndReportedDepth: the child's tool list is its own
// profile's, and it carries the next depth so its own children are capped too.
func TestChildRunsWithItsOwnToolsAndReportedDepth(t *testing.T) {
	chat := delegationChat(t, &scriptedProvider{})
	tools, err := chat.toolsForAgent("advisor", 1)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Definition().Name] = true
	}
	if names["write"] || names["edit"] || names["bash"] {
		t.Fatalf("a read-only agent was given write tools: %v", names)
	}
	if !names["read"] || !names["grep"] {
		t.Fatalf("a read-only agent lost its reconnaissance tools: %v", names)
	}
	if names["task"] {
		t.Fatalf("a read-only agent was given delegation: %v", names)
	}
	// An agent with room to delegate gets the tool at the next depth.
	tools, err = chat.toolsForAgent("main", 1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if entry, ok := tool.(taskTool); ok {
			if entry.depth != 1 {
				t.Fatalf("nested task depth = %d, want 1", entry.depth)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("a delegating agent has no task tool")
	}
}

// TestFailedChildSurfacesItsError: a child that cannot run ends the tool call with
// an error the caller can read, instead of a silent empty answer.
func TestFailedChildSurfacesItsError(t *testing.T) {
	provider := &scriptedProvider{}
	chat := delegationChat(t, provider)
	chat.SetAgentRuntime(func(string, string) (AgentRuntime, error) {
		return AgentRuntime{}, errors.New("no credentials for that provider")
	})
	entry := taskTool{chat: chat, agent: "main", depth: 0}
	_, err := entry.Execute(context.Background(), ai.NewToolCall("1", "task", map[string]any{
		"agent": "advisor", "prompt": "advise",
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("failed child = %v", err)
	}
}

// TestTaskResultIsBounded: a chatty child cannot flood the caller's context.
func TestTaskResultIsBounded(t *testing.T) {
	chat := delegationChat(t, &scriptedProvider{})
	entry := taskTool{chat: chat, agent: "main", depth: 0}
	if tool := entry; tool.depth != 0 {
		t.Fatal("unreachable")
	}
	var builder strings.Builder
	appendBounded(&builder, strings.Repeat("x", taskResultLimit*2), taskResultLimit)
	if builder.Len() > taskResultLimit+4 {
		t.Fatalf("bounded builder grew to %d", builder.Len())
	}
	if !strings.HasSuffix(builder.String(), "…") {
		t.Fatal("truncation was not marked")
	}
	_ = fmt.Sprintf("%v", chat)
}
