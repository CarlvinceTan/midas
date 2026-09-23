package chat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/profiles"
	"github.com/CarlvinceTan/midas/internal/provider"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// NewStatusGenerator keeps the helper on the active provider so it reuses the
// credentials the user has already configured. It deliberately declines to use
// a large model; the TUI retains its local mechanical status in that case.
//
// Candidates are tried cheapest-first because a plan may exclude the smallest
// model it advertises (CommandCode, for example, rejects Claude Haiku on lower
// tiers). A model that refuses the request for plan/availability reasons is
// remembered for the session so every later status update does not retry it.
func NewStatusGenerator(currentModel func() ai.Model, models func() []ai.Model, getenv func(string) string) func(context.Context, string, int) (string, error) {
	var mu sync.Mutex
	blocked := make(map[string]bool)
	return func(ctx context.Context, source string, maximum int) (string, error) {
		mu.Lock()
		unavailable := make(map[string]bool, len(blocked))
		for key := range blocked {
			unavailable[key] = true
		}
		mu.Unlock()
		candidates := statusModelCandidates(currentModel(), models(), unavailable)
		if len(candidates) == 0 {
			return "", errors.New("no small status model is available on the active provider")
		}
		var lastErr error
		for _, selected := range candidates {
			provider, configured, key, err := provider.ConfiguredProvider(selected.Provider, selected.ID, selected.API, selected.BaseURL, getenv)
			if err != nil {
				lastErr = err
				continue
			}
			if selected.API == "" {
				selected.API = configured.API
			}
			if selected.BaseURL == "" {
				selected.BaseURL = configured.BaseURL
			}
			text, err := generateStatusText(ctx, provider, selected, key, source, maximum)
			if err != nil {
				lastErr = err
				if statusModelUnavailable(err) {
					mu.Lock()
					blocked[selected.Provider+"\x00"+selected.ID] = true
					mu.Unlock()
				}
				continue
			}
			if strings.TrimSpace(text) == "" {
				lastErr = errors.New("status model returned no text")
				continue
			}
			return text, nil
		}
		return "", lastErr
	}
}

// statusModelUnavailable reports a refusal that another request to the same
// model cannot fix (plan exclusions, missing models, removed access).
func statusModelUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"model_not_in_plan", "model not found", "not available", "does not exist", "no access to model", "not supported"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// statusModelCandidates returns the acceptable helper models on the active
// provider, cheapest and smallest first, skipping models known to be blocked.
func statusModelCandidates(active ai.Model, catalog []ai.Model, blocked map[string]bool) []ai.Model {
	providerID := strings.TrimSpace(active.Provider)
	if providerID == "" {
		return nil
	}
	candidates := make([]ai.Model, 0, len(catalog)+1)
	seen := make(map[string]bool)
	for _, candidate := range catalog {
		if candidate.Provider != providerID || !statusModelAcceptable(candidate) {
			continue
		}
		key := candidate.Provider + "\x00" + candidate.ID
		if seen[key] || blocked[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, candidate)
	}
	activeKey := active.Provider + "\x00" + active.ID
	if !seen[activeKey] && !blocked[activeKey] && statusModelAcceptable(active) {
		candidates = append(candidates, active)
	}
	slices.SortStableFunc(candidates, func(a, b ai.Model) int {
		if left, right := statusModelRank(a), statusModelRank(b); left != right {
			return cmp.Compare(left, right)
		}
		if left, right := statusModelCost(a), statusModelCost(b); left != right {
			return cmp.Compare(left, right)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return candidates
}

func statusModelAcceptable(model ai.Model) bool {
	if strings.TrimSpace(model.ID) == "" {
		return false
	}
	name := strings.ToLower(model.ID + " " + model.Name)
	for _, blocked := range []string{"embedding", "embed-", "rerank", "moderation", "image", "audio", "speech", "tts", "transcri", "realtime"} {
		if strings.Contains(name, blocked) {
			return false
		}
	}
	if len(model.Input) > 0 {
		text := false
		for _, modality := range model.Input {
			text = text || modality == ai.ModalityText
		}
		if !text {
			return false
		}
	}
	return statusModelRank(model) < 100
}

func statusModelRank(model ai.Model) int {
	name := strings.ToLower(model.ID + " " + model.Name)
	if strings.Contains(name, "mini") && !strings.Contains(name, "minimax") {
		return 3
	}
	markers := []struct {
		value string
		rank  int
	}{
		{"nano", 0}, {"flash-lite", 1}, {"flash lite", 1}, {"haiku", 2},
		{"small", 4}, {"flash", 5}, {"turbo", 6},
		{"instant", 7}, {"deepseek-chat", 8}, {"gemma", 9},
		{"1b", 10}, {"3b", 10}, {"7b", 11}, {"8b", 11}, {"14b", 12},
	}
	for _, marker := range markers {
		if strings.Contains(name, marker.value) {
			return marker.rank
		}
	}
	return 100
}

func statusModelCost(model ai.Model) float64 {
	cost := model.Cost.Input + model.Cost.Output
	if cost <= 0 {
		return 1e12
	}
	return cost
}

// generateStatusText writes the live summary shown in the header: what the
// session is doing now, in the configured word budget.
func generateStatusText(ctx context.Context, provider ai.Streamer, model ai.Model, apiKey, source string, maximum int) (string, error) {
	if maximum <= 0 {
		maximum = 6
	}
	profile, err := profiles.ResolveProfile("summary")
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf("Describe current progress toward the goal in at most %d words Use no punctuation Mention progress not tools files commands or agents Reply with only the status phrase\n\n%s", maximum, source)
	return generateHelperText(ctx, provider, model, apiKey, profile.SystemPrompt, prompt, max(16, maximum*4), 12*time.Second)
}
