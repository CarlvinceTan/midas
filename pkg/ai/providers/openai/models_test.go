package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsDiscoversAndSorts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"z-model"},{"id":"a-model"}]}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "secret", BaseURL: server.URL + "/v1", HTTPClient: server.Client()})
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a-model" || models[1].ID != "z-model" || models[0].API != "openai-responses" {
		t.Fatalf("models=%#v", models)
	}
}

func TestModelsUseConfiguredCompatibleAPIAndProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"compatible-model"}]}`))
	}))
	defer server.Close()
	provider := New(Options{ID: "compatible", DefaultAPI: "openai-completions", APIKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()})
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != "compatible" || models[0].API != "openai-completions" {
		t.Fatalf("models = %#v", models)
	}
}

func TestModelsCarryAdvertisedMetadataAndProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[` +
			`{"id":"gpt-test","name":"GPT Test","context_length":400000,"supported_endpoints":["/chat/completions","/responses"]},` +
			`{"id":"claude-test","name":"Claude Test","context_length":200000,"supported_endpoints":["/messages"]}` +
			`]}`))
	}))
	defer server.Close()
	provider := New(Options{
		ID: "command-code", DefaultAPI: "openai-completions", APIKey: "secret",
		BaseURL: server.URL, HTTPClient: server.Client(),
		ModelAPI: func(_ string, endpoints []string) string {
			for _, endpoint := range endpoints {
				if endpoint == "/messages" {
					return "anthropic-messages"
				}
			}
			return "openai-completions"
		},
	})
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[0].ID != "claude-test" || models[0].Name != "Claude Test" || models[0].API != "anthropic-messages" || models[0].ContextWindow != 200000 {
		t.Fatalf("Claude model = %#v", models[0])
	}
	if models[1].ID != "gpt-test" || models[1].Name != "GPT Test" || models[1].API != "openai-completions" || models[1].ContextWindow != 400000 {
		t.Fatalf("GPT model = %#v", models[1])
	}
}

func TestCompatibleModelsCanBeListedWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = io.WriteString(response, `{"data":[{"id":"local-model"}]}`)
	}))
	defer server.Close()
	provider := New(Options{ID: "local", BaseURL: server.URL, DefaultAPI: "openai-completions", AllowNoAPIKey: true})
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "local-model" {
		t.Fatalf("models = %#v, %v", models, err)
	}
}

func TestModelsAllowExternalOAuthTransportWithoutAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, `{"data":[{"id":"oauth-model"}]}`)
	}))
	defer server.Close()
	provider := New(Options{BaseURL: server.URL, HTTPClient: server.Client(), ExternalAuth: true})
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "oauth-model" {
		t.Fatalf("models = %#v, %v", models, err)
	}
}

func TestSupportsReasoningModelFamilies(t *testing.T) {
	for _, id := range []string{"gpt-5", "gpt-5.1-codex", "o1-mini", "o3", "o4-mini"} {
		if !SupportsReasoning(id) {
			t.Fatalf("%s should support reasoning", id)
		}
	}
	if SupportsReasoning("gpt-4.1") {
		t.Fatal("gpt-4.1 should not be marked as reasoning")
	}
}
