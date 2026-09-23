// Package openai implements the OpenAI Responses API using only Go's standard
// library. It also works with compatible endpoints selected through Model.BaseURL.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
	providerretry "github.com/CarlvinceTan/midas/pkg/ai/providers/internal/retry"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Options struct {
	ID         string
	DefaultAPI string
	APIKey     string
	// AllowNoAPIKey supports local OpenAI-compatible servers. It should not be
	// enabled for OpenAI's hosted API.
	AllowNoAPIKey bool
	// ExternalAuth allows an HTTP transport (for example OAuth2) to attach
	// Authorization after the request leaves this adapter.
	ExternalAuth bool
	BaseURL      string
	HTTPClient   *http.Client
	Headers      map[string]string
	Models       []ai.Model
	// ModelAPI picks the protocol for a discovered model. The endpoints are the
	// ones the provider advertises for that model, which lets a provider that
	// serves several protocols from one base URL label each model correctly.
	ModelAPI func(modelID string, endpoints []string) string
}

type Provider struct {
	id            string
	defaultAPI    string
	apiKey        string
	allowNoAPIKey bool
	externalAuth  bool
	baseURL       string
	httpClient    *http.Client
	headers       map[string]string
	models        []ai.Model
	modelAPI      func(modelID string, endpoints []string) string
}

func New(options Options) *Provider {
	id := options.ID
	if id == "" {
		id = "openai"
	}
	defaultAPI := options.DefaultAPI
	if defaultAPI == "" {
		defaultAPI = "openai-responses"
	}
	return &Provider{
		id:            id,
		defaultAPI:    defaultAPI,
		apiKey:        options.APIKey,
		allowNoAPIKey: options.AllowNoAPIKey,
		externalAuth:  options.ExternalAuth,
		baseURL:       options.BaseURL,
		httpClient:    options.HTTPClient,
		headers:       cloneStrings(options.Headers),
		models:        append([]ai.Model(nil), options.Models...),
		modelAPI:      options.ModelAPI,
	}
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Stream(ctx context.Context, model ai.Model, input ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	var (
		payload  map[string]any
		path     string
		consumer func(context.Context, context.CancelFunc, io.ReadCloser, ai.Model, *ai.AssistantStream)
		err      error
	)
	switch model.API {
	case "openai-responses":
		payload, err = buildRequest(model, input, options)
		path = "responses"
		consumer = consumeResponsesStream
	case "openai-completions":
		payload, err = buildCompletionsRequest(model, input, options)
		path = "chat/completions"
		consumer = consumeCompletionsStream
	default:
		return nil, fmt.Errorf("openai: unsupported API %q", model.API)
	}
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}

	requestCtx := ctx
	cancel := func() {}
	if options.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, options.Timeout)
	}
	endpoint, err := apiEndpoint(model.BaseURL, path)
	if err != nil {
		cancel()
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openai: create request: %w", err)
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
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if !p.allowNoAPIKey && !p.externalAuth && request.Header.Get("Authorization") == "" && request.Header.Get("Cf-Aig-Authorization") == "" {
		cancel()
		return nil, fmt.Errorf("openai: no API key for provider %s", model.Provider)
	}
	if options.SessionID != "" && options.CacheRetention != ai.CacheNone {
		request.Header.Set("Session-Id", options.SessionID)
		request.Header.Set("X-Client-Request-Id", options.SessionID)
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
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		defer cancel()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, responseError(p.id, response.StatusCode, message)
	}

	stream := ai.NewAssistantStream()
	go consumer(requestCtx, cancel, response.Body, model, stream)
	return stream, nil
}

func apiEndpoint(baseURL, endpointPath string) (string, error) {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("openai: invalid base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + endpointPath
	return parsed.String(), nil
}

// responseError reports a provider error under the adapter's own ID, so an alias
// such as xai or deepseek is not described as openai.
func responseError(provider string, status int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
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
			continue
		}
		target.Set(name, value)
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

var errTerminalMissing = errors.New("OpenAI Responses stream ended before a terminal response event")
