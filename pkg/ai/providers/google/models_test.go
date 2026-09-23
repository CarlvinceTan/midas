package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsPaginatesAndFiltersGenerationModels(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1beta/models" || r.Header.Get("x-goog-api-key") != "secret" {
			t.Errorf("request=%s key=%q", r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"models":[{"name":"models/z","baseModelId":"z","displayName":"Z","supportedGenerationMethods":["generateContent"],"inputTokenLimit":100,"outputTokenLimit":20,"thinking":true},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next"}`))
			return
		}
		if r.URL.Query().Get("pageToken") != "next" {
			t.Errorf("token=%q", r.URL.Query().Get("pageToken"))
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/a","displayName":"A","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "secret", BaseURL: server.URL + "/v1beta", HTTPClient: server.Client()})
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(models) != 2 || models[0].ID != "a" || models[1].ID != "z" || !models[1].Reasoning || models[1].ContextWindow != 100 {
		t.Fatalf("calls=%d models=%#v", calls, models)
	}
}

func TestModelsAllowExternalOAuthTransportWithoutAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-goog-api-key") != "" {
			t.Fatalf("unexpected API key header")
		}
		_, _ = response.Write([]byte(`{"models":[{"name":"models/oauth","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer server.Close()
	provider := New(Options{BaseURL: server.URL, HTTPClient: server.Client(), ExternalAuth: true})
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "oauth" {
		t.Fatalf("models = %#v, %v", models, err)
	}
}
