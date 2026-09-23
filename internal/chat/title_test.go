package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/CarlvinceTan/midas/internal/profiles"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

type recordingTitleStreamer struct {
	model   ai.Model
	context ai.Context
	options ai.StreamOptions
	calls   int
}

func (s *recordingTitleStreamer) Stream(_ context.Context, model ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	s.calls++
	s.model, s.context, s.options = model, input, options
	stream := ai.NewAssistantStream()
	message := ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(`"Preparation for deployment"`)}, StopReason: ai.StopComplete}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	return stream, nil
}

func TestTitleGeneratorUsesTheAgentsModelAndBoundedRequest(t *testing.T) {
	provider := &recordingTitleStreamer{}
	model := ai.Model{Provider: "fake", ID: "big-model", API: "openai-completions"}
	profile, err := profiles.ResolveProfile("title")
	if err != nil {
		t.Fatal(err)
	}
	got, err := generateTitleText(context.Background(), provider, model, "key", profile.SystemPrompt, "Original request: ship v0.3.0", 5)
	if err != nil || got != `"Preparation for deployment"` {
		t.Fatalf("generated title = %q, %v", got, err)
	}
	if provider.options.APIKey != "key" {
		t.Fatalf("title request lost its credential: %#v", provider.options)
	}
	if provider.model.ID != "big-model" {
		t.Fatalf("title model = %#v", provider.model)
	}
	// Helpers never see tools, reasoning, or retries.
	if len(provider.context.Tools) != 0 || provider.options.Reasoning != ai.ThinkingOff || provider.options.MaxRetries != 1 {
		t.Fatalf("title request was not bounded: %#v", provider.options)
	}
	if !strings.Contains(provider.context.SystemPrompt, "name the overarching goal") {
		t.Fatalf("title system prompt = %q", provider.context.SystemPrompt)
	}
	user := provider.context.Messages[0].(ai.UserMessage)
	prompt, _ := user.Content.Text()
	if !strings.Contains(prompt, "at most 5 words") || !strings.Contains(prompt, "never the current activity") || !strings.Contains(prompt, "Original request: ship v0.3.0") {
		t.Fatalf("title prompt = %q", prompt)
	}
}

func TestTitleGeneratorDeclinesWithoutAModel(t *testing.T) {
	called := false
	generator := NewTitleGenerator(
		func() string { return "" },
		func() []ai.Model { called = true; return nil },
		func(string) string { return "" },
	)
	if _, err := generator(context.Background(), "Original request: anything", 5); err == nil {
		t.Fatal("title generation ran without a model")
	}
	if called {
		t.Fatal("title generator resolved a catalog without a model reference")
	}
}
