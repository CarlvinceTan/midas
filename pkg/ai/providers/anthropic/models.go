package anthropic

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if len(p.models) > 0 {
		return append([]ai.Model(nil), p.models...), nil
	}
	if p.apiKey == "" && !p.externalAuth {
		return nil, nil
	}
	baseURL := p.baseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("anthropic: invalid base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	models := []ai.Model{}
	after := ""
	for {
		query := parsed.Query()
		query.Set("limit", "1000")
		if after != "" {
			query.Set("after_id", after)
		}
		parsed.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("x-api-key", p.apiKey)
		request.Header.Set("anthropic-version", p.version)
		mergeHeaders(request.Header, p.headers)
		client := p.httpClient
		if client == nil {
			client = http.DefaultClient
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("%s: list models: %w", p.id, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, responseError(p.id, response.StatusCode, body)
		}
		var envelope struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("anthropic: decode models: %w", err)
		}
		for _, item := range envelope.Data {
			if item.ID != "" {
				name := item.DisplayName
				if name == "" {
					name = item.ID
				}
				model := ai.Model{ID: item.ID, Name: name, Provider: p.id, API: "anthropic-messages", BaseURL: baseURL, Reasoning: true, AdaptiveThinking: SupportsAdaptiveThinking(item.ID), Input: []ai.Modality{ai.ModalityText, ai.ModalityImage}, Cost: ModelCost(item.ID)}
				if p.id == "anthropic" {
					model.PromptCache = ai.ModelPromptCache{Short: 300, Long: 3600}
				}
				models = append(models, model)
			}
		}
		if !envelope.HasMore || envelope.LastID == "" || envelope.LastID == after {
			break
		}
		after = envelope.LastID
	}
	slices.SortFunc(models, func(a, b ai.Model) int { return cmp.Compare(a.ID, b.ID) })
	return models, nil
}

// SupportsAdaptiveThinking identifies Claude models whose reasoning settings
// are independent of max_tokens, which makes a one-token cache replay safe.
func SupportsAdaptiveThinking(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return strings.Contains(id, "opus-4-6") || strings.Contains(id, "opus-4-7") || strings.Contains(id, "opus-4-8") || strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "opus-5") || strings.Contains(id, "sonnet-5") || strings.Contains(id, "fable-5") || strings.Contains(id, "mythos-5")
}

// ModelCost supplies the direct Anthropic per-million-token prices needed for
// usage accounting and cache-warming decisions.
func ModelCost(modelID string) ai.ModelCost {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.Contains(id, "haiku"):
		return ai.ModelCost{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite: 0.625}
	case strings.Contains(id, "sonnet-5"):
		return ai.ModelCost{Input: 1, Output: 5, CacheRead: 0.10, CacheWrite: 1.25}
	case strings.Contains(id, "sonnet"):
		return ai.ModelCost{Input: 1.50, Output: 7.50, CacheRead: 0.15, CacheWrite: 1.875}
	case strings.Contains(id, "fable"), strings.Contains(id, "mythos"):
		return ai.ModelCost{Input: 5, Output: 25, CacheRead: 0.50, CacheWrite: 6.25}
	case strings.Contains(id, "opus-4-5"), strings.Contains(id, "opus-4-6"), strings.Contains(id, "opus-4-7"), strings.Contains(id, "opus-4-8"), strings.Contains(id, "opus-5"):
		return ai.ModelCost{Input: 2.50, Output: 12.50, CacheRead: 0.25, CacheWrite: 3.125}
	case strings.Contains(id, "opus"):
		return ai.ModelCost{Input: 7.50, Output: 37.50, CacheRead: 0.75, CacheWrite: 9.375}
	default:
		return ai.ModelCost{}
	}
}
