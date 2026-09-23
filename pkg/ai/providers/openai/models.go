package openai

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func (p *Provider) Models(ctx context.Context) ([]ai.Model, error) {
	if len(p.models) > 0 {
		return append([]ai.Model(nil), p.models...), nil
	}
	if p.apiKey == "" && !p.allowNoAPIKey && !p.externalAuth {
		return nil, nil
	}
	endpoint, err := apiEndpoint(p.baseURL, "models")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	mergeHeaders(request.Header, p.headers)
	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: list models: %w", p.id, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, responseError(p.id, response.StatusCode, body)
	}
	var envelope struct {
		Data []struct {
			ID                 string   `json:"id"`
			Name               string   `json:"name"`
			ContextLength      int      `json:"context_length"`
			SupportedEndpoints []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	// The model list is bounded: a response that never ends must not be decoded
	// without limit.
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("%s: decode models: %w", p.id, err)
	}
	baseURL := p.baseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	models := make([]ai.Model, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if item.ID == "" {
			continue
		}
		api := p.defaultAPI
		if p.modelAPI != nil {
			api = p.modelAPI(item.ID, item.SupportedEndpoints)
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = item.ID
		}
		models = append(models, ai.Model{
			ID: item.ID, Name: name, Provider: p.id, API: api, BaseURL: baseURL,
			Reasoning: SupportsReasoning(item.ID), Input: []ai.Modality{ai.ModalityText, ai.ModalityImage},
			ContextWindow: item.ContextLength,
		})
	}
	slices.SortFunc(models, func(a, b ai.Model) int { return cmp.Compare(a.ID, b.ID) })
	return models, nil
}

// SupportsReasoning identifies OpenAI model families that accept reasoning
// effort. Compatible providers can still supply explicit Model metadata.
func SupportsReasoning(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return strings.HasPrefix(id, "gpt-5") || strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4")
}
