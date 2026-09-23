// Package google implements Google's Gemini Generative Language streaming API
// using Go's standard HTTP client.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
	providerretry "github.com/CarlvinceTan/midas/pkg/ai/providers/internal/retry"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type Options struct {
	ID           string
	APIKey       string
	BaseURL      string
	HTTPClient   *http.Client
	Headers      map[string]string
	Models       []ai.Model
	ExternalAuth bool
}

type Provider struct {
	id           string
	apiKey       string
	baseURL      string
	httpClient   *http.Client
	headers      map[string]string
	models       []ai.Model
	externalAuth bool
}

func New(options Options) *Provider {
	id := options.ID
	if id == "" {
		id = "google"
	}
	return &Provider{
		id: id, apiKey: options.APIKey, baseURL: options.BaseURL, httpClient: options.HTTPClient,
		headers: cloneStrings(options.Headers), models: append([]ai.Model(nil), options.Models...), externalAuth: options.ExternalAuth,
	}
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Stream(ctx context.Context, model ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	if model.API != "google-generative-ai" {
		return nil, fmt.Errorf("google: unsupported API %q", model.API)
	}
	payload, err := buildRequest(model, input, options)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("google: encode request: %w", err)
	}

	requestCtx := ctx
	cancel := func() {}
	if options.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	}
	endpoint, err := streamEndpoint(model.BaseURL, model.ID)
	if err != nil {
		cancel()
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("google: create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "midas-go")
	mergeHeaders(request.Header, model.Headers)
	mergeHeaders(request.Header, p.headers)
	mergeHeaders(request.Header, options.Headers)
	apiKey := options.APIKey
	if apiKey == "" {
		apiKey = p.apiKey
	}
	if apiKey != "" {
		request.Header.Set("X-Goog-Api-Key", apiKey)
	}
	if !p.externalAuth && request.Header.Get("X-Goog-Api-Key") == "" && request.Header.Get("Authorization") == "" {
		cancel()
		return nil, fmt.Errorf("google: no API key for provider %s", model.Provider)
	}

	client := options.HTTPClient
	if client == nil {
		client = p.httpClient
	}
	if client == nil {
		client = http.DefaultClient
	}
	response, err := providerretry.Do(client, request, body, options.MaxRetries, options.MaxRetryDelay)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("google: request failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		defer cancel()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, responseError(p.id, response.StatusCode, message)
	}

	stream := ai.NewAssistantStream()
	go consumeStream(requestCtx, cancel, response.Body, model, stream)
	return stream, nil
}

func streamEndpoint(baseURL, modelID string) (string, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if modelID == "" || strings.Contains(modelID, "..") || strings.ContainsAny(modelID, "?&") {
		return "", fmt.Errorf("google: invalid model ID %q", modelID)
	}
	modelID = strings.TrimPrefix(modelID, "models/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("google: invalid base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models/" + url.PathEscape(modelID) + ":streamGenerateContent"
	query := parsed.Query()
	query.Set("alt", "sse")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// responseError reports a provider error under the adapter's own ID, so an alias
// is not described as google.
func responseError(provider string, status int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &ai.HTTPError{Provider: provider, Status: status, Message: message}
}

func mergeHeaders(target http.Header, values map[string]string) {
	for name, value := range values {
		if value == "" {
			target.Del(name)
		} else {
			target.Set(name, value)
		}
	}
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
