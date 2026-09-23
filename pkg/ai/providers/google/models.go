package google

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
		return nil, fmt.Errorf("google: invalid base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	models := []ai.Model{}
	pageToken := ""
	for {
		query := parsed.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		parsed.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("x-goog-api-key", p.apiKey)
		mergeHeaders(request.Header, p.headers)
		client := p.httpClient
		if client == nil {
			client = http.DefaultClient
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("%s: list models: %w", p.id, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, responseError(p.id, response.StatusCode, body)
		}
		var envelope struct {
			Models []struct {
				Name             string   `json:"name"`
				BaseModelID      string   `json:"baseModelId"`
				DisplayName      string   `json:"displayName"`
				InputTokenLimit  int      `json:"inputTokenLimit"`
				OutputTokenLimit int      `json:"outputTokenLimit"`
				Methods          []string `json:"supportedGenerationMethods"`
				Thinking         bool     `json:"thinking"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("google: decode models: %w", err)
		}
		for _, item := range envelope.Models {
			supported := false
			for _, method := range item.Methods {
				if method == "generateContent" {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
			id := item.BaseModelID
			if id == "" {
				id = strings.TrimPrefix(item.Name, "models/")
			}
			if id == "" {
				continue
			}
			name := item.DisplayName
			if name == "" {
				name = id
			}
			models = append(models, ai.Model{ID: id, Name: name, Provider: p.id, API: "google-generative-ai", BaseURL: baseURL, Reasoning: item.Thinking, Input: []ai.Modality{ai.ModalityText, ai.ModalityImage}, ContextWindow: item.InputTokenLimit, MaxTokens: item.OutputTokenLimit})
		}
		if envelope.NextPageToken == "" || envelope.NextPageToken == pageToken {
			break
		}
		pageToken = envelope.NextPageToken
	}
	slices.SortFunc(models, func(a, b ai.Model) int { return cmp.Compare(a.ID, b.ID) })
	return models, nil
}
