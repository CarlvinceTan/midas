package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/internal/profiles"
	"github.com/CarlvinceTan/midas/internal/provider"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// generateHelperText runs one short helper completion (a session title or a live
// summary) and returns its text. Helpers are deliberately cheap and bounded: no
// tools, no reasoning, one retry, and a hard timeout.
func generateHelperText(ctx context.Context, provider ai.Streamer, model ai.Model, apiKey, system, prompt string, maxTokens int, timeout time.Duration) (string, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	temperature := 0.0
	stream, err := provider.Stream(requestContext, model, ai.Context{
		SystemPrompt: system,
		Messages:     []ai.Message{ai.NewUserMessage(prompt, time.Now())},
	}, ai.StreamOptions{
		APIKey: apiKey, Temperature: &temperature, MaxTokens: maxTokens,
		Reasoning: ai.ThinkingOff, CacheRetention: ai.CacheNone, Timeout: timeout,
		MaxRetries: 1, MaxRetryDelay: time.Second,
	})
	if err != nil {
		return "", err
	}
	message, err := stream.Result(requestContext)
	if err != nil {
		return "", err
	}
	if message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
		if message.ErrorMessage != "" {
			return "", errors.New(message.ErrorMessage)
		}
		return "", fmt.Errorf("helper model stopped with %s", message.StopReason)
	}
	parts := make([]string, 0, len(message.Content))
	for _, content := range message.Content {
		switch value := content.(type) {
		case ai.TextContent:
			parts = append(parts, value.Text)
		case *ai.TextContent:
			if value != nil {
				parts = append(parts, value.Text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, " ")), nil
}

// NewTitleGenerator names a session using the model its title agent runs with:
// a model pinned for "title" in /agents, otherwise the model the active session
// uses, which is what the /agents row shows as the inherited default.
func NewTitleGenerator(modelRef func() string, catalog func() []ai.Model, getenv func(string) string) func(context.Context, string, int) (string, error) {
	return func(ctx context.Context, source string, maximum int) (string, error) {
		ref := strings.TrimSpace(modelRef())
		if ref == "" {
			return "", errors.New("no model is available for the title agent")
		}
		profile, err := profiles.ResolveProfile("title")
		if err != nil {
			return "", err
		}
		selected := provider.ModelForRef(ref, catalog())
		provider, configured, key, err := provider.ConfiguredProvider(selected.Provider, selected.ID, selected.API, selected.BaseURL, getenv)
		if err != nil {
			return "", err
		}
		if selected.API == "" {
			selected.API = configured.API
		}
		if selected.BaseURL == "" {
			selected.BaseURL = configured.BaseURL
		}
		return generateTitleText(ctx, provider, selected, key, profile.SystemPrompt, source, maximum)
	}
}

// generateTitleText writes the session title: the overarching goal of the work
// in the configured word budget, never the current activity.
func generateTitleText(ctx context.Context, provider ai.Streamer, model ai.Model, apiKey, system, source string, maximum int) (string, error) {
	if maximum <= 0 {
		maximum = 8
	}
	prompt := fmt.Sprintf("In at most %d words, name the overarching goal or outcome of the work as a title (for example \"Preparation for deployment for v0.3.0\"). Describe the overall objective, never the current activity, tool, command, or agent. Reply with only the title, no quotes.\n\n%s", maximum, source)
	return generateHelperText(ctx, provider, model, apiKey, system, prompt, max(24, maximum*6), 20*time.Second)
}
