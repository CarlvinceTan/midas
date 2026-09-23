package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/internal/goal"
	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

type appProvider struct {
	mu       sync.Mutex
	response ai.AssistantMessage
	started  chan struct{}
	release  chan struct{}
}

type appSteerProvider struct {
	mu       sync.Mutex
	contexts []ai.Context
	started  chan int
	release  []chan struct{}
}

func (p *appSteerProvider) Stream(ctx context.Context, _ ai.Model, input ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
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
	message := appAssistant(fmt.Sprintf("reply %d", index+1))
	stream := ai.NewAssistantStream()
	partial := message
	partial.Content = nil
	partial.StopReason = ai.StopPending
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Message: &message})
	return stream, nil
}

type failingAppProvider struct {
	err   error
	calls int
}

func (p *failingAppProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (*ai.AssistantStream, error) {
	p.calls++
	return nil, p.err
}

func (p *appProvider) Stream(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	if p.started != nil {
		select {
		case <-p.started:
		default:
			close(p.started)
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	message := p.response
	p.mu.Unlock()
	stream := ai.NewAssistantStream()
	partial := message
	partial.Content = nil
	partial.StopReason = ai.StopPending
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Message: &message})
	return stream, nil
}

func TestChatWiresMinimalGoalToolsAndPersistsSession(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	goals := goal.NewStore(filepath.Join(root, "config", "goal.json"))
	provider := &appProvider{response: appAssistant("done")}
	chat, err := New(Options{
		Root: root, SessionID: "session-1", Provider: provider,
		Model: ai.Model{ID: "test", Provider: "fake"}, Agent: "orchestrator", Sessions: store, Goals: goals,
		StreamOptions: ai.StreamOptions{Reasoning: ai.ThinkingHigh},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chat.options.StreamOptions.SessionID != "session-1" || chat.options.StreamOptions.CacheRetention != ai.CacheShort {
		t.Fatalf("cache defaults = %#v", chat.options.StreamOptions)
	}
	var names []string
	defined, err := chat.Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range defined {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "read,write,edit,bash,create_goal,get_goal,update_goal,clear_goal" {
		t.Fatalf("tools = %s", got)
	}
	run, err := chat.Prompt(context.Background(), "implement native runtime")
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, ok, nextErr := run.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
	}
	result, err := run.Result(context.Background())
	if err != nil || len(result) != 2 || len(chat.Messages()) != 2 || chat.Busy() {
		t.Fatalf("result/messages/busy = %d/%d/%v, %v", len(result), len(chat.Messages()), chat.Busy(), err)
	}
	user, ok := chat.Messages()[0].(ai.UserMessage)
	if !ok || user.Agent != "orchestrator" || user.Thinking != ai.ThinkingHigh {
		t.Fatalf("prompt mode metadata = %#v", chat.Messages()[0])
	}
	resumed, err := store.ReadTranscript("session-1")
	if err != nil {
		t.Fatal(err)
	}
	resumedUser, ok := resumed[0].(ai.UserMessage)
	if !ok || resumedUser.Agent != "orchestrator" || resumedUser.Thinking != ai.ThinkingHigh {
		t.Fatalf("resumed prompt mode metadata = %#v", resumed[0])
	}
	stored := store.ReadSessions()
	if len(stored) != 1 || stored[0].ID != "session-1" || stored[0].Title != "Implement native runtime" {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestChatBusyAndAbort(t *testing.T) {
	provider := &appProvider{
		response: appAssistant("never"), started: make(chan struct{}), release: make(chan struct{}),
	}
	chat, err := New(Options{Root: t.TempDir(), Provider: provider, Model: ai.Model{ID: "test", Provider: "fake"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := chat.Prompt(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if _, err := chat.Prompt(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second prompt = %v", err)
	}
	if !chat.Abort(errors.New("stop now")) {
		t.Fatal("abort reported idle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, ok, nextErr := run.Next(ctx)
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
	}
	result, err := run.Result(ctx)
	if err != nil || len(result) != 2 {
		t.Fatalf("abort result = %#v, %v", result, err)
	}
	message := result[1].(ai.AssistantMessage)
	if message.StopReason != ai.StopAborted || !strings.Contains(message.ErrorMessage, "context canceled") {
		t.Fatalf("abort message = %#v", message)
	}
}

func TestChatSteersActiveRunAndPersistsMessage(t *testing.T) {
	provider := &appSteerProvider{
		started: make(chan int, 2),
		release: []chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	chat, err := New(Options{
		Root: t.TempDir(), Provider: provider, Model: ai.Model{ID: "test", Provider: "fake"},
		Agent: "main", StreamOptions: ai.StreamOptions{Reasoning: ai.ThinkingHigh},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := chat.Prompt(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if call := <-provider.started; call != 0 {
		t.Fatalf("first provider call = %d", call)
	}
	if err := chat.Steer("change direction"); err != nil {
		t.Fatal(err)
	}
	close(provider.release[0])
	if call := <-provider.started; call != 1 {
		t.Fatalf("second provider call = %d", call)
	}
	close(provider.release[1])

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, ok, nextErr := run.Next(ctx)
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
	}
	if _, err := run.Result(ctx); err != nil {
		t.Fatal(err)
	}
	messages := chat.Messages()
	if len(messages) != 4 {
		t.Fatalf("persisted steered messages = %#v", messages)
	}
	steered, ok := messages[2].(ai.UserMessage)
	text, _ := steered.Content.Text()
	if !ok || text != "change direction" || steered.Agent != "main" || steered.Thinking != ai.ThinkingHigh {
		t.Fatalf("persisted steer = %#v", messages[2])
	}
	if err := chat.Steer("too late"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("late steer = %v", err)
	}
}

func TestChatValidatesConfiguration(t *testing.T) {
	if _, err := New(Options{Root: t.TempDir(), Model: ai.Model{ID: "x"}}); err == nil {
		t.Fatal("nil provider succeeded")
	}
	chat, err := New(Options{Root: t.TempDir(), Provider: &appProvider{}})
	if err != nil {
		t.Fatalf("model-less interactive session rejected: %v", err)
	}
	if _, err := chat.Prompt(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "select a model") {
		t.Fatalf("model-less prompt = %v", err)
	}
}

func TestChatProfileToolsEnforceCapabilityModes(t *testing.T) {
	root := t.TempDir()
	provider := &appProvider{response: appAssistant("done")}
	readOnly, err := New(Options{Root: root, Provider: provider, Model: ai.Model{ID: "test"}, CodingTools: CodingToolsReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	var readOnlyNames []string
	readOnlyTools, err := readOnly.Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range readOnlyTools {
		readOnlyNames = append(readOnlyNames, tool.Name)
	}
	if got := strings.Join(readOnlyNames, ","); got != "read,list,grep" {
		t.Fatalf("read-only tools = %q", got)
	}
	none, err := New(Options{
		Root: root, Provider: provider, Model: ai.Model{ID: "test"}, CodingTools: CodingToolsNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tools, err := none.Tools(); err != nil || len(tools) != 0 {
		t.Fatalf("no-tools profile = %#v (err %v)", tools, err)
	}
	if err := none.SetProfile("main", "main", CodingToolsAll, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	mainTools, err := none.Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range mainTools {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, ","); got != "read,write,edit,bash" {
		t.Fatalf("main tools = %q", got)
	}
}

func TestChatModelCanChangeOnlyWhileIdle(t *testing.T) {
	provider := &appProvider{response: appAssistant("done")}
	chat, err := New(Options{Root: t.TempDir(), Provider: provider, Model: ai.Model{ID: "first", Provider: "fake"}})
	if err != nil {
		t.Fatal(err)
	}
	second := ai.Model{ID: "second", Provider: "fake", API: "fake-api"}
	if err := chat.SetModel(second); err != nil || chat.Model().ID != "second" {
		t.Fatalf("idle model change = %#v, %v", chat.Model(), err)
	}
	if err := chat.SetReasoning(ai.ThinkingHigh); err != nil || chat.Reasoning() != ai.ThinkingHigh {
		t.Fatalf("idle thinking change = %q, %v", chat.Reasoning(), err)
	}
	if err := chat.SetReasoning("impossible"); err == nil {
		t.Fatal("invalid thinking level succeeded")
	}
	replacement := &appProvider{response: appAssistant("replacement")}
	third := ai.Model{ID: "third", Provider: "replacement", API: "replacement-api"}
	if err := chat.SetRuntime(replacement, third, "replacement-key"); err != nil || chat.Model().ID != "third" || chat.options.Provider != replacement || chat.options.StreamOptions.APIKey != "replacement-key" {
		t.Fatalf("runtime change = %#v, %v", chat.Model(), err)
	}
	blocking := &appProvider{response: appAssistant("done"), started: make(chan struct{}), release: make(chan struct{})}
	busy, err := New(Options{Root: t.TempDir(), Provider: blocking, Model: second})
	if err != nil {
		t.Fatal(err)
	}
	run, err := busy.Prompt(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	<-blocking.started
	if err := busy.SetModel(ai.Model{ID: "third", Provider: "fake"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy model change = %v", err)
	}
	if err := busy.SetReasoning(ai.ThinkingLow); err != nil || busy.Reasoning() != ai.ThinkingLow {
		t.Fatalf("busy next-prompt thinking change = %q, %v", busy.Reasoning(), err)
	}
	close(blocking.release)
	if _, err := run.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChatFailoverRetriesBeforeOutputAndKeepsSinglePrompt(t *testing.T) {
	failure := &ai.HTTPError{Provider: "first", Status: 503, Message: "unavailable"}
	first := &failingAppProvider{err: failure}
	second := &appProvider{response: appAssistant("replacement answer")}
	chat, err := New(Options{Root: t.TempDir(), Provider: first, Model: ai.Model{ID: "one", Provider: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	var previous, next ai.Model
	chat.SetFailover(func(_ context.Context, failed ai.Model, got error, attempted []ai.Model) (Runtime, bool) {
		if !errors.Is(got, failure) || failed.ID != "one" || len(attempted) != 1 {
			t.Fatalf("failover inputs = %#v, %v, %#v", failed, got, attempted)
		}
		return Runtime{Provider: second, Model: ai.Model{ID: "two", Provider: "second"}, APIKey: "next-key"}, true
	}, func(from, to ai.Model, _ error) {
		previous, next = from, to
	})
	run, err := chat.Prompt(context.Background(), "retry once")
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.Event
	for {
		event, ok, nextErr := run.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
		events = append(events, event)
	}
	result, err := run.Result(context.Background())
	if err != nil || len(result) != 2 || first.calls != 1 || chat.Model().ID != "two" || chat.options.StreamOptions.APIKey != "next-key" {
		t.Fatalf("result/calls/model = %#v/%d/%#v, %v", result, first.calls, chat.Model(), err)
	}
	if previous.ID != "one" || next.ID != "two" {
		t.Fatalf("notice = %#v -> %#v", previous, next)
	}
	userStarts := 0
	for _, event := range events {
		if event.Type == agent.EventMessageStart {
			if _, ok := event.Message.(ai.UserMessage); ok {
				userStarts++
			}
			if message, ok := event.Message.(ai.AssistantMessage); ok && message.StopReason == ai.StopError {
				t.Fatalf("transient failure leaked into output: %#v", event)
			}
		}
	}
	if userStarts != 1 {
		t.Fatalf("user message starts = %d", userStarts)
	}
}

func TestChatResumesNativeTranscript(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	provider := &appProvider{response: appAssistant("first answer")}
	first, err := New(Options{Root: root, SessionID: "ses_resume", Provider: provider, Model: ai.Model{ID: "test", Provider: "fake"}, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.Prompt(context.Background(), "first question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Result(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := New(Options{Root: root, SessionID: "ses_resume", Provider: provider, Model: ai.Model{ID: "test", Provider: "fake"}, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Messages(); len(got) != 2 {
		t.Fatalf("resumed messages = %#v", got)
	}
	user, ok := second.Messages()[0].(ai.UserMessage)
	if text, textOK := user.Content.Text(); !ok || !textOK || text != "first question" {
		t.Fatalf("resumed user = %#v", second.Messages()[0])
	}
}

func TestChatRevertLastPromptRemovesPromptAndEverythingAfterIt(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	if _, err := New(Options{Root: root, SessionID: "ses_undo", Provider: &appProvider{response: appAssistant("done")}, Model: ai.Model{ID: "test"}, Sessions: store}); err != nil {
		t.Fatal(err)
	}
	seeded := []ai.Message{
		ai.NewUserMessage("first", time.Unix(1, 0)), appAssistant("one"),
		ai.NewUserMessage("second", time.Unix(2, 0)), appAssistant("two"),
	}
	if err := store.WriteTranscript("ses_undo", seeded); err != nil {
		t.Fatal(err)
	}
	resumed, err := New(Options{Root: root, SessionID: "ses_undo", Provider: &appProvider{response: appAssistant("done")}, Model: ai.Model{ID: "test"}, Sessions: store})
	if err != nil || len(resumed.Messages()) != 4 {
		t.Fatalf("resumed = %#v, %v", resumed.Messages(), err)
	}

	prompt, remaining, undone, err := resumed.RevertLastPrompt()
	if err != nil || !undone || prompt != "second" {
		t.Fatalf("revert = %q %#v %v %v", prompt, remaining, undone, err)
	}
	if len(remaining) != 2 || len(resumed.Messages()) != 2 {
		t.Fatalf("remaining = %#v", resumed.Messages())
	}
	if text, ok := remaining[0].(ai.UserMessage).Content.Text(); !ok || text != "first" {
		t.Fatalf("remaining user = %#v", remaining[0])
	}
	saved, err := store.ReadTranscript("ses_undo")
	if err != nil || len(saved) != 2 {
		t.Fatalf("saved transcript = %#v, %v", saved, err)
	}

	// A second revert drops the opening prompt too, and a third has nothing to do.
	if _, remaining, undone, err = resumed.RevertLastPrompt(); err != nil || !undone || len(remaining) != 0 {
		t.Fatalf("second revert = %#v %v %v", remaining, undone, err)
	}
	if _, _, undone, err = resumed.RevertLastPrompt(); err != nil || undone {
		t.Fatalf("empty revert = %v %v", undone, err)
	}
}

func TestChatRevertLastPromptRefusesWhileRunning(t *testing.T) {
	provider := &appProvider{response: appAssistant("done"), started: make(chan struct{}), release: make(chan struct{})}
	chat, err := New(Options{Root: t.TempDir(), Provider: provider, Model: ai.Model{ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := chat.Prompt(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if _, _, _, revertErr := chat.RevertLastPrompt(); !errors.Is(revertErr, ErrBusy) {
		t.Fatalf("revert while running = %v", revertErr)
	}
	close(provider.release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, ok, nextErr := run.Next(ctx); nextErr != nil || !ok {
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			break
		}
	}
	if _, _, undone, err := chat.RevertLastPrompt(); err != nil || !undone {
		t.Fatalf("revert after the run = %v %v", undone, err)
	}
}

func TestChatSwitchesAndCreatesNativeSessions(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	saved := []ai.Message{ai.NewUserMessage("saved", time.Unix(1, 0)), appAssistant("answer")}
	if err := store.WriteTranscript("ses_saved", saved); err != nil {
		t.Fatal(err)
	}
	chat, err := New(Options{Root: root, SessionID: "ses_initial", Provider: &appProvider{response: appAssistant("done")}, Model: ai.Model{ID: "test"}, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	id, messages, err := chat.SwitchSession("ses_saved")
	if err != nil || id != "ses_saved" || chat.ID() != id || len(messages) != 2 || len(chat.Messages()) != 2 {
		t.Fatalf("saved switch = %q %#v, %v", id, messages, err)
	}
	created, messages, err := chat.SwitchSession("")
	if err != nil || !strings.HasPrefix(created, "ses_") || len(messages) != 0 || chat.ID() != created {
		t.Fatalf("new switch = %q %#v, %v", created, messages, err)
	}
}

func appAssistant(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(text)}, StopReason: ai.StopComplete,
		Provider: "fake", Model: "test", Timestamp: ai.UnixMillis(time.Now()),
	}
}

// compactingProvider answers the summarization request, then the run itself, so
// one test can observe both calls.
type compactingProvider struct {
	mu       sync.Mutex
	contexts []ai.Context
	options  []ai.StreamOptions
}

func (p *compactingProvider) ID() string                                 { return "compacting" }
func (p *compactingProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }

func (p *compactingProvider) Stream(_ context.Context, _ ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	p.mu.Lock()
	p.contexts = append(p.contexts, input)
	p.options = append(p.options, options)
	index := len(p.contexts)
	p.mu.Unlock()
	message := appAssistant("reply")
	if index == 1 {
		message = appAssistant("## Goal\nsummary of the old half")
	}
	stream := ai.NewAssistantStream()
	partial := message
	partial.Content = nil
	partial.StopReason = ai.StopPending
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: message.StopReason, Message: &message})
	return stream, nil
}

func TestChatAutoCompactsBeforeARunThatWouldExceedTheWindow(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	history := make([]ai.Message, 0, 12)
	for index := 0; index < 10; index++ {
		text := strings.Repeat("x", 4000) // ~1000 estimated tokens per message
		if index%2 == 0 {
			history = append(history, ai.NewUserMessage(text, time.Unix(int64(index), 0)))
			continue
		}
		history = append(history, ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(text)}, StopReason: ai.StopComplete, Timestamp: int64(index)})
	}
	if err := store.WriteTranscript("ses_compact", history); err != nil {
		t.Fatal(err)
	}
	provider := &compactingProvider{}
	chat, err := New(Options{
		Root: root, SessionID: "ses_compact", Provider: provider,
		Model:      ai.Model{ID: "test", Provider: "compacting", ContextWindow: 6000},
		Sessions:   store,
		Compaction: agent.CompactionSettings{Enabled: true, ReserveTokens: 1024, KeepRecentTokens: 2000},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := chat.Prompt(context.Background(), "keep going")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	compactions := [][2]int{}
	for {
		event, ok, nextErr := run.Next(ctx)
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
		if event.Type == agent.EventCompaction {
			compactions = append(compactions, [2]int{event.TokensBefore, event.TokensAfter})
		}
	}
	if len(compactions) != 1 {
		t.Fatalf("compactions = %#v", compactions)
	}
	if before, after := compactions[0][0], compactions[0][1]; before <= after || after <= 0 {
		t.Fatalf("compaction sizes = %d -> %d", before, after)
	}
	if len(provider.contexts) < 2 {
		t.Fatalf("provider calls = %d", len(provider.contexts))
	}
	// The first call is the summarizer: no tools, no cache write.
	if provider.options[0].CacheRetention != ai.CacheNone || len(provider.contexts[0].Tools) != 0 {
		t.Fatalf("summarization options = %#v", provider.options[0])
	}
	prompt, _ := provider.contexts[0].Messages[0].(ai.UserMessage)
	if text, _ := prompt.Content.Text(); !strings.Contains(text, "<conversation>") {
		t.Fatalf("summarization prompt = %q", text)
	}
	// The run itself starts from the compacted history plus the new prompt.
	runContext := provider.contexts[1]
	if len(runContext.Messages) != 4 {
		t.Fatalf("run context = %d messages", len(runContext.Messages))
	}
	summary, ok := runContext.Messages[0].(ai.UserMessage)
	if !ok || !summary.Synthetic {
		t.Fatalf("first run message = %#v", runContext.Messages[0])
	}
	// The checkpoint plus the kept tail, then the run's own prompt and reply.
	saved, err := store.ReadTranscript("ses_compact")
	if err != nil || len(saved) != 5 {
		t.Fatalf("saved transcript = %d messages, %v", len(saved), err)
	}
}

func TestChatCompactRewritesAndPersistsTheConversation(t *testing.T) {
	root := t.TempDir()
	store := storage.New(filepath.Join(root, "config"))
	history := make([]ai.Message, 0, 8)
	for index := 0; index < 8; index++ {
		history = append(history, ai.NewUserMessage(strings.Repeat("y", 4000), time.Unix(int64(index), 0)))
	}
	if err := store.WriteTranscript("ses_manual", history); err != nil {
		t.Fatal(err)
	}
	provider := &compactingProvider{}
	chat, err := New(Options{
		Root: root, SessionID: "ses_manual", Provider: provider, Model: ai.Model{ID: "test"},
		Sessions:   store,
		Compaction: agent.CompactionSettings{Enabled: true, ReserveTokens: 2048, KeepRecentTokens: 2000},
	})
	if err != nil {
		t.Fatal(err)
	}
	compacted, done, err := chat.Compact(context.Background())
	if err != nil || !done {
		t.Fatalf("compact = %v %v", done, err)
	}
	if len(compacted) != 3 || len(chat.Messages()) != 3 {
		t.Fatalf("compacted = %#v", compacted)
	}
	saved, err := store.ReadTranscript("ses_manual")
	if err != nil || len(saved) != 3 {
		t.Fatalf("saved = %#v, %v", saved, err)
	}
}

// TestChangeDirectoryMovesTheToolsAndTellsTheModelOnce covers the parts of a
// working-directory change that are easy to get wrong: the coding tools must
// follow it, and the model must be told once, not every turn.
func TestChangeDirectoryMovesTheToolsAndTellsTheModelOnce(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	provider := &appProvider{response: appAssistant("done")}
	environments := 0
	chat, err := New(Options{
		Root: first, Provider: provider, Model: ai.Model{ID: "test"},
		Environment: func(root string) (Environment, error) {
			environments++
			return Environment{
				Fingerprint: "dir:" + root,
				Block:       "The working directory is now " + root + ". These instructions supersede the previous project's.",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The opening turn already carries the environment, so nothing is appended.
	if notice, appended := chat.environmentNotice(); appended {
		t.Fatalf("first turn appended %#v", notice)
	}
	if err := chat.ChangeDirectory(second); err != nil {
		t.Fatal(err)
	}
	if chat.Root() != second {
		t.Fatalf("root = %q, want %q", chat.Root(), second)
	}
	notice, appended := chat.environmentNotice()
	if !appended {
		t.Fatal("a directory change was not reported")
	}
	text, _ := notice.(ai.UserMessage).Content.Text()
	if !strings.Contains(text, second) || !strings.Contains(text, "supersede") {
		t.Fatalf("notice = %q", text)
	}
	if userMessage, ok := notice.(ai.UserMessage); !ok || !userMessage.Synthetic {
		t.Fatalf("notice is not a synthetic message: %#v", notice)
	}
	// The same environment is not announced twice.
	if notice, appended := chat.environmentNotice(); appended {
		t.Fatalf("unchanged environment appended %#v", notice)
	}
	if environments < 3 {
		t.Fatalf("environment consulted %d times", environments)
	}

	// The write tool now resolves against the new directory.
	chat.mu.Lock()
	available, toolsErr := chat.availableToolsLocked()
	chat.mu.Unlock()
	if toolsErr != nil {
		t.Fatal(toolsErr)
	}
	written := false
	for _, tool := range available {
		if tool.Definition().Name != "write" {
			continue
		}
		if _, err := tool.Execute(context.Background(), ai.ToolCall{
			Name: "write", Arguments: map[string]any{"path": "moved.txt", "content": "here"},
		}, nil); err != nil {
			t.Fatalf("write after the move: %v", err)
		}
		written = true
	}
	if !written {
		t.Fatal("no write tool")
	}
	if _, err := os.Stat(filepath.Join(second, "moved.txt")); err != nil {
		t.Fatalf("the write did not land in the new directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first, "moved.txt")); err == nil {
		t.Fatal("the write landed in the old directory")
	}
}

func TestChangeDirectoryRejectsMissingAndBusyDirectories(t *testing.T) {
	root := t.TempDir()
	chat, err := New(Options{Root: root, Provider: &appProvider{response: appAssistant("done")}, Model: ai.Model{ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := chat.ChangeDirectory(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := chat.ChangeDirectory(file); err == nil {
		t.Fatal("a file was accepted as a directory")
	}
	if chat.Root() != root {
		t.Fatalf("root moved to %q", chat.Root())
	}
}
