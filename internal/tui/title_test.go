package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHeuristicTitleNamesTheSessionImmediately(t *testing.T) {
	for _, test := range []struct {
		prompt  string
		maximum int
		want    string
	}{
		{prompt: "fix the slash routing bug", maximum: 8, want: "Fix the slash routing bug"},
		{prompt: "  \n\n  wire the title agent into the header\n", maximum: 8, want: "Wire the title agent into the header"},
		{prompt: "## Summarize the board with a model and more words", maximum: 4, want: "Summarize the board with"},
		{prompt: "`code` and **no** markdown markers", maximum: 8, want: "Code and no markdown markers"},
		{prompt: "", maximum: 8, want: ""},
	} {
		if got := heuristicTitle(test.prompt, test.maximum); got != test.want {
			t.Fatalf("heuristicTitle(%q, %d) = %q, want %q", test.prompt, test.maximum, got, test.want)
		}
	}
}

func TestGeneratedTitleCleanupIsDeterministic(t *testing.T) {
	if got, want := cleanGeneratedTitle(`  "Preparation for deployment for v0.3.0."  `, 8), "Preparation for deployment for v0.3.0"; got != want {
		t.Fatalf("clean title = %q, want %q", got, want)
	}
	if got, want := cleanGeneratedTitle("First line\nSecond line", 8), "First line"; got != want {
		t.Fatalf("multi-line title = %q, want %q", got, want)
	}
	if got, want := cleanGeneratedTitle("one two three four five six seven eight nine", 4), "one two three four"; got != want {
		t.Fatalf("capped title = %q, want %q", got, want)
	}
	long := cleanGeneratedTitle(strings.Repeat("word ", 40), 20)
	if runes := len([]rune(long)); runes > maxTitleRunes {
		t.Fatalf("long title = %d runes", runes)
	}
}

func TestSubmittingAPromptGeneratesAndPersistsASessionTitle(t *testing.T) {
	requests := make(chan string, 1)
	maximums := make(chan int, 1)
	applied := make(chan string, 2)
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test", TitleMaxWords: 6,
		TitleGenerator: func(_ context.Context, source string, maximum int) (string, error) {
			requests <- source
			maximums <- maximum
			return `"Preparation for deployment for v0.3.0"`, nil
		},
		OnTitle: func(title string) { applied <- title },
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("prepare the v0.3.0 deployment and tag the release")
	// The header never sits on the placeholder while the helper model works.
	if header := stripANSI(strings.Join(chat.renderHeader(72), "\n")); !strings.Contains(header, "Prepare the v0.3.0 deployment and") {
		t.Fatalf("heuristic title missing from the header: %q", header)
	}
	select {
	case source := <-requests:
		if !strings.Contains(source, "prepare the v0.3.0 deployment") {
			t.Fatalf("title source = %q", source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title generator was not called")
	}
	if got := <-maximums; got != 6 {
		t.Fatalf("title maximum = %d, want 6", got)
	}
	select {
	case title := <-applied:
		if title != "Preparation for deployment for v0.3.0" {
			t.Fatalf("generated title = %q", title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generated title was not reported")
	}
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return strings.Contains(chat.title, "Preparation")
	})
	header := stripANSI(strings.Join(chat.renderHeader(72), "\n"))
	if !strings.Contains(header, "Preparation for deployment for v0.3.0") {
		t.Fatalf("generated title missing from the header: %q", header)
	}
	if strings.Contains(header, "New Session") {
		t.Fatalf("header kept the placeholder: %q", header)
	}
}

func TestCommandSubmissionsDoNotGenerateTitles(t *testing.T) {
	calls := make(chan string, 2)
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		TitleGenerator: func(_ context.Context, source string, _ int) (string, error) {
			calls <- source
			return "Generated", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("/stats")
	// A command never starts a generation, and the flag is set before the goroutine
	// is spawned, so the state after submit is proof rather than a timing window.
	chat.mu.Lock()
	generating := chat.titleGenerating
	chat.mu.Unlock()
	if generating {
		t.Fatal("a slash command started title generation")
	}
	select {
	case source := <-calls:
		t.Fatalf("a command generated a title for %q", source)
	default:
	}
	// Slash text that names no command is an ordinary prompt, so it titles the
	// session like any other request.
	chat.submit("/stats extra words are a prompt")
	select {
	case source := <-calls:
		if !strings.Contains(source, "/stats extra words are a prompt") {
			t.Fatalf("title source = %q", source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt-like slash text did not generate a title")
	}
}

func TestTitleGenerationIsSerializedPerSession(t *testing.T) {
	release := make(chan struct{})
	calls := make(chan string, 4)
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		TitleGenerator: func(_ context.Context, source string, _ int) (string, error) {
			calls <- source
			<-release
			return "Generated", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.applyHeuristicTitle("first request")
	chat.maybeGenerateTitle("first request")
	chat.maybeGenerateTitle("second request")
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("first title generation did not start")
	}
	// The second request is dropped rather than queued, and the first is still
	// blocked inside the generator, so this state is deterministic.
	chat.mu.Lock()
	generation := chat.titleGeneration
	chat.mu.Unlock()
	if generation != 1 {
		t.Fatalf("concurrent title generations started: %d", generation)
	}
	select {
	case source := <-calls:
		t.Fatalf("concurrent title generation started for %q", source)
	default:
	}
	close(release)
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.titleGenerating
	})
}

func TestSetTitleMaxWordsClampsAndDrivesTheBudget(t *testing.T) {
	maximums := make(chan int, 1)
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test",
		TitleGenerator: func(_ context.Context, _ string, maximum int) (string, error) {
			maximums <- maximum
			return "Generated", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.SetTitleMaxWords(50)
	if chat.titleMaxWords != 20 {
		t.Fatalf("clamped budget = %d", chat.titleMaxWords)
	}
	chat.SetTitleMaxWords(3)
	chat.maybeGenerateTitle("a request")
	select {
	case got := <-maximums:
		if got != 3 {
			t.Fatalf("title maximum = %d, want 3", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title generator was not called")
	}
}
