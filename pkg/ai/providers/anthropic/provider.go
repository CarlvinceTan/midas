// Package anthropic implements the Anthropic Messages API using Go's standard
// HTTP client and provider-neutral ai types.
package anthropic

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

const (
	defaultBaseURL = "https://api.anthropic.com/v1"
	defaultVersion = "2023-06-01"
)

type Options struct {
	ID           string
	APIKey       string
	ExternalAuth bool
	BaseURL      string
	Version      string
	HTTPClient   *http.Client
	Headers      map[string]string
	Models       []ai.Model
}

type Provider struct {
	id           string
	apiKey       string
	externalAuth bool
	baseURL      string
	version      string
	httpClient   *http.Client
	headers      map[string]string
	models       []ai.Model
}

func New(options Options) *Provider {
	id := options.ID
	if id == "" {
		id = "anthropic"
	}
	version := options.Version
	if version == "" {
		version = defaultVersion
	}
	return &Provider{
		id: id, apiKey: options.APIKey, externalAuth: options.ExternalAuth, baseURL: options.BaseURL, version: version, httpClient: options.HTTPClient,
		headers: cloneStrings(options.Headers), models: append([]ai.Model(nil), options.Models...),
	}
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Stream(ctx context.Context, model ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	if model.API != "anthropic-messages" {
		return nil, fmt.Errorf("anthropic: unsupported API %q", model.API)
	}
	payload, err := buildRequest(model, input, options)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	requestCtx := ctx
	cancel := func() {}
	if options.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	}
	endpoint, err := messagesEndpoint(model.BaseURL)
	if err != nil {
		cancel()
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Anthropic-Version", p.version)
	request.Header.Set("User-Agent", "midas-go")
	mergeHeaders(request.Header, model.Headers)
	mergeHeaders(request.Header, p.headers)
	mergeHeaders(request.Header, options.Headers)
	apiKey := options.APIKey
	if apiKey == "" {
		apiKey = p.apiKey
	}
	if apiKey != "" {
		request.Header.Set("X-Api-Key", apiKey)
	}
	if !p.externalAuth && request.Header.Get("X-Api-Key") == "" && request.Header.Get("Authorization") == "" {
		cancel()
		return nil, fmt.Errorf("anthropic: no API key for provider %s", model.Provider)
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
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
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

func messagesEndpoint(baseURL string) (string, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("anthropic: invalid base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/messages"
	return parsed.String(), nil
}

// responseError reports a provider error under the adapter's own ID, so an alias
// is not described as anthropic.
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
