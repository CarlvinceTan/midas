package openai

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// TestCompletionPayloadPrefixIsAppendOnly guards the property provider prompt
// caching depends on: the request for the next turn must repeat the previous
// turn's messages byte for byte so the cached prefix keeps growing, and the tool
// schemas and system prompt must not move between turns.
func TestCompletionPayloadPrefixIsAppendOnly(t *testing.T) {
	model := ai.Model{ID: "deepseek/deepseek-v4.1-flash", Provider: "command-code", API: "openai-completions"}
	tools := []ai.Tool{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}
	options := ai.StreamOptions{SessionID: "ses_test", CacheRetention: ai.CacheShort}

	turnOne := ai.Context{
		SystemPrompt: "You are Midas.",
		Tools:        tools,
		Messages: []ai.Message{
			ai.NewUserMessage("remember this document", time.Unix(1, 0)),
			textAssistant("noted"),
		},
	}
	turnTwo := turnOne
	turnTwo.Messages = append(append([]ai.Message(nil), turnOne.Messages...), ai.NewUserMessage("follow up", time.Unix(2, 0)))

	first := completionMessages(t, model, turnOne, options)
	second := completionMessages(t, model, turnTwo, options)
	if len(second) <= len(first) {
		t.Fatalf("second payload has %d messages, first %d", len(second), len(first))
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second[:len(first)])
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("the replayed prefix changed between turns.\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}

	firstPayload := completionsPayload(t, model, turnOne, options)
	secondPayload := completionsPayload(t, model, turnTwo, options)
	for _, key := range []string{"tools", "model", "reasoning_effort"} {
		before, _ := json.Marshal(firstPayload[key])
		after, _ := json.Marshal(secondPayload[key])
		if string(before) != string(after) {
			t.Fatalf("%s changed between turns:\n%s\n%s", key, before, after)
		}
	}
}

func TestCompletionsRequestCarriesTheSessionCacheKey(t *testing.T) {
	model := ai.Model{ID: "deepseek/deepseek-v4.1-flash", Provider: "command-code", API: "openai-completions"}
	payload, err := buildCompletionsRequest(model, ai.Context{Messages: []ai.Message{ai.NewUserMessage("hi", time.Unix(1, 0))}}, ai.StreamOptions{SessionID: "ses_1234567890"})
	if err != nil {
		t.Fatal(err)
	}
	if payload["prompt_cache_key"] != "ses_1234567890" {
		t.Fatalf("prompt_cache_key = %#v", payload["prompt_cache_key"])
	}
	// Nothing is pinned when the caller opts out of caching.
	payload, err = buildCompletionsRequest(model, ai.Context{Messages: []ai.Message{ai.NewUserMessage("hi", time.Unix(1, 0))}}, ai.StreamOptions{SessionID: "ses_1", CacheRetention: ai.CacheNone})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["prompt_cache_key"]; exists {
		t.Fatal("prompt_cache_key was sent with cache retention disabled")
	}
	// Long session ids are clamped to OpenAI's 64-character limit.
	long := strings.Repeat("s", 80)
	payload, err = buildCompletionsRequest(model, ai.Context{Messages: []ai.Message{ai.NewUserMessage("hi", time.Unix(1, 0))}}, ai.StreamOptions{SessionID: long})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := payload["prompt_cache_key"].(string)
	if len([]rune(key)) != 64 {
		t.Fatalf("clamped key = %q (%d runes)", key, len([]rune(key)))
	}
}

func textAssistant(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(text)},
		StopReason: ai.StopComplete, Timestamp: ai.UnixMillis(time.Unix(2, 0)),
	}
}

func completionMessages(t *testing.T, model ai.Model, chatContext ai.Context, options ai.StreamOptions) []any {
	t.Helper()
	messages, ok := completionsPayload(t, model, chatContext, options)["messages"].([]any)
	if !ok {
		t.Fatalf("payload has no messages: %#v", messages)
	}
	return messages
}

func completionsPayload(t *testing.T, model ai.Model, chatContext ai.Context, options ai.StreamOptions) map[string]any {
	t.Helper()
	payload, err := buildCompletionsRequest(model, chatContext, options)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
