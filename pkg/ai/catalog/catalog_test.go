package catalog

import "testing"

func TestCatalogCoversNativePiAIKeyProvidersWithoutOpenCode(t *testing.T) {
	providers := APIKeyProviders()
	if len(providers) < 30 {
		t.Fatalf("provider count = %d", len(providers))
	}
	for _, id := range []string{"anthropic", "command-code", "deepseek", "google-vertex", "meta", "openrouter", "qwen-token-plan-individual", "xiaomi-token-plan-sgp"} {
		if _, ok := Lookup(id); !ok {
			t.Errorf("missing provider %q", id)
		}
	}
	for _, id := range []string{"opencode", "opencode-go"} {
		if _, ok := Lookup(id); ok {
			t.Errorf("OpenCode backend %q should not be in Midas", id)
		}
	}
}

func TestCatalogReturnsCopiesInDisplayOrder(t *testing.T) {
	first := APIKeyProviders()
	second := APIKeyProviders()
	first[0].Name = "changed"
	if second[0].Name == "changed" {
		t.Fatal("catalog slice was shared")
	}
	for index := 1; index < len(second); index++ {
		if second[index-1].Name > second[index].Name {
			t.Fatalf("catalog is not sorted at %q", second[index].Name)
		}
	}
}

func TestCommandCodeUsesCanonicalDisplayName(t *testing.T) {
	provider, ok := Lookup("command-code")
	if !ok || provider.Name != "CommandCode" || provider.Description != "CommandCode plan or Provider API credits" {
		t.Fatalf("CommandCode provider = %#v, %v", provider, ok)
	}
	if !provider.MixedProtocols {
		t.Fatal("CommandCode serves several protocols from one base URL")
	}
}

func TestModelProtocolPrefersAdvertisedEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		modelID   string
		endpoints []string
		want      string
	}{
		{name: "Chat Completions wins over Responses", modelID: "deepseek/deepseek-v4-flash", endpoints: []string{"/chat/completions", "/responses"}, want: "openai-completions"},
		{name: "Responses only", modelID: "future-model", endpoints: []string{"/responses"}, want: "openai-responses"},
		{name: "Messages only", modelID: "claude-test", endpoints: []string{"/messages"}, want: "anthropic-messages"},
		{name: "Endpoint spelling is tolerated", modelID: "claude-test", endpoints: []string{" messages "}, want: "anthropic-messages"},
		{name: "Claude without endpoints", modelID: "claude-sonnet-5", want: "anthropic-messages"},
		{name: "Unknown model without endpoints", modelID: "deepseek/deepseek-v4-flash", want: "openai-completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ModelProtocol(test.modelID, test.endpoints); got != test.want {
				t.Fatalf("ModelProtocol(%q, %v) = %q, want %q", test.modelID, test.endpoints, got, test.want)
			}
		})
	}
}

func TestModelSupportsReasoning(t *testing.T) {
	if !ModelSupportsReasoning("claude-sonnet-5", "anthropic-messages") {
		t.Fatal("Anthropic-shaped routes carry thinking")
	}
	for _, id := range []string{"deepseek/deepseek-v4-flash", "kimi-k2", "glm-5", "qwen3-max", "minimax-m2"} {
		if !ModelSupportsReasoning(id, "openai-completions") {
			t.Fatalf("%s should support reasoning", id)
		}
	}
	if ModelSupportsReasoning("llama-3", "openai-completions") {
		t.Fatal("llama-3 should not be marked as reasoning")
	}
}
