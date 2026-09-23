package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

type recordingStatusStreamer struct {
	model   ai.Model
	context ai.Context
	options ai.StreamOptions
}

func (s *recordingStatusStreamer) Stream(_ context.Context, model ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	s.model, s.context, s.options = model, input, options
	stream := ai.NewAssistantStream()
	message := ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("tests running")}, StopReason: ai.StopComplete}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	return stream, nil
}

func TestEfficientStatusModelStaysOnActiveProviderAndRejectsLargeFallback(t *testing.T) {
	active := ai.Model{Provider: "openrouter", ID: "vendor/large-model"}
	models := []ai.Model{
		{Provider: "other", ID: "gpt-nano", Cost: ai.ModelCost{Input: 0.01, Output: 0.01}},
		{Provider: "openrouter", ID: "vendor/large-model", Cost: ai.ModelCost{Input: 0.001, Output: 0.001}},
		{Provider: "openrouter", ID: "google/gemini-flash-lite", Cost: ai.ModelCost{Input: 0.1, Output: 0.4}},
		{Provider: "openrouter", ID: "openai/gpt-nano", Cost: ai.ModelCost{Input: 0.2, Output: 0.5}},
	}
	candidates := statusModelCandidates(active, models, nil)
	if len(candidates) == 0 || candidates[0].Provider != "openrouter" || candidates[0].ID != "openai/gpt-nano" {
		t.Fatalf("status candidates = %#v", candidates)
	}
	if candidates := statusModelCandidates(active, models[1:2], nil); len(candidates) != 0 {
		t.Fatalf("a large model was accepted as a status fallback: %#v", candidates)
	}
	if statusModelAcceptable(ai.Model{Provider: "minimax", ID: "MiniMax-M2"}) {
		t.Fatal("MiniMax was mistaken for a mini status model")
	}
}

func TestGenerateStatusTextUsesBoundedNonReasoningRequest(t *testing.T) {
	provider := &recordingStatusStreamer{}
	model := ai.Model{Provider: "fake", ID: "tiny-flash", API: "openai-completions"}
	got, err := generateStatusText(context.Background(), provider, model, "key", "Goal: compact header", 3)
	if err != nil || got != "tests running" {
		t.Fatalf("generated status = %q, %v", got, err)
	}
	if provider.model.ID != model.ID || provider.options.APIKey != "key" || provider.options.MaxTokens != 16 || provider.options.Reasoning != ai.ThinkingOff || provider.options.MaxRetries != 1 {
		t.Fatalf("status request was not bounded: model=%#v options=%#v", provider.model, provider.options)
	}
	if len(provider.context.Tools) != 0 || !strings.Contains(provider.context.SystemPrompt, "summarize what is happening") || len(provider.context.Messages) != 1 {
		t.Fatalf("status context = %#v", provider.context)
	}
	user := provider.context.Messages[0].(ai.UserMessage)
	prompt, _ := user.Content.Text()
	if !strings.Contains(prompt, "at most 3 words") || !strings.Contains(prompt, "Use no punctuation") {
		t.Fatalf("status prompt = %q", prompt)
	}
}
