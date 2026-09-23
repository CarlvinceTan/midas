// Package provider turns a provider ID and model into the concrete provider,
// model, and API key the runtime should use. It owns credential lookup
// (environment, auth.json, OAuth), the provider catalog, and model discovery, so
// every Midas entry point shares one implementation.
package provider

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/CarlvinceTan/midas/internal/storage"
	"github.com/CarlvinceTan/midas/pkg/ai"
	providerauth "github.com/CarlvinceTan/midas/pkg/ai/auth"
	providercatalog "github.com/CarlvinceTan/midas/pkg/ai/catalog"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/anthropic"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/google"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/openai"
	"net/http"
	"slices"
	"strings"
	"sync"
)

func ConfiguredProvider(providerID, modelID, api, baseURL string, getenv func(string) string) (ai.Provider, ai.Model, string, error) {
	explicitBaseURL := strings.TrimSpace(baseURL) != ""
	explicitAPI := strings.TrimSpace(api)
	credential, hasCredential := providerauth.New(storage.ConfigDir()).Get(providerID)
	spec, knownProvider := providercatalog.Lookup(providerID)
	if baseURL == "" && hasCredential {
		baseURL = credential.BaseURL
	}
	if baseURL == "" && knownProvider {
		baseURL = spec.BaseURL
	}
	if api == "" && hasCredential {
		api = credential.API
	}
	if api == "" && knownProvider {
		api = spec.API
	}
	if providerID == "github-copilot" && modelID != "" {
		api = GitHubCopilotAPI(modelID)
	}
	model := ai.Model{ID: modelID, Name: modelID, Provider: providerID, BaseURL: baseURL, Input: []ai.Modality{ai.ModalityText, ai.ModalityImage}}
	switch providerID {
	case "openai":
		if api == "" {
			api = "openai-responses"
		}
		model.API = api
		model.Reasoning = openai.SupportsReasoning(modelID)
		key := getenv("OPENAI_API_KEY")
		if key == "" && hasCredential {
			key = credential.APIKey
		}
		provider := openai.New(openai.Options{APIKey: key, BaseURL: baseURL})
		return WithStoredAPIKeys(provider, credential, key), model, key, nil
	case "anthropic":
		if api == "" {
			api = "anthropic-messages"
		}
		model.API = api
		model.Reasoning = true
		model.AdaptiveThinking = anthropic.SupportsAdaptiveThinking(modelID)
		model.Cost = anthropic.ModelCost(modelID)
		model.PromptCache = ai.ModelPromptCache{Short: 300, Long: 3600}
		key := getenv("ANTHROPIC_API_KEY")
		if key == "" && hasCredential {
			if credential.Kind == providerauth.KindOAuth {
				token, tokenErr := providerauth.New(storage.ConfigDir()).OAuthToken(context.Background(), providerID)
				if tokenErr != nil {
					return nil, ai.Model{}, "", tokenErr
				}
				key = token.AccessToken
			} else {
				key = credential.APIKey
			}
		}
		provider := anthropic.New(anthropic.Options{APIKey: key, BaseURL: baseURL})
		return WithStoredAPIKeys(provider, credential, key), model, key, nil
	case "google":
		if api == "" {
			api = "google-generative-ai"
		}
		model.API = api
		lowerModel := strings.ToLower(modelID)
		model.Reasoning = strings.Contains(lowerModel, "gemini-2.5") || strings.Contains(lowerModel, "gemini-3") || strings.Contains(lowerModel, "gemma-4")
		key := getenv("GOOGLE_API_KEY")
		if key == "" {
			key = getenv("GEMINI_API_KEY")
		}
		if key == "" && hasCredential {
			key = credential.APIKey
		}
		if hasCredential && credential.Kind == providerauth.KindOAuth {
			client, clientErr := providerauth.New(storage.ConfigDir()).OAuthClient(context.Background(), providerID)
			if clientErr != nil {
				return nil, ai.Model{}, "", clientErr
			}
			headers := map[string]string{}
			if credential.ProjectID != "" {
				headers["X-Goog-User-Project"] = credential.ProjectID
			}
			return google.New(google.Options{BaseURL: baseURL, HTTPClient: client, Headers: headers, ExternalAuth: true}), model, "", nil
		}
		provider := google.New(google.Options{APIKey: key, BaseURL: baseURL})
		return WithStoredAPIKeys(provider, credential, key), model, key, nil
	default:
		if strings.TrimSpace(baseURL) == "" {
			return nil, ai.Model{}, "", fmt.Errorf("provider %q requires --base-url for OpenAI-compatible access", providerID)
		}
		if spec.MixedProtocols && explicitAPI == "" {
			// One base URL serves several protocols here, so the model decides
			// which one to use instead of the provider-wide catalog default.
			api = providercatalog.ModelProtocol(modelID, nil)
		}
		if api == "" {
			api = "openai-completions"
		}
		model.API = api
		model.Reasoning = openai.SupportsReasoning(modelID) || strings.Contains(strings.ToLower(modelID), "reason")
		if providerID == "github-copilot" && api == "anthropic-messages" {
			model.Reasoning = true
			model.AdaptiveThinking = anthropic.SupportsAdaptiveThinking(modelID)
		}
		if spec.MixedProtocols {
			model.Reasoning = model.Reasoning || providercatalog.ModelSupportsReasoning(modelID, api)
			if api == "anthropic-messages" {
				model.AdaptiveThinking = anthropic.SupportsAdaptiveThinking(modelID)
			}
		}
		key := ""
		if knownProvider {
			for _, envName := range spec.EnvVars {
				if key = getenv(envName); key != "" {
					break
				}
			}
		} else {
			key = getenv(ProviderKeyName(providerID))
		}
		if key == "" {
			key = getenv("MIDAS_API_KEY")
		}
		if key == "" && hasCredential {
			key = credential.APIKey
		}
		var oauthClient *http.Client
		if hasCredential && credential.Kind == providerauth.KindOAuth {
			var clientErr error
			oauthClient, clientErr = providerauth.New(storage.ConfigDir()).OAuthClient(context.Background(), providerID)
			if clientErr != nil {
				return nil, ai.Model{}, "", clientErr
			}
		}
		extraHeaders := CommandCodeHeaders(providerID, getenv)
		switch api {
		case "anthropic-messages":
			headers := copyHeaders(extraHeaders)
			if providerID == "github-copilot" {
				for name, value := range GitHubCopilotHeaders() {
					headers[name] = value
				}
			}
			provider := anthropic.New(anthropic.Options{ID: providerID, APIKey: key, BaseURL: baseURL, HTTPClient: oauthClient, ExternalAuth: oauthClient != nil, Headers: headers})
			return WithStoredAPIKeys(provider, credential, key), model, key, nil
		case "google-generative-ai":
			provider := google.New(google.Options{ID: providerID, APIKey: key, BaseURL: baseURL, HTTPClient: oauthClient, ExternalAuth: oauthClient != nil})
			return WithStoredAPIKeys(provider, credential, key), model, key, nil
		case "openai-completions", "openai-responses":
			headers := copyHeaders(extraHeaders)
			providerKey := key
			allowNoKey := (!knownProvider || explicitBaseURL) && oauthClient == nil
			switch providerID {
			case "github-copilot":
				headers["User-Agent"] = "GitHubCopilotChat/0.35.0"
				headers["Editor-Version"] = "vscode/1.107.0"
				headers["Editor-Plugin-Version"] = "copilot-chat/0.35.0"
				headers["Copilot-Integration-Id"] = "vscode-chat"
				headers["X-GitHub-Api-Version"] = "2026-06-01"
			case "openai-codex":
				headers["chatgpt-account-id"] = credential.Env["CHATGPT_ACCOUNT_ID"]
				headers["originator"] = "pi"
				headers["OpenAI-Beta"] = "responses=experimental"
			case "azure-openai-responses":
				headers["api-key"] = key
				providerKey = ""
				allowNoKey = true
			case "cloudflare-ai-gateway":
				if key != "" {
					headers["Cf-Aig-Authorization"] = "Bearer " + key
					providerKey = ""
					allowNoKey = true
				}
			}
			var modelAPI func(modelID string, endpoints []string) string
			switch {
			case providerID == "github-copilot":
				modelAPI = func(modelID string, _ []string) string { return GitHubCopilotAPI(modelID) }
			case spec.MixedProtocols:
				modelAPI = providercatalog.ModelProtocol
			}
			provider := openai.New(openai.Options{ID: providerID, DefaultAPI: api, APIKey: providerKey, BaseURL: baseURL, Headers: headers, AllowNoAPIKey: allowNoKey, ExternalAuth: oauthClient != nil, HTTPClient: oauthClient, ModelAPI: modelAPI})
			return WithStoredAPIKeys(provider, credential, providerKey), model, providerKey, nil
		default:
			return nil, ai.Model{}, "", fmt.Errorf("provider %q uses unsupported API %q", providerID, api)
		}
	}
}

func WithStoredAPIKeys(provider ai.Provider, credential providerauth.Credential, primary string) ai.Provider {
	if credential.Kind != providerauth.KindAPIKey {
		return provider
	}
	keys := append([]string(nil), credential.APIKeys...)
	if credential.APIKey != "" {
		keys = append(keys, credential.APIKey)
	}
	if primary != "" {
		keys = append(keys, primary)
	}
	return ai.NewAPIKeyPoolProvider(provider, keys, primary)
}

// agentModelRuntime resolves a stored model reference into a provider, model and
// API key using the configured provider catalog.

func GitHubCopilotAPI(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if strings.Contains(id, "claude") {
		return "anthropic-messages"
	}
	if strings.HasPrefix(id, "gpt-5") || strings.HasPrefix(id, "gpt-6") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") {
		return "openai-responses"
	}
	return "openai-completions"
}

func GitHubCopilotHeaders() map[string]string {
	return map[string]string{
		"User-Agent": "GitHubCopilotChat/0.35.0", "Editor-Version": "vscode/1.107.0",
		"Editor-Plugin-Version": "copilot-chat/0.35.0", "Copilot-Integration-Id": "vscode-chat", "X-GitHub-Api-Version": "2026-06-01",
	}
}

// CommandCodeHeaders carries the provider-specific request headers a catalog
// entry cannot express, currently CommandCode's opt-in zero-data-retention flag.

func CommandCodeHeaders(providerID string, getenv func(string) string) map[string]string {
	if providerID != "command-code" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(getenv("CMD_ZDR"))) {
	case "1", "true":
		return map[string]string{"x-cmd-zdr": "1"}
	default:
		return nil
	}
}

func copyHeaders(source map[string]string) map[string]string {
	headers := make(map[string]string, len(source))
	for name, value := range source {
		headers[name] = value
	}
	return headers
}

func ProviderKeyName(providerID string) string {
	var name strings.Builder
	for _, value := range strings.ToUpper(strings.TrimSpace(providerID)) {
		if value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
			name.WriteRune(value)
		} else {
			name.WriteByte('_')
		}
	}
	return strings.Trim(name.String(), "_") + "_API_KEY"
}

func EnvDefault(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}

func DiscoverModels(ctx context.Context, current ai.Provider, selected ai.Model, getenv func(string) string) []ai.Model {
	providers := map[string]ai.Provider{}
	currentID := ""
	if current != nil && current.ID() != "" {
		currentID = current.ID()
		providers[currentID] = current
	}
	if currentID != "openai" && getenv("OPENAI_API_KEY") != "" {
		providers["openai"] = openai.New(openai.Options{APIKey: getenv("OPENAI_API_KEY")})
	}
	if currentID != "anthropic" && getenv("ANTHROPIC_API_KEY") != "" {
		providers["anthropic"] = anthropic.New(anthropic.Options{APIKey: getenv("ANTHROPIC_API_KEY")})
	}
	googleKey := getenv("GOOGLE_API_KEY")
	if googleKey == "" {
		googleKey = getenv("GEMINI_API_KEY")
	}
	if currentID != "google" && googleKey != "" {
		providers["google"] = google.New(google.Options{APIKey: googleKey})
	}
	for _, providerID := range providerauth.New(storage.ConfigDir()).ProviderIDs() {
		if _, exists := providers[providerID]; exists {
			continue
		}
		provider, _, _, err := ConfiguredProvider(providerID, "", "", "", getenv)
		if err == nil {
			providers[providerID] = provider
		}
	}

	type result struct {
		models []ai.Model
	}
	results := make(chan result, len(providers))
	var workers sync.WaitGroup
	for _, provider := range providers {
		workers.Add(1)
		go func(provider ai.Provider) {
			defer workers.Done()
			models, err := provider.Models(ctx)
			if err == nil {
				results <- result{models: models}
			}
		}(provider)
	}
	workers.Wait()
	close(results)

	seen := make(map[string]bool)
	models := make([]ai.Model, 0)
	for result := range results {
		for _, model := range result.models {
			key := model.Provider + "\x00" + model.ID
			if model.ID == "" || seen[key] {
				continue
			}
			seen[key] = true
			models = append(models, model)
		}
	}
	ApplyCatalogCapabilities(models)
	selectedKey := selected.Provider + "\x00" + selected.ID
	if selected.ID != "" && !seen[selectedKey] {
		models = append(models, selected)
	}
	slices.SortStableFunc(models, func(a, b ai.Model) int {
		if a.Provider == b.Provider {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	return models
}

// ApplyCatalogCapabilities fills the per-model capabilities a model listing
// cannot express on its own, so discovery matches the model the runtime builds
// for the same provider and model ID.

func ApplyCatalogCapabilities(models []ai.Model) {
	for index, model := range models {
		spec, known := providercatalog.Lookup(model.Provider)
		if !known || !spec.MixedProtocols {
			continue
		}
		models[index].Reasoning = model.Reasoning || providercatalog.ModelSupportsReasoning(model.ID, model.API)
	}
}

func DisplayModelDetails(selected ai.Model, models []ai.Model) ai.Model {
	for _, model := range models {
		if model.Provider != selected.Provider || model.ID != selected.ID {
			continue
		}
		if model.API == "" {
			model.API = selected.API
		}
		if model.BaseURL == "" {
			model.BaseURL = selected.BaseURL
		}
		return model
	}
	return selected
}

// DisconnectedProvider is the placeholder a provider slot holds while a
// connection is being replaced.
type DisconnectedProvider struct{}

func (DisconnectedProvider) ID() string                                 { return "" }
func (DisconnectedProvider) Models(context.Context) ([]ai.Model, error) { return nil, nil }
func (DisconnectedProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (*ai.AssistantStream, error) {
	return nil, errors.New("no provider selected")
}
