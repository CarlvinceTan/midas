package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestLiveContextGrowsWithStreamedContentAndReconcilesUsage(t *testing.T) {
	context := LiveContext{}
	context.Start(1000, true)
	if tokens, known := context.Read(1000, 100_000, true); !known || tokens != 1000 {
		t.Fatalf("initial context = %d, known=%v", tokens, known)
	}
	context.AddChars(400)
	if tokens, _ := context.Read(0, 100_000, true); tokens != 1100 {
		t.Fatalf("context after 400 streamed chars = %d, want 1100 (chars/4)", tokens)
	}
	// Provider input replaces the baseline; cached input counts as context.
	context.UpdateUsage(ai.Usage{Input: 900, CacheRead: 300, Output: 50})
	if tokens, _ := context.Read(0, 100_000, true); tokens != 1300 {
		t.Fatalf("context after usage = %d, want 1300 (1200 input + max(50, 100) delta)", tokens)
	}
	// Streamed output only grows the reading, never shrinks it below the max.
	// Streamed characters accumulate, so 1200 chars of content is 300 tokens.
	context.AddChars(800)
	if tokens, _ := context.Read(0, 100_000, true); tokens != 1500 {
		t.Fatalf("context after more streamed chars = %d, want 1500", tokens)
	}
	context.UpdateUsage(ai.Usage{Input: 1200, Output: 400})
	if tokens, _ := context.Read(0, 100_000, true); tokens != 1600 {
		t.Fatalf("context with provider output = %d, want 1600", tokens)
	}
}

func TestLiveContextStaysUnknownWithoutABaseline(t *testing.T) {
	context := LiveContext{}
	context.Start(0, false)
	context.AddChars(400)
	// No baseline: the caller's own reading passes through untouched.
	if tokens, known := context.Read(7000, 100_000, false); known || tokens != 7000 {
		t.Fatalf("context without a baseline = %d, known=%v", tokens, known)
	}
	// Provider input usage establishes the baseline.
	context.UpdateUsage(ai.Usage{Input: 2000})
	if tokens, known := context.Read(0, 100_000, true); !known || tokens != 2100 {
		t.Fatalf("context after input usage = %d, known=%v", tokens, known)
	}
	// A cleared estimate defers entirely to the caller's own reading.
	context.Clear()
	context.AddChars(100)
	if tokens, known := context.Read(7000, 100_000, true); !known || tokens != 7000 {
		t.Fatalf("cleared estimate = %d, known=%v, want the caller's 7000", tokens, known)
	}
}

func TestContextDisplaySamplesEstimatesAtMostOncePerSecond(t *testing.T) {
	display := ContextDisplay{}
	start := time.Unix(1000, 0)
	tokens, _ := display.Read(1000, 100_000, true, start)
	if tokens != 1000 {
		t.Fatalf("first sample = %d", tokens)
	}
	if tokens, _ := display.Read(1200, 100_000, true, start.Add(300*time.Millisecond)); tokens != 1000 {
		t.Fatalf("sample within the interval = %d, want the first sample 1000", tokens)
	}
	if tokens, _ := display.Read(1200, 100_000, true, start.Add(time.Second)); tokens != 1200 {
		t.Fatalf("sample after the interval = %d, want 1200", tokens)
	}
	// Final provider usage is not an estimate and applies immediately.
	if tokens, _ := display.Read(1500, 100_000, false, start.Add(1100*time.Millisecond)); tokens != 1500 {
		t.Fatalf("final reading = %d, want 1500", tokens)
	}
	if _, known := display.Read(0, 100_000, false, start); known {
		t.Fatal("an unknown window reported a reading")
	}
}

func TestFooterReportsContextSizeNotCumulativeUsage(t *testing.T) {
	small := ai.Usage{Input: 10_000, Output: 500, TotalTokens: 10_500, Cost: ai.UsageCost{Input: 0.01}}
	large := ai.Usage{Input: 90_000, Output: 2_000, TotalTokens: 92_000, Cost: ai.UsageCost{Input: 0.02}}
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "m", ModelName: "M", ModelContext: 100_000,
		InitialMessages: []ai.Message{
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("first")}, Usage: small, Timestamp: 1_000},
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("second")}, Usage: large, Timestamp: 2_000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	footer := strings.Join(plainLines(chat.renderFooter(100)), "\n")
	// 92.0k of a 100k window is the context; the 102.5k cumulative total must not show.
	if !strings.Contains(footer, "92.0k (92%)") {
		t.Fatalf("footer context = %q", footer)
	}
	if strings.Contains(footer, "102.5k") {
		t.Fatalf("footer reported cumulative tokens: %q", footer)
	}
	if !strings.Contains(footer, "$0.03") {
		t.Fatalf("footer cost is not cumulative: %q", footer)
	}
}

func TestFooterShowsLiveContextWhileStreaming(t *testing.T) {
	usage := ai.Usage{Input: 40_000, Output: 1_000, TotalTokens: 41_000}
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Model: "m", ModelName: "M", ModelContext: 100_000, Provider: "p",
		InitialMessages: []ai.Message{
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("first")}, Usage: usage, Timestamp: 1_000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit("next request")
	footer := strings.Join(plainLines(chat.renderFooter(100)), "\n")
	if !strings.Contains(footer, "41.0k (41%)") {
		t.Fatalf("footer before streaming = %q", footer)
	}
	// Streaming 4k characters grows the reading by ~1k tokens before usage lands.
	chat.applyEvent(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{
		Type: ai.EventTextDelta, Delta: strings.Repeat("x", 4000),
	}})
	// The rendered pair is sampled, so an immediate repaint keeps the last value.
	if footer := strings.Join(plainLines(chat.renderFooter(100)), "\n"); !strings.Contains(footer, "41.0k (41%)") {
		t.Fatalf("sampled footer = %q", footer)
	}
	chat.mu.Lock()
	tokens, ok := chat.contextReading(time.Now().Add(2 * time.Second))
	chat.mu.Unlock()
	if !ok || tokens != 42_000 {
		t.Fatalf("live context while streaming = %d, ok=%v, want 42000", tokens, ok)
	}
}
