package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

type fakeChatRun struct {
	*ai.EventStream[agent.Event, []ai.Message]
}

func newFakeChatRun() *fakeChatRun {
	return &fakeChatRun{EventStream: ai.NewEventStream(func(event agent.Event) ([]ai.Message, bool) {
		return event.Messages, event.Type == agent.EventAgentEnd
	})}
}

func (r *fakeChatRun) Cancel(error) {}

type fakeChatBackend struct {
	mu      sync.Mutex
	prompts []string
	steers  []string
	runs    []*fakeChatRun
	aborts  int
}

// fakeUndoBackend is a fakeChatBackend that can also undo the last prompt.
type fakeUndoBackend struct {
	*fakeChatBackend
	mu       sync.Mutex
	prompt   string
	messages []ai.Message
	undone   bool
}

func (b *fakeUndoBackend) RevertLastPrompt() (string, []ai.Message, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.undone {
		return "", nil, false, nil
	}
	b.undone = false
	return b.prompt, b.messages, true, nil
}

type failingChatBackend struct{ err error }

type observingSteerBackend struct {
	*fakeChatBackend
	beforeAdmission func(string)
	err             error
}

func (b failingChatBackend) Start(context.Context, string) (ChatRun, error) { return nil, b.err }
func (failingChatBackend) Abort(error) bool                                 { return false }

func (b *observingSteerBackend) Steer(ctx context.Context, prompt string) error {
	if b.beforeAdmission != nil {
		b.beforeAdmission(prompt)
	}
	if b.err != nil {
		return b.err
	}
	return b.fakeChatBackend.Steer(ctx, prompt)
}

func (b *fakeChatBackend) Start(_ context.Context, prompt string) (ChatRun, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	run := newFakeChatRun()
	b.prompts = append(b.prompts, prompt)
	b.runs = append(b.runs, run)
	return run, nil
}

func (b *fakeChatBackend) Abort(error) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.runs) == 0 {
		return false
	}
	b.aborts++
	b.runs[len(b.runs)-1].Push(agent.Event{Type: agent.EventAgentEnd})
	return true
}

func (b *fakeChatBackend) Steer(_ context.Context, prompt string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.steers = append(b.steers, prompt)
	return nil
}

func (b *fakeChatBackend) snapshot() ([]string, []*fakeChatRun) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.prompts...), append([]*fakeChatRun(nil), b.runs...)
}

func (b *fakeChatBackend) steerSnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.steers...)
}

func TestChatStreamsTranscriptAndTools(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Model: "test-model", SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("inspect the repo")
	_, runs := backend.snapshot()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	runs[0].Push(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{Type: ai.EventTextDelta, Delta: "Working"}})
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "1", ToolName: "read", Arguments: map[string]any{"path": "README.md"}})
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: "1", ToolName: "read"})
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	for _, want := range []string{"inspect the repo", "+ Worked", "Working", "Test Model", "ses_test"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("render missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Read README.md") {
		t.Fatalf("collapsed run exposed tool details:\n%s", plain)
	}
	chat.HandleInput("\x0f") // ctrl+o mirrors the old Midas expand-all control.
	expanded := stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(expanded, "- Worked") || !strings.Contains(expanded, "✓ Read README.md") {
		t.Fatalf("expanded transcript missing tool detail:\n%s", expanded)
	}
}

func TestTranscriptRestoresNestedMultiToolExpansionAndKeepsState(t *testing.T) {
	messages := []ai.Message{
		ai.NewUserMessage("inspect and test", time.UnixMilli(1_000)),
		ai.AssistantMessage{
			Role: ai.RoleAssistant, Timestamp: 1_100,
			Content: []ai.Content{
				ai.NewThinking("I should inspect first"),
				ai.NewToolCall("read-1", "read", map[string]any{"path": "README.md"}),
				ai.NewToolCall("shell-1", "shell", map[string]any{"command": "go test ./..."}),
			},
		},
		ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "read-1", ToolName: "read", Content: []ai.Content{ai.NewText("line one\nline two")}, Timestamp: 2_000},
		ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "shell-1", ToolName: "shell", Content: []ai.Content{ai.NewText("ok")}, Timestamp: 3_000},
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("Everything passes.")}, Timestamp: 4_000},
	}
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: messages})
	if err != nil {
		t.Fatal(err)
	}

	plain := stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if !strings.Contains(plain, "+ Worked for 3s") || !strings.Contains(plain, "Everything passes.") {
		t.Fatalf("collapsed restored run:\n%s", plain)
	}
	for _, hidden := range []string{"Read 1 file", "Read README.md", "line one", "Thought"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("collapsed run exposed %q:\n%s", hidden, plain)
		}
	}

	clickTranscriptTarget(t, chat, transcriptRunTarget, "", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if !strings.Contains(plain, "+ Read 1 file, ran 1 command") {
		t.Fatalf("expanded run did not retain the multi-tool activity chain:\n%s", plain)
	}
	// Reasoning and tools are consecutive, so the chain summarizes all three
	// activities while staying collapsed until its own header is clicked.
	if strings.Contains(plain, "Read README.md") || strings.Contains(plain, "line one") {
		t.Fatalf("collapsed activity chain exposed child rows:\n%s", plain)
	}

	clickTranscriptTarget(t, chat, transcriptChainTarget, "", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	for _, want := range []string{"Thought", "✓ Read README.md", "✓ Ran `go test ./...`"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expanded activity chain missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "line one") {
		t.Fatalf("collapsed tool row exposed its result:\n%s", plain)
	}

	clickTranscriptTarget(t, chat, transcriptDetailTarget, ":1", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if !strings.Contains(plain, "line one") || !strings.Contains(plain, "line two") {
		t.Fatalf("expanded tool result missing:\n%s", plain)
	}

	// A later result update must not throw away the user's expansion choice.
	chat.mu.Lock()
	chat.entries[1].tools[0].result += "\nline three"
	chat.mu.Unlock()
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if !strings.Contains(plain, "line three") {
		t.Fatalf("tool expansion state was lost after an update:\n%s", plain)
	}

	clickTranscriptTarget(t, chat, transcriptDetailTarget, ":1", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if strings.Contains(plain, "line one") {
		t.Fatalf("tool detail did not minimise:\n%s", plain)
	}
	clickTranscriptTarget(t, chat, transcriptChainTarget, "", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if strings.Contains(plain, "Read README.md") {
		t.Fatalf("activity chain did not minimise:\n%s", plain)
	}
	clickTranscriptTarget(t, chat, transcriptRunTarget, "", 72)
	plain = stripANSI(strings.Join(chat.renderTranscript(72), "\n"))
	if strings.Contains(plain, "Read 1 file") || !strings.Contains(plain, "+ Worked for 3s") {
		t.Fatalf("run did not minimise to its Worked header:\n%s", plain)
	}
}

func clickQueuedRow(t *testing.T, chat *Chat, index, width int) {
	t.Helper()
	chat.renderPending(width)
	chat.mu.Lock()
	targets := append([]pendingTarget(nil), chat.pendingTargets...)
	chat.mu.Unlock()
	for _, target := range targets {
		if target.index != index {
			continue
		}
		result := chat.handlePendingMouse(MouseEvent{Type: MouseClick, Button: MouseLeft, Y: target.start, Width: width})
		if result == nil || !result.Handled {
			t.Fatalf("queued row click was not handled: %#v", target)
		}
		return
	}
	t.Fatalf("no queued row target %d in %#v", index, targets)
}

func TestChatClickingAQueuedRowPullsItIntoTheEditor(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test",
		InitialQueue: []string{"first queued", "second queued"}, QueueHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clickQueuedRow(t, chat, 0, 60)
	if got := chat.Editor().Text(); got != "first queued" {
		t.Fatalf("editor = %q", got)
	}
	chat.mu.Lock()
	queue, edit := append([]string(nil), chat.queue...), chat.queueEdit
	chat.mu.Unlock()
	if edit != 0 || len(queue) != 1 || queue[0] != "second queued" {
		t.Fatalf("queue = %#v edit = %d", queue, edit)
	}

	// Enter returns the edited text to its original slot and empties the editor.
	chat.submit("first queued edited")
	if got := chat.Editor().Text(); got != "" {
		t.Fatalf("editor after edit = %q", got)
	}
	chat.mu.Lock()
	queue, edit = append([]string(nil), chat.queue...), chat.queueEdit
	chat.mu.Unlock()
	if edit != -1 || len(queue) != 2 || queue[0] != "first queued edited" || queue[1] != "second queued" {
		t.Fatalf("queue after edit = %#v edit = %d", queue, edit)
	}
}

func TestChatClearingAnEditedQueuedRowDeletesIt(t *testing.T) {
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test",
		InitialQueue: []string{"first queued", "second queued"}, QueueHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clickQueuedRow(t, chat, 1, 60)
	chat.submit("")
	chat.mu.Lock()
	queue, edit := append([]string(nil), chat.queue...), chat.queueEdit
	chat.mu.Unlock()
	if edit != -1 || len(queue) != 1 || queue[0] != "first queued" {
		t.Fatalf("queue after deleting = %#v edit = %d", queue, edit)
	}
	if got := chat.Editor().Text(); got != "" {
		t.Fatalf("editor after delete = %q", got)
	}

	// The editor is back to ordinary behavior: with a run active, Enter queues
	// a new message instead of editing anything.
	backend := &fakeChatBackend{}
	busy, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		InitialQueue: []string{"first queued", "second queued"}, QueueHeld: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	clickQueuedRow(t, busy, 1, 60)
	busy.submit("")
	busy.start("running", "running")
	busy.submit("a fresh prompt")
	busy.mu.Lock()
	queue = append([]string(nil), busy.queue...)
	busy.mu.Unlock()
	if len(queue) != 2 || queue[1] != "a fresh prompt" {
		t.Fatalf("queue after fresh submit = %#v", queue)
	}
}

func clickTranscriptTarget(t *testing.T, chat *Chat, kind transcriptTargetKind, keyContains string, width int) {
	t.Helper()
	chat.renderTranscript(width)
	chat.mu.Lock()
	targets := append([]transcriptTarget(nil), chat.transcriptTargets...)
	chat.mu.Unlock()
	for _, target := range targets {
		if target.kind != kind || !strings.Contains(target.key, keyContains) {
			continue
		}
		y := len(chat.renderStartup(width)) + target.start
		result := chat.handleTranscriptMouse(MouseEvent{Type: MouseClick, Button: MouseLeft, Y: y, Width: width})
		if result == nil || !result.Handled {
			t.Fatalf("target click was not handled: %#v", target)
		}
		return
	}
	t.Fatalf("no transcript target kind=%d key containing %q in %#v", kind, keyContains, targets)
}

func TestChatReturnsLastAssistantText(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, InitialMessages: []ai.Message{
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("first")}},
		ai.NewUserMessage("question", time.Unix(1, 0)),
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("latest")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := chat.LastAssistantText(); got != "latest" {
		t.Fatalf("last assistant text = %q", got)
	}
}

func TestChatShowsModelSelectionState(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, SessionID: "ses_test"})
	if err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(plain, "Not set • off") || strings.Contains(plain, "No Provider") || strings.Contains(plain, "No provider selected.") {
		t.Fatalf("render missing selection state:\n%s", plain)
	}
}

func TestChatDistinguishesProviderFromModelAndOnlyWarnsOnSubmit(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("keep this")
	if chat.Editor().Text() != "keep this" {
		t.Fatalf("draft = %q", chat.Editor().Text())
	}
	plain := stripANSI(strings.Join(chat.Render(80), "\n"))
	if !strings.Contains(plain, "No provider selected.") || strings.Contains(plain, "No model selected.") {
		t.Fatalf("provider warning =\n%s", plain)
	}
	if prompts, _ := backend.snapshot(); len(prompts) != 0 {
		t.Fatalf("backend prompts = %#v", prompts)
	}

	chat.SetProvider("openai")
	chat.submit("still here")
	plain = stripANSI(strings.Join(chat.Render(80), "\n"))
	if !strings.Contains(plain, "No model selected.") || !strings.Contains(plain, "Not set • off") || strings.Contains(plain, "No Model") {
		t.Fatalf("model warning =\n%s", plain)
	}
}

func TestChatRestoresPromptWhenRunCannotStart(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: failingChatBackend{err: errors.New("select a model")}, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("keep this")
	if got := chat.Editor().Text(); got != "keep this" {
		t.Fatalf("restored prompt = %q", got)
	}
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(plain, "│ keep this") || strings.Count(plain, "Error: select a model") != 1 {
		t.Fatalf("failed start render:\n%s", plain)
	}
	if transcript := stripANSI(strings.Join(chat.renderTranscript(60), "\n")); strings.Contains(transcript, "Error: select a model") {
		t.Fatalf("failed start leaked into transcript: %q", transcript)
	}
}

func TestToolArgumentSummaryPrefersUsefulStableFields(t *testing.T) {
	if got := toolArgumentSummary(map[string]any{"content": "large", "path": "file.go"}); got != "file.go" {
		t.Fatalf("path summary = %q", got)
	}
	if got := toolArgumentSummary(map[string]any{"z": true, "a": float64(2)}); got != "a=2" {
		t.Fatalf("fallback summary = %q", got)
	}
}

func TestChatQueuesOnePromptPerCompletedRun(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, _ := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	chat.submit("first")
	chat.submit("second")
	chat.submit("third")
	prompts, runs := backend.snapshot()
	if strings.Join(prompts, ",") != "first" {
		t.Fatalf("started early: %#v", prompts)
	}
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool { prompts, _ = backend.snapshot(); return len(prompts) == 2 })
	if prompts[1] != "second" {
		t.Fatalf("second start = %#v", prompts)
	}
	_, runs = backend.snapshot()
	runs[1].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool { prompts, _ = backend.snapshot(); return len(prompts) == 3 })
	if prompts[2] != "third" {
		t.Fatalf("third start = %#v", prompts)
	}
}

func TestEnterQueuesAndCmdEnterSteersTypedInput(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}

	chat.Editor().SetText("first")
	chat.HandleInput("\r")
	chat.Editor().SetText("queued")
	chat.HandleInput("\r")
	chat.Editor().SetText("steer now")
	chat.HandleInput("\x1b[13;9u")

	prompts, runs := backend.snapshot()
	if !slices.Equal(prompts, []string{"first"}) {
		t.Fatalf("fresh prompts = %#v", prompts)
	}
	if got := backend.steerSnapshot(); !slices.Equal(got, []string{"steer now"}) {
		t.Fatalf("steers = %#v", got)
	}
	if got := chat.Editor().Text(); got != "" {
		t.Fatalf("editor after steer = %q", got)
	}
	chat.mu.Lock()
	queued := append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if !slices.Equal(queued, []string{"queued"}) {
		t.Fatalf("queue after typed steer = %#v", queued)
	}

	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("before")}}})
	steered := ai.NewUserMessage("steer now", time.Now())
	runs[0].Push(agent.Event{Type: agent.EventMessageStart, Message: steered})
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: steered})
	runs[0].Push(agent.Event{Type: agent.EventTurnStart})
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("after")}}})
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})

	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})
	transcript := stripANSI(strings.Join(chat.renderTranscript(60), "\n"))
	before, steer, after := strings.Index(transcript, "before"), strings.Index(transcript, "steer now"), strings.Index(transcript, "after")
	if before < 0 || steer <= before || after <= steer {
		t.Fatalf("steered transcript order is wrong:\n%s", transcript)
	}
}

func TestSteeredTurnReportsWorkedDurationOnSupersededBlock(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	_, runs := backend.snapshot()
	runs[0].Push(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{Type: ai.EventTextDelta, Delta: "answer part one"}})
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "1", ToolName: "read", Arguments: map[string]any{"path": "README.md"}})
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: "1", ToolName: "read"})
	// Backdate the open block so the reported duration is deterministic.
	chat.mu.Lock()
	superseded := chat.active
	chat.entries[superseded].startedAt = time.Now().UnixMilli() - 5_000
	chat.mu.Unlock()

	chat.Editor().SetText("steer now")
	chat.HandleInput("\x1b[13;9u")
	steered := ai.NewUserMessage("steer now", time.Now())
	runs[0].Push(agent.Event{Type: agent.EventMessageStart, Message: steered})
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: steered})
	runs[0].Push(agent.Event{Type: agent.EventTurnStart})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.entries[superseded].endedAt != 0
	})

	plain := stripANSI(strings.Join(chat.renderTranscript(60), "\n"))
	if !strings.Contains(plain, "+ Worked for 5s") {
		t.Fatalf("superseded block lost its duration:\n%s", plain)
	}
	if !strings.Contains(plain, "answer part one") {
		t.Fatalf("superseded block lost its answer:\n%s", plain)
	}
}

func TestSteerCardEntersTranscriptBeforeAdmissionAndKeepsItsMode(t *testing.T) {
	backend := &observingSteerBackend{fakeChatBackend: &fakeChatBackend{}}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	_, runs := backend.snapshot()
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("before")},
	}})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return len(chat.entries) == 2 && chat.entries[1].text == "before"
	})

	steerStyle := func(value string) string { return "\x1b[38;5;203m" + value + "\x1b[0m" }
	chat.SetEditorBorderStyle(steerStyle)
	sawCard := false
	backend.beforeAdmission = func(prompt string) {
		if prompt != "steer now" {
			t.Fatalf("admitted prompt = %q", prompt)
		}
		chat.mu.Lock()
		if len(chat.entries) != 3 {
			chat.mu.Unlock()
			t.Fatalf("entries before admission = %#v", chat.entries)
		}
		entry := chat.entries[2]
		chat.mu.Unlock()
		if entry.kind != chatUser || entry.text != "steer now" || !entry.steerPending {
			t.Fatalf("steer card before admission = %#v", entry)
		}
		if got, want := entry.borderStyle("border"), steerStyle("border"); got != want {
			t.Fatalf("steer border = %q, want %q", got, want)
		}
		plain := strings.Join(plainLines(chat.renderTranscript(40)), "\n")
		if !strings.Contains(plain, "│ steer now") || strings.Contains(plain, "Queue:") {
			t.Fatalf("pending steer is not a transcript prompt card:\n%s", plain)
		}
		sawCard = true
	}
	chat.Editor().SetText("steer now")
	chat.HandleInput("\x1b[13;9u")
	if !sawCard {
		t.Fatal("backend admission did not observe the steer card")
	}

	steered := ai.NewUserMessage("steer now", time.Now())
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: steered})
	runs[0].Push(agent.Event{Type: agent.EventTurnStart})
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("after")},
	}})
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})

	got := plainLines(chat.renderTranscript(40))
	margin, available := chatRowGeometry(40)
	want := plainLines(insetChatRows(renderPromptCard("first", available, func(value string) string { return value }), margin))
	want = append(want, "")
	want = append(want, plainLines(renderTranscriptText("before", 40, Padding()))...)
	want = append(want, "")
	want = append(want, plainLines(insetChatRows(renderPromptCard("steer now", available, func(value string) string { return value }), margin))...)
	want = append(want, "")
	want = append(want, plainLines(renderTranscriptText("after", 40, Padding()))...)
	if !slices.Equal(got, want) {
		t.Fatalf("steered transcript geometry:\n%q\nwant:\n%q", got, want)
	}
}

func TestRejectedSteerRemovesProvisionalTranscriptCard(t *testing.T) {
	backend := &observingSteerBackend{
		fakeChatBackend: &fakeChatBackend{},
		err:             errors.New("run ended"),
	}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	chat.Editor().SetText("keep me")
	chat.HandleInput("\x1b[13;9u")

	chat.mu.Lock()
	defer chat.mu.Unlock()
	for _, entry := range chat.entries {
		if entry.kind == chatUser && entry.text == "keep me" {
			t.Fatalf("rejected steer left a ghost transcript card: %#v", chat.entries)
		}
	}
	if !slices.Equal(chat.queue, []string{"keep me"}) {
		t.Fatalf("rejected steer queue = %#v", chat.queue)
	}
}

func TestConsecutiveSteersStayOrderedBeforeTheirSharedContinuation(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	_, runs := backend.snapshot()
	for _, value := range []string{"steer A", "steer B"} {
		chat.Editor().SetText(value)
		chat.HandleInput("\x1b[13;9u")
	}
	for _, value := range []string{"steer A", "steer B"} {
		message := ai.NewUserMessage(value, time.Now())
		runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: message})
	}
	runs[0].Push(agent.Event{Type: agent.EventTurnStart})
	runs[0].Push(agent.Event{Type: agent.EventMessageEnd, Message: ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("shared continuation")},
	}})
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})

	plain := strings.Join(plainLines(chat.renderTranscript(50)), "\n")
	a := strings.Index(plain, "steer A")
	b := strings.Index(plain, "steer B")
	continuation := strings.Index(plain, "shared continuation")
	if a < 0 || b <= a || continuation <= b {
		t.Fatalf("consecutive steer order:\n%s", plain)
	}
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.entries) != 5 || chat.entries[2].kind != chatUser || chat.entries[3].kind != chatUser || chat.entries[4].kind != chatAssistant {
		t.Fatalf("consecutive steer entries = %#v", chat.entries)
	}
}

func TestEmptyCmdEnterSteersOneQueuedMessageAtATime(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, _ := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	chat.submit("first")
	chat.submit("A")
	chat.submit("B")
	chat.HandleInput("\x1b[13;9:3u")
	chat.HandleInput("\x1b[13;9:2u")
	if got := backend.steerSnapshot(); len(got) != 0 {
		t.Fatalf("release/repeat dequeued messages = %#v", got)
	}

	chat.HandleInput("\x1b[13;9u")
	if got := backend.steerSnapshot(); !slices.Equal(got, []string{"A"}) {
		t.Fatalf("first explicit dequeue = %#v", got)
	}
	chat.mu.Lock()
	queued := append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if !slices.Equal(queued, []string{"B"}) {
		t.Fatalf("queue after first dequeue = %#v", queued)
	}

	chat.HandleInput("\x1b[13;9u")
	if got := backend.steerSnapshot(); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("second explicit dequeue = %#v", got)
	}
	chat.mu.Lock()
	queued = append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if len(queued) != 0 {
		t.Fatalf("queue after second dequeue = %#v", queued)
	}
}

func TestEmptyCmdEnterStartsHeldQueueHeadWhenIdle(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, _ := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	chat.submit("first")
	chat.submit("A")
	chat.submit("B")
	chat.escape()
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})

	chat.HandleInput("\x1b[13;9u")
	waitFor(t, func() bool {
		prompts, _ := backend.snapshot()
		return len(prompts) == 2
	})
	prompts, _ := backend.snapshot()
	if !slices.Equal(prompts, []string{"first", "A"}) {
		t.Fatalf("explicit idle dequeue prompts = %#v", prompts)
	}
	chat.mu.Lock()
	queued := append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if !slices.Equal(queued, []string{"B"}) {
		t.Fatalf("held queue remainder = %#v", queued)
	}
}

func TestEmptyCmdEnterRunsQueuedShellWithoutReplacingActiveRun(t *testing.T) {
	backend := &fakeChatBackend{}
	completed := make(chan ShellEntry, 1)
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		ShellRunner: func(_ context.Context, _, command string, output func(string)) (int, bool, error) {
			if command != "printf steered" {
				t.Fatalf("command = %q", command)
			}
			output("steered")
			return 0, false, nil
		},
		OnShellComplete: func(entry ShellEntry) { completed <- entry },
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	chat.submit("! printf steered")
	chat.HandleInput("\x1b[13;9u")

	select {
	case entry := <-completed:
		if entry.Command != "printf steered" || entry.Output != "steered" || entry.Status != "complete" {
			t.Fatalf("steered shell = %#v", entry)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steered shell did not complete")
	}
	if got := backend.steerSnapshot(); len(got) != 0 {
		t.Fatalf("shell command was sent to model: %#v", got)
	}
	chat.mu.Lock()
	busy, queued := chat.busy, append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if !busy || len(queued) != 0 {
		t.Fatalf("active run/queue = %v/%#v", busy, queued)
	}
}

func TestChatAbortHoldsQueueUntilNewWorkCompletes(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, _ := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	chat.submit("first")
	chat.submit("held")
	chat.escape()
	waitFor(t, func() bool { chat.mu.Lock(); defer chat.mu.Unlock(); return !chat.busy })
	prompts, _ := backend.snapshot()
	if len(prompts) != 1 {
		t.Fatalf("held queue dispatched after abort: %#v", prompts)
	}
	chat.submit("recovery")
	waitFor(t, func() bool { prompts, _ = backend.snapshot(); return len(prompts) == 2 })
	_, runs := backend.snapshot()
	runs[1].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool { prompts, _ = backend.snapshot(); return len(prompts) == 3 })
	if prompts[2] != "held" {
		t.Fatalf("held prompt not resumed: %#v", prompts)
	}
}

func TestChatUndoRestoresTheLastPromptWhenIdle(t *testing.T) {
	backend := &fakeUndoBackend{
		fakeChatBackend: &fakeChatBackend{},
		prompt:          "second question",
		messages:        []ai.Message{ai.NewUserMessage("first question", time.Unix(1, 0))},
		undone:          true,
	}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		InitialMessages: []ai.Message{
			ai.NewUserMessage("first question", time.Unix(1, 0)),
			ai.NewUserMessage("second question", time.Unix(2, 0)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isSlashCommandCall("/undo") {
		t.Fatal("/undo is not routed as a command")
	}
	chat.submit("/undo")
	if got := chat.Editor().Text(); got != "second question" {
		t.Fatalf("editor = %q", got)
	}
	chat.mu.Lock()
	users := make([]string, 0, 2)
	for _, entry := range chat.entries {
		if entry.kind == chatUser {
			users = append(users, entry.text)
		}
	}
	chat.mu.Unlock()
	if len(users) != 1 || users[0] != "first question" {
		t.Fatalf("transcript after undo = %#v", users)
	}
}

func TestChatUndoReportsNothingToUndo(t *testing.T) {
	backend := &fakeUndoBackend{fakeChatBackend: &fakeChatBackend{}}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test", InitialDraft: "typed"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("/undo")
	if got := chat.Editor().Text(); got != "typed" {
		t.Fatalf("editor = %q", got)
	}
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(plain, "Nothing to undo") {
		t.Fatalf("toast missing:\n%s", plain)
	}
}

func TestChatUndoStopsTheRunThenRevertsAndHoldsTheQueue(t *testing.T) {
	backend := &fakeUndoBackend{
		fakeChatBackend: &fakeChatBackend{},
		prompt:          "first",
		undone:          true,
	}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("first")
	chat.submit("queued")
	chat.submit("/undo")
	waitFor(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return !backend.undone
	})
	waitFor(t, func() bool { chat.mu.Lock(); defer chat.mu.Unlock(); return !chat.busy })
	backend.fakeChatBackend.mu.Lock()
	aborts := backend.aborts
	backend.fakeChatBackend.mu.Unlock()
	if aborts != 1 {
		t.Fatalf("aborts = %d", aborts)
	}
	if got := chat.Editor().Text(); got != "first" {
		t.Fatalf("editor = %q", got)
	}
	chat.mu.Lock()
	held, queue := chat.queueHeld, append([]string(nil), chat.queue...)
	chat.mu.Unlock()
	if !held || len(queue) != 1 || queue[0] != "queued" {
		t.Fatalf("queue = %#v held = %v", queue, held)
	}
	prompts, _ := backend.snapshot()
	if len(prompts) != 1 {
		t.Fatalf("aborted queue dispatched: %#v", prompts)
	}
}

func TestChatPersistsDraftAndQueueChanges(t *testing.T) {
	backend := &fakeChatBackend{}
	var mu sync.Mutex
	draft := ""
	var queue []string
	held := false
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test", InitialDraft: "saved", InitialQueue: []string{"prior"}, QueueHeld: true,
		OnDraftChange: func(value string) { mu.Lock(); draft = value; mu.Unlock() },
		OnQueueChange: func(values []string, value bool) {
			mu.Lock()
			queue, held = append([]string(nil), values...), value
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chat.Editor().Text() != "saved" {
		t.Fatalf("initial draft = %q", chat.Editor().Text())
	}
	chat.Editor().InsertText(" text")
	mu.Lock()
	gotDraft := draft
	mu.Unlock()
	if gotDraft != "saved text" {
		t.Fatalf("draft callback = %q", gotDraft)
	}
	chat.submit("first")
	chat.submit("second")
	mu.Lock()
	gotQueue, gotHeld := append([]string(nil), queue...), held
	mu.Unlock()
	if strings.Join(gotQueue, ",") != "prior,second" || gotHeld {
		t.Fatalf("queue callback = %#v held=%v", gotQueue, gotHeld)
	}
}

func TestChatVoiceLifecycleKeepsTranscriptInEditor(t *testing.T) {
	backend := &fakeChatBackend{}
	stops := 0
	var chat *Chat
	chat, _ = NewChat(ChatOptions{Backend: backend, OnVoiceStop: func() {
		stops++
		chat.EndVoice(true)
	}})
	chat.Editor().SetText("base ")
	chat.StartVoice()
	// A cold start loads the model, so the frame reports Loading first.
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(plain, "Voice: Loading") || strings.Contains(plain, "Listening") {
		t.Fatalf("voice loading render:\n%s", plain)
	}
	chat.SetVoiceReady(true)
	chat.SetVoiceText("hello ", "world")
	plain = stripANSI(strings.Join(chat.Render(60), "\n"))
	if !strings.Contains(plain, "Voice: Listening") || strings.Contains(plain, "Loading") || !strings.Contains(plain, "base hello world") {
		t.Fatalf("voice render:\n%s", plain)
	}
	chat.HandleInput("x")
	if chat.Editor().Text() != "base " {
		t.Fatalf("voice accepted typing: %q", chat.Editor().Text())
	}
	chat.HandleInput("\x1b")
	if stops != 1 || chat.VoiceActive() || chat.Editor().Text() != "base hello world" {
		t.Fatalf("voice stop = %d active=%v text=%q", stops, chat.VoiceActive(), chat.Editor().Text())
	}
	if plain = stripANSI(strings.Join(chat.Render(60), "\n")); strings.Contains(plain, "Listening") {
		t.Fatalf("listening label remained after stop:\n%s", plain)
	}
}

func TestChatVoiceKeepsBaseStableAcrossPartialRevisions(t *testing.T) {
	chat, _ := NewChat(ChatOptions{Backend: &fakeChatBackend{}})
	chat.Editor().SetText("existing text")
	chat.StartVoice()
	chat.SetVoiceText("", "first guess")
	chat.SetVoiceText("", "corrected phrase")
	chat.EndVoice(true)
	if got := chat.Editor().Text(); got != "existing text corrected phrase" {
		t.Fatalf("voice text = %q", got)
	}
}

func TestChatEscapeHidesListeningWhileFinalTextIsPending(t *testing.T) {
	chat, _ := NewChat(ChatOptions{Backend: &fakeChatBackend{}, OnVoiceStop: func() {}})
	chat.Editor().SetText("keep")
	chat.StartVoice()
	chat.SetVoiceText("captured", "words")
	chat.HandleInput("\x1b")
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if strings.Contains(plain, "Listening") || !strings.Contains(plain, "keep captured words") {
		t.Fatalf("stopping voice render:\n%s", plain)
	}
	chat.EndVoice(true)
	if got := chat.Editor().Text(); got != "keep captured words" {
		t.Fatalf("retained voice text = %q", got)
	}
}

func TestChatRoutesLocalCommands(t *testing.T) {
	backend := &fakeChatBackend{}
	called := make(chan string, 1)
	chat, _ := NewChat(ChatOptions{
		Backend: backend,
		Command: func(_ context.Context, name, args string) (string, error) {
			called <- name + ":" + args
			if name == "fail" {
				return "", errors.New("broken")
			}
			return "goal active", nil
		},
	})
	chat.submit("/goal show")
	select {
	case got := <-called:
		if got != "goal:show" {
			t.Fatalf("command = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("command was not routed")
	}
	waitFor(t, func() bool { return strings.Contains(stripANSI(strings.Join(chat.Render(50), "\n")), "goal active") })
}

func TestChatRoutesOverlayCommandsBeforeLocalCommands(t *testing.T) {
	backend := &fakeChatBackend{}
	overlayCalls, localCalls := 0, 0
	chat, err := NewChat(ChatOptions{
		Backend: backend,
		OverlayCommand: func(name, args string) bool {
			overlayCalls++
			return name == "model" && args == ""
		},
		Command: func(context.Context, string, string) (string, error) {
			localCalls++
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("/model")
	if overlayCalls != 1 || localCalls != 0 {
		t.Fatalf("calls = overlay %d, local %d", overlayCalls, localCalls)
	}
	chat.SetModel("new-model")
	chat.SetAgent("orchestrator")
	chat.SetThinking(ai.ThinkingHigh)
	chat.SetEditorBorderStyle(func(value string) string { return "[[" + value + "]]" })
	chat.Notice("multitask enabled")
	rendered := strings.Join(chat.Render(50), "\n")
	if plain := stripANSI(rendered); !strings.Contains(plain, "New Model") || !strings.Contains(plain, "orchestrator") || !strings.Contains(plain, "thinking high") || !strings.Contains(plain, "multitask enabled") {
		t.Fatalf("updated model missing:\n%s", plain)
	}
	if !strings.Contains(rendered, "[[") {
		t.Fatalf("updated editor border missing:\n%s", rendered)
	}
}

func TestChatRendersResumedMessages(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, InitialMessages: []ai.Message{
		ai.NewUserMessage("saved question", time.Unix(1, 0)),
		ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("saved answer")}, StopReason: ai.StopComplete},
	}})
	if err != nil {
		t.Fatal(err)
	}
	plain := stripANSI(strings.Join(chat.Render(50), "\n"))
	if !strings.Contains(plain, "saved question") || !strings.Contains(plain, "saved answer") {
		t.Fatalf("resumed render:\n%s", plain)
	}
}

func TestResumedPromptCardsKeepTheirSentModeColour(t *testing.T) {
	mainPrompt := ai.NewUserMessage("main prompt", time.Unix(1, 0))
	mainPrompt.Agent = "main"
	mainPrompt.Thinking = ai.ThinkingOff
	highPrompt := ai.NewUserMessage("thinking prompt", time.Unix(2, 0))
	highPrompt.Agent = "main"
	highPrompt.Thinking = ai.ThinkingHigh
	entries := transcriptEntries([]ai.Message{mainPrompt, highPrompt})
	if len(entries) != 2 || entries[0].borderStyle == nil || entries[1].borderStyle == nil {
		t.Fatalf("prompt entries = %#v", entries)
	}
	if got, want := entries[0].borderStyle("border"), CurrentTheme().Thinking(ai.ThinkingOff, "border"); got != want {
		t.Fatalf("main border = %q, want %q", got, want)
	}
	if got, want := entries[1].borderStyle("border"), CurrentTheme().Thinking(ai.ThinkingHigh, "border"); got != want {
		t.Fatalf("high-thinking border = %q, want %q", got, want)
	}
}

func TestLivePromptCardDoesNotRecolourAfterModeChanges(t *testing.T) {
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetEditorBorderStyle(func(value string) string { return "<multitask>" + value + "</multitask>" })
	chat.submit("sent in multitask")
	_, runs := backend.snapshot()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})
	chat.SetEditorBorderStyle(func(value string) string { return "<main>" + value + "</main>" })
	chat.mu.Lock()
	style := chat.entries[0].borderStyle
	chat.mu.Unlock()
	if style == nil {
		t.Fatal("prompt card lost its sent-mode border")
	}
	if got := style("border"); got != "<multitask>border</multitask>" {
		t.Fatalf("prompt card recoloured after mode switch: %q", got)
	}
}

func TestShellModeColourExecutionAndRoundedTranscript(t *testing.T) {
	completed := make(chan ShellEntry, 1)
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, CWD: t.TempDir(),
		ShellRunner: func(_ context.Context, _, command string, output func(string)) (int, bool, error) {
			if command != "printf hello" {
				t.Fatalf("command = %q", command)
			}
			output("hello\n")
			return 0, false, nil
		},
		OnShellComplete: func(entry ShellEntry) { completed <- entry },
	})
	if err != nil {
		t.Fatal(err)
	}
	purple := func(value string) string { return "<purple>" + value + "</purple>" }
	chat.SetEditorBorderStyle(purple)
	chat.HandleInput("!")
	if chat.Editor().Text() != "! " {
		t.Fatalf("shell prefix = %q", chat.Editor().Text())
	}
	if got, want := chat.editor.borderStyle("border"), CurrentTheme().FG("bashMode", "border"); got != want {
		t.Fatalf("shell border = %q, want %q", got, want)
	}
	// The frame takes the same gutter as every other row, so it is one cell shorter
	// on each side and inset by the padding.
	if got, want := chat.renderEditorDock(40)[0], " "+CurrentTheme().FG("bashMode", "╭"+strings.Repeat("─", 36)+"╮"); got != want {
		t.Fatalf("rendered shell border = %q, want %q", got, want)
	}
	chat.HandleInput("\x7f")
	if chat.Editor().Text() != "!" || chat.editor.borderStyle("border") != "<purple>border</purple>" {
		t.Fatalf("deactivated shell = %q border=%q", chat.Editor().Text(), chat.editor.borderStyle("border"))
	}
	if got, want := chat.renderEditorDock(40)[0], " "+purple("╭"+strings.Repeat("─", 36)+"╮"); got != want {
		t.Fatalf("rendered deactivated border = %q, want %q", got, want)
	}
	chat.HandleInput(" ")
	chat.HandleInput("printf hello")
	chat.HandleInput("\r")
	select {
	case entry := <-completed:
		if entry.Command != "printf hello" || entry.Output != "hello\n" || entry.Status != "complete" || entry.ExitCode == nil || *entry.ExitCode != 0 {
			t.Fatalf("shell entry = %#v", entry)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shell command did not complete")
	}
	plain := stripANSI(strings.Join(chat.renderTranscript(40), "\n"))
	for _, want := range []string{"╭", "╮", "╰", "╯", "$ printf hello", "hello"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("shell transcript missing %q:\n%s", want, plain)
		}
	}
}

func TestCtrlTCyclesThinkingWithoutChangingDraft(t *testing.T) {
	cycles := 0
	chat, err := NewChat(ChatOptions{
		Backend:         &fakeChatBackend{},
		InitialDraft:    "keep this draft",
		OnCycleThinking: func() { cycles++ },
	})
	if err != nil {
		t.Fatal(err)
	}

	chat.HandleInput("\x14")
	if cycles != 1 {
		t.Fatalf("legacy ctrl+t cycles = %d, want 1", cycles)
	}
	if got := chat.Editor().Text(); got != "keep this draft" {
		t.Fatalf("draft after ctrl+t = %q", got)
	}

	chat.HandleInput("\x1b[116;5u")
	if cycles != 2 {
		t.Fatalf("kitty ctrl+t cycles = %d, want 2", cycles)
	}
}

func TestGeneratedHeaderStatusHasNoPunctuationAndHonoursWordCap(t *testing.T) {
	maximums := make(chan int, 1)
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "small-fast", StatusMaxWords: 4,
		StatusGenerator: func(_ context.Context, _ string, maximum int) (string, error) {
			maximums <- maximum
			return `"Parser implemented, tests running!!! extra words"`, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("implement compact header")
	_, runs := backend.snapshot()
	runs[0].Push(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{Type: ai.EventTextDelta, Delta: "Implementing"}})
	select {
	case got := <-maximums:
		if got != 4 {
			t.Fatalf("status maximum = %d, want 4", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status generator was not called")
	}
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.statusText != ""
	})
	header := stripANSI(strings.Join(chat.renderHeader(72), "\n"))
	if !strings.Contains(header, "Parser implemented tests running") {
		t.Fatalf("generated status missing from header: %q", header)
	}
	if strings.ContainsAny(strings.Split(header, "\n")[1], ",!\"") {
		t.Fatalf("generated status retained punctuation: %q", header)
	}
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
}

func TestStatusPhraseCleanupIsDeterministic(t *testing.T) {
	if got, want := cleanStatusPhrase("  writing parser-tests, then QA!!!  ", 4), "writing parser tests then"; got != want {
		t.Fatalf("clean status = %q, want %q", got, want)
	}
}

func TestCompactHeaderDoesNotInvokeStatusModel(t *testing.T) {
	called := false
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, CompactHeader: true,
		StatusGenerator: func(context.Context, string, int) (string, error) {
			called = true
			return "unexpected", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.mu.Lock()
	chat.entries = []chatEntry{{kind: chatUser, text: "work"}, {kind: chatAssistant, text: "progress"}}
	chat.active = 1
	chat.busy = true
	chat.mu.Unlock()
	chat.maybeGenerateStatus()
	if called {
		t.Fatal("compact header invoked the status model")
	}
}

func TestShellParserRequiresBangSpaceAtTheStart(t *testing.T) {
	for _, test := range []struct {
		value   string
		command string
		exclude bool
		ok      bool
	}{
		{value: "! ls -la", command: "ls -la", ok: true},
		{value: "!! git status", command: "git status", exclude: true, ok: true},
		{value: "! ", ok: true},
		{value: "!not-shell"},
		{value: " ! ls"},
	} {
		command, exclude, ok := parseShellCommand(test.value)
		if command != test.command || exclude != test.exclude || ok != test.ok {
			t.Fatalf("parseShellCommand(%q) = %q, %v, %v", test.value, command, exclude, ok)
		}
	}
}

func TestChatReplacesSessionTranscript(t *testing.T) {
	chat, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, SessionID: "old", InitialMessages: []ai.Message{ai.NewUserMessage("old question", time.Unix(1, 0))}})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetSession("new", []ai.Message{ai.NewUserMessage("new question", time.Unix(2, 0))}, nil, "draft", []string{"queued"}, true)
	plain := stripANSI(strings.Join(chat.Render(60), "\n"))
	if strings.Contains(plain, "old question") || !strings.Contains(plain, "new question") || !strings.Contains(plain, "new") || !strings.Contains(plain, "draft") || !strings.Contains(plain, "1. queued") {
		t.Fatalf("switched session:\n%s", plain)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not reached")
}

func stripANSI(value string) string {
	value = strings.ReplaceAll(value, CursorMarker, "")
	for {
		start := strings.Index(value, "\x1b[")
		if start < 0 {
			return value
		}
		end := start + 2
		for end < len(value) && (value[end] < '@' || value[end] > '~') {
			end++
		}
		if end >= len(value) {
			return value[:start]
		}
		value = value[:start] + value[end+1:]
	}
}

// fakeCompactBackend answers /compact with a rewritten transcript.
type fakeCompactBackend struct {
	*fakeChatBackend
	mu       sync.Mutex
	messages []ai.Message
	done     bool
	calls    int
}

func (b *fakeCompactBackend) CompactContext(context.Context) ([]ai.Message, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if !b.done {
		return nil, false, nil
	}
	b.done = false
	return b.messages, true, nil
}

func TestChatCompactReplacesTheTranscript(t *testing.T) {
	backend := &fakeCompactBackend{
		fakeChatBackend: &fakeChatBackend{},
		messages:        []ai.Message{ai.NewSyntheticMessage("summary", time.Unix(1, 0)), ai.NewUserMessage("recent", time.Unix(2, 0))},
		done:            true,
	}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		InitialMessages: []ai.Message{ai.NewUserMessage("old", time.Unix(0, 0))},
		InitialQueue:    []string{"queued"}, InitialDraft: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isSlashCommandCall("/compact") {
		t.Fatal("/compact is not routed as a command")
	}
	chat.submit("/compact")
	waitFor(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.calls == 1 && !backend.done
	})
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return len(chat.entries) == 2 && chat.entries[0].kind == chatNotice
	})
	chat.mu.Lock()
	queue, held := append([]string(nil), chat.queue...), chat.queueHeld
	chat.mu.Unlock()
	if len(queue) != 1 || queue[0] != "queued" {
		t.Fatalf("queue = %#v", queue)
	}
	_ = held
	if got := chat.Editor().Text(); got != "draft" {
		t.Fatalf("draft = %q", got)
	}
}

func TestChatCompactReportsNothingToCompact(t *testing.T) {
	backend := &fakeCompactBackend{fakeChatBackend: &fakeChatBackend{}}
	chat, err := NewChat(ChatOptions{Backend: backend, Provider: "fake", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("/compact")
	waitFor(t, func() bool {
		return strings.Contains(stripANSI(strings.Join(chat.Render(60), "\n")), "Nothing to compact")
	})
	backend.mu.Lock()
	calls := backend.calls
	backend.mu.Unlock()
	if calls != 1 {
		t.Fatalf("compact calls = %d", calls)
	}
}
