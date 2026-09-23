package ai

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUserContentJSONMatchesWireShape(t *testing.T) {
	t.Parallel()

	text, err := json.Marshal(TextUserContent("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(text), `"hello"`; got != want {
		t.Fatalf("text JSON = %s, want %s", got, want)
	}

	parts, err := MultipartUserContent(NewText("look"), NewImage("AAAA", "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `[{"type":"text","text":"look"},{"type":"image","data":"AAAA","mimeType":"image/png"}]`; got != want {
		t.Fatalf("multipart JSON = %s, want %s", got, want)
	}
}

func TestMultipartUserContentRejectsAssistantOnlyKinds(t *testing.T) {
	t.Parallel()

	if _, err := MultipartUserContent(NewThinking("secret")); err == nil {
		t.Fatal("expected thinking content to be rejected")
	}
	if _, err := MultipartUserContent(NewToolCall("call", "read", nil)); err == nil {
		t.Fatal("expected tool call content to be rejected")
	}
}

func TestNewUserMessageUsesUnixMilliseconds(t *testing.T) {
	t.Parallel()

	at := time.Unix(123, 456_000_000)
	message := NewUserMessage("hello", at)
	if message.Role != RoleUser || message.Timestamp != 123456 {
		t.Fatalf("message = %#v", message)
	}
}

func TestUsageRecalculateTotals(t *testing.T) {
	t.Parallel()

	usage := Usage{
		Input:      10,
		Output:     4,
		CacheRead:  3,
		CacheWrite: 2,
		Cost: UsageCost{
			Input: 1, Output: 2, CacheRead: 0.25, CacheWrite: 0.5,
		},
	}
	usage.RecalculateTotals()
	if usage.TotalTokens != 19 || usage.Cost.Total != 3.75 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestCalculateCostUsesHighestMatchingTierAndLongCacheRate(t *testing.T) {
	t.Parallel()

	longWrite := 10
	usage := Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 40, CacheWrite1H: &longWrite}
	model := Model{Cost: ModelCost{
		Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25,
		Tiers: []ModelCostTier{
			{Input: 2, Output: 4, CacheRead: 0.2, CacheWrite: 2.5, InputTokensAbove: 50},
			{Input: 3, Output: 6, CacheRead: 0.3, CacheWrite: 3.75, InputTokensAbove: 150},
		},
	}}
	cost := CalculateCost(model, &usage)
	want := (3*100 + 6*20 + 0.3*30 + 3.75*30 + 3*2*10) / 1_000_000
	if cost.Total != want {
		t.Fatalf("total cost = %.12f, want %.12f", cost.Total, want)
	}
}

func TestCloneAssistantMessageCopiesToolArguments(t *testing.T) {
	t.Parallel()

	call := NewToolCall("id", "tool", map[string]any{"nested": map[string]any{"value": "before"}})
	message := AssistantMessage{Content: []Content{&call}}
	clone := CloneAssistantMessage(message)
	call.Arguments["nested"].(map[string]any)["value"] = "after"
	got := clone.Content[0].(*ToolCall).Arguments["nested"].(map[string]any)["value"]
	if got != "before" {
		t.Fatalf("clone changed to %q", got)
	}
}
