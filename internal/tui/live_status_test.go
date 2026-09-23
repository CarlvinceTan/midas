package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// submitProbeChat starts one fake run so the live indicator has an active entry.
func submitProbeChat(t *testing.T) (*Chat, []*fakeChatRun) {
	t.Helper()
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{Backend: backend, Model: "deepseek-v4.1-flash", SessionID: "ses_live"})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("fix the parser")
	_, runs := backend.snapshot()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	return chat, runs
}

func waitForPlain(t *testing.T, chat *Chat, want string) {
	t.Helper()
	waitFor(t, func() bool {
		return strings.Contains(stripANSI(strings.Join(chat.Render(72), "\n")), want)
	})
}

func waitIdle(t *testing.T, chat *Chat) {
	t.Helper()
	waitFor(t, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return !chat.busy
	})
}

func TestLiveIndicatorShowsRunningToolInTranscript(t *testing.T) {
	chat, runs := submitProbeChat(t)
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "1", ToolName: "bash", Arguments: map[string]any{"command": "go test ./..."}})
	waitForPlain(t, chat, "Running `go test ./...`")
	plain := stripANSI(strings.Join(chat.Render(72), "\n"))
	if !strings.Contains(plain, "Running `go test ./...`") {
		t.Fatalf("live action row missing:\n%s", plain)
	}
	if !strings.Contains(plain, spinnerFrames[0]) {
		t.Fatalf("live action row has no spinner:\n%s", plain)
	}
}

func TestLiveIndicatorReportsWritingAndFinishedChain(t *testing.T) {
	chat, runs := submitProbeChat(t)
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "1", ToolName: "read", Arguments: map[string]any{"path": "README.md"}})
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: "1", ToolName: "read"})
	runs[0].Push(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{Type: ai.EventTextDelta, Delta: "hello there"}})
	waitForPlain(t, chat, "Writing 1 line")
	plain := stripANSI(strings.Join(chat.Render(72), "\n"))
	if !strings.Contains(plain, "Writing 1 line") {
		t.Fatalf("writing row missing:\n%s", plain)
	}
	if !strings.Contains(plain, "✓ Read README.md") {
		t.Fatalf("finished chain should stay visible:\n%s", plain)
	}
}

func TestLiveIndicatorDisappearsWhenIdle(t *testing.T) {
	chat, runs := submitProbeChat(t)
	runs[0].Push(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "1", ToolName: "bash", Arguments: map[string]any{"command": "ls"}})
	waitForPlain(t, chat, "Running `ls`")
	runs[0].Push(agent.Event{Type: agent.EventAgentEnd})
	waitIdle(t, chat)
	plain := stripANSI(strings.Join(chat.Render(72), "\n"))
	if strings.Contains(plain, "Running `ls`") {
		t.Fatalf("idle transcript still shows a live row:\n%s", plain)
	}
}

func TestAdvanceEasesRateAndSpinsOnlyWhileBusy(t *testing.T) {
	chat, _ := submitProbeChat(t)
	if !chat.Advance(time.Now()) {
		t.Fatal("a busy chat should request a repaint for the spinner")
	}
	chat.mu.Lock()
	frame := chat.spinnerFrame
	chat.mu.Unlock()
	chat.Advance(time.Now().Add(2 * liveSpinnerInterval))
	chat.mu.Lock()
	next := chat.spinnerFrame
	busy := chat.busy
	chat.mu.Unlock()
	if !busy || next == frame {
		t.Fatalf("spinner did not advance: %d -> %d", frame, next)
	}
}

func TestFormatLiveSecondsUsesWholeSeconds(t *testing.T) {
	cases := map[int64]string{0: "0s", 999: "0s", 1000: "1s", 125_000: "2m 5s", 7_200_000: "2h"}
	for milliseconds, want := range cases {
		if got := formatLiveSeconds(milliseconds); got != want {
			t.Fatalf("formatLiveSeconds(%d) = %q, want %q", milliseconds, got, want)
		}
	}
}
