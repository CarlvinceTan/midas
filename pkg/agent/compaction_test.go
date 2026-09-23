package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func compactionMessages(count, charsPerMessage int) []ai.Message {
	messages := make([]ai.Message, 0, count)
	for index := 0; index < count; index++ {
		text := strings.Repeat("x", charsPerMessage)
		if index%2 == 0 {
			messages = append(messages, ai.NewUserMessage(text, time.Unix(int64(index), 0)))
			continue
		}
		messages = append(messages, assistantMessageWithUsage(text, 0))
	}
	return messages
}

func assistantMessageWithUsage(text string, tokens int) ai.AssistantMessage {
	message := ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(text)}, StopReason: ai.StopComplete,
		Provider: "test", Model: "test", Timestamp: 1,
	}
	if tokens > 0 {
		message.Usage = ai.Usage{Input: tokens, TotalTokens: tokens}
	}
	return message
}

func TestShouldCompactUsesReserveTokens(t *testing.T) {
	settings := DefaultCompactionSettings()
	if ShouldCompact(100, 200_000, settings) {
		t.Fatal("a small context should not compact")
	}
	if !ShouldCompact(200_000-settings.ReserveTokens+1, 200_000, settings) {
		t.Fatal("a context past the reserve should compact")
	}
	settings.Enabled = false
	if ShouldCompact(200_000, 200_000, settings) {
		t.Fatal("disabled compaction never runs")
	}
	if ShouldCompact(200_000, 0, DefaultCompactionSettings()) {
		t.Fatal("an unknown context window never compacts")
	}
}

func TestContextTokensUsesLastUsagePlusTrailingEstimate(t *testing.T) {
	messages := []ai.Message{
		ai.NewUserMessage(strings.Repeat("u", 400), time.Unix(1, 0)),
		assistantMessageWithUsage("done", 1200),
		ai.NewUserMessage(strings.Repeat("v", 400), time.Unix(2, 0)),
	}
	if got := ContextTokens(messages); got != 1200+100 {
		t.Fatalf("context tokens = %d, want %d", got, 1300)
	}

	// Without any reported usage the estimate covers every message.
	estimated := ContextTokens([]ai.Message{ai.NewUserMessage(strings.Repeat("u", 400), time.Unix(1, 0))})
	if estimated != 100 {
		t.Fatalf("estimated tokens = %d", estimated)
	}

	// Aborted turns carry no usable usage, so they are skipped.
	aborted := assistantMessageWithUsage("stopped", 900)
	aborted.StopReason = ai.StopAborted
	withAborted := []ai.Message{ai.NewUserMessage(strings.Repeat("u", 400), time.Unix(1, 0)), aborted}
	// The aborted turn contributes no usage, so both messages are estimated.
	if got := ContextTokens(withAborted); got != 102 {
		t.Fatalf("context tokens with an aborted turn = %d", got)
	}
}

func TestPlanCompactionKeepsTheRecentTailAndNeverCutsAToolResult(t *testing.T) {
	messages := compactionMessages(10, 4000) // 10 messages of ~1000 tokens each
	summarize, keep, ok := PlanCompaction(messages, 3000)
	if !ok {
		t.Fatal("expected a compaction plan")
	}
	if len(keep) != 3 || len(summarize) != 7 {
		t.Fatalf("plan = %d summarized, %d kept", len(summarize), len(keep))
	}

	// A cut landing on a tool result moves forward so the kept tail starts with
	// the tool call that produced it.
	withToolResult := append(compactionMessages(4, 4000),
		ai.NewUserMessage(strings.Repeat("c", 200), time.Unix(9, 0)),
		ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "1", ToolName: "read", Content: []ai.Content{ai.NewText(strings.Repeat("r", 8000))}},
		ai.NewUserMessage(strings.Repeat("d", 200), time.Unix(10, 0)),
	)
	summarize, keep, ok = PlanCompaction(withToolResult, 2600)
	if !ok || len(keep) == 0 {
		t.Fatalf("plan = %d summarized, %d kept", len(summarize), len(keep))
	}
	if _, isToolResult := keep[0].(ai.ToolResultMessage); isToolResult {
		t.Fatalf("kept tail starts with a tool result: %#v", keep[0])
	}

	// A conversation that fits the recent budget is left alone.
	if _, _, ok := PlanCompaction(compactionMessages(2, 40), 20_000); ok {
		t.Fatal("a short conversation should not compact")
	}
}

func TestApplyCompactionPutsTheSummaryFirst(t *testing.T) {
	keep := []ai.Message{ai.NewUserMessage("recent", time.Unix(5, 0))}
	compacted := ApplyCompaction("## Goal\ndo the thing", keep, time.Unix(6, 0))
	if len(compacted) != 2 {
		t.Fatalf("compacted = %#v", compacted)
	}
	summary, ok := compacted[0].(ai.UserMessage)
	if !ok || !summary.Synthetic {
		t.Fatalf("summary message = %#v", compacted[0])
	}
	text, _ := summary.Content.Text()
	if !strings.HasPrefix(text, summaryMarker) || !strings.Contains(text, "do the thing") {
		t.Fatalf("summary text = %q", text)
	}
	if got := PreviousSummary(compacted); !strings.Contains(got, "do the thing") {
		t.Fatalf("previous summary = %q", got)
	}
	if PreviousSummary(keep) != "" {
		t.Fatal("a conversation without a checkpoint reported one")
	}
}

type summaryProvider struct {
	contexts []ai.Context
	options  []ai.StreamOptions
	message  ai.AssistantMessage
	err      error
}

func (p *summaryProvider) ID() string                                 { return "summary" }
func (p *summaryProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }
func (p *summaryProvider) Stream(_ context.Context, _ ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	p.contexts = append(p.contexts, input)
	p.options = append(p.options, options)
	if p.err != nil {
		return nil, p.err
	}
	stream := ai.NewAssistantStream()
	message := p.message
	partial := message
	partial.Content = nil
	partial.StopReason = ai.StopPending
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: message.StopReason, Message: &message})
	return stream, nil
}

func TestCompactSummarizesWithoutCacheAndAppliesTheCheckpoint(t *testing.T) {
	provider := &summaryProvider{message: assistantMessageWithUsage("## Goal\nship it", 0)}
	messages := compactionMessages(10, 4000)
	settings := CompactionSettings{Enabled: true, ReserveTokens: 2048, KeepRecentTokens: 3000}
	compacted, done, err := Compact(context.Background(), provider, ai.Model{ID: "test", Provider: "test", Reasoning: true}, ai.StreamOptions{APIKey: "key", SessionID: "ses_1", Reasoning: ai.ThinkingHigh}, messages, settings)
	if err != nil || !done {
		t.Fatalf("compact = %v %v", done, err)
	}
	if len(provider.options) != 1 {
		t.Fatalf("summarization calls = %d", len(provider.options))
	}
	options := provider.options[0]
	if options.CacheRetention != ai.CacheNone || options.MaxTokens != settings.ReserveTokens || options.APIKey != "key" {
		t.Fatalf("summarization options = %#v", options)
	}
	if options.Reasoning != ai.ThinkingHigh {
		t.Fatalf("reasoning = %q", options.Reasoning)
	}
	prompt := provider.contexts[0].Messages[0].(ai.UserMessage)
	text, _ := prompt.Content.Text()
	if !strings.Contains(text, "<conversation>") || !strings.Contains(text, "## Goal") {
		t.Fatalf("summarization prompt = %q", text)
	}
	// Seven messages were replaced by one checkpoint and three kept.
	if len(compacted) != 4 {
		t.Fatalf("compacted length = %d", len(compacted))
	}
	if summary := PreviousSummary(compacted); !strings.Contains(summary, "ship it") {
		t.Fatalf("checkpoint = %q", summary)
	}

	// A second compaction updates the existing checkpoint instead of restating it.
	provider.contexts = nil
	provider.message = assistantMessageWithUsage("## Goal\nship it still", 0)
	if _, done, err := Compact(context.Background(), provider, ai.Model{ID: "test"}, ai.StreamOptions{}, compacted, settings); err != nil || !done {
		t.Fatalf("second compact = %v %v", done, err)
	}
	update := provider.contexts[0].Messages[0].(ai.UserMessage)
	updateText, _ := update.Content.Text()
	if !strings.Contains(updateText, "<previous-summary>") || !strings.Contains(updateText, "NEW conversation messages") {
		t.Fatalf("update prompt = %q", updateText)
	}
}

func TestCompactSkipsShortConversationsAndSurfacesFailures(t *testing.T) {
	provider := &summaryProvider{message: assistantMessageWithUsage("summary", 0)}
	if _, done, err := Compact(context.Background(), provider, ai.Model{ID: "test"}, ai.StreamOptions{}, []ai.Message{ai.NewUserMessage("hi", time.Unix(1, 0))}, DefaultCompactionSettings()); err != nil || done {
		t.Fatalf("short conversation compacted: %v %v", done, err)
	}

	// A truncated summary must never become a checkpoint.
	truncated := assistantMessageWithUsage("partial", 0)
	truncated.StopReason = ai.StopLength
	failing := &summaryProvider{message: truncated}
	messages := compactionMessages(10, 4000)
	if _, done, err := Compact(context.Background(), failing, ai.Model{ID: "test"}, ai.StreamOptions{}, messages, CompactionSettings{Enabled: true, ReserveTokens: 2048, KeepRecentTokens: 3000}); err == nil || done {
		t.Fatalf("truncated summary accepted: %v %v", done, err)
	}
}
