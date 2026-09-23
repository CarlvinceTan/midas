package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsPaginatesAndAuthenticates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/models" || r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("request=%s headers=%v", r.URL.Path, r.Header)
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"z","display_name":"Z"}],"has_more":true,"last_id":"z"}`))
			return
		}
		if r.URL.Query().Get("after_id") != "z" {
			t.Errorf("after=%q", r.URL.Query().Get("after_id"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"a","display_name":"A"}],"has_more":false,"last_id":"a"}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "secret", BaseURL: server.URL + "/v1", HTTPClient: server.Client()})
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(models) != 2 || models[0].ID != "a" || models[1].ID != "z" {
		t.Fatalf("calls=%d models=%#v", calls, models)
	}
}

func TestCurrentClaudeCacheMetadata(t *testing.T) {
	if !SupportsAdaptiveThinking("claude-sonnet-5") || SupportsAdaptiveThinking("claude-haiku-4-5") {
		t.Fatal("adaptive-thinking classification is incorrect")
	}
	cost := ModelCost("claude-sonnet-5")
	if cost.Input != 1 || cost.Output != 5 || cost.CacheRead != 0.10 || cost.CacheWrite != 1.25 {
		t.Fatalf("Sonnet 5 cost = %#v", cost)
	}
}
