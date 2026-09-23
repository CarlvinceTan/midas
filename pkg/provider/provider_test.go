package provider

import (
	"testing"

	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/pkg/ai"
	providerauth "github.com/CarlvinceTan/midas/pkg/ai/auth"
)

func TestConfiguredProviders(t *testing.T) {
	env := func(name string) string {
		return map[string]string{"OPENAI_API_KEY": "o", "ANTHROPIC_API_KEY": "a", "GEMINI_API_KEY": "g"}[name]
	}
	tests := []struct {
		provider string
		api      string
		key      string
	}{
		{"openai", "openai-responses", "o"},
		{"anthropic", "anthropic-messages", "a"},
		{"google", "google-generative-ai", "g"},
	}
	for _, test := range tests {
		provider, model, key, err := ConfiguredProvider(test.provider, "model", "", "", env)
		if err != nil || provider == nil || model.API != test.api || model.Provider != test.provider || key != test.key {
			t.Fatalf("%s = %#v %q %v", test.provider, model, key, err)
		}
	}
	if _, _, _, err := ConfiguredProvider("unknown", "x", "", "", env); err == nil {
		t.Fatal("unknown provider succeeded")
	}
	compatibleEnv := func(name string) string {
		if name == "OPENROUTER_API_KEY" {
			return "router-key"
		}
		return ""
	}
	provider, model, key, err := ConfiguredProvider("openrouter", "model", "", "https://openrouter.example/v1", compatibleEnv)
	if err != nil || provider.ID() != "openrouter" || model.API != "openai-completions" || key != "router-key" {
		t.Fatalf("compatible = %#v %#v %q, %v", provider, model, key, err)
	}
	if got := ProviderKeyName("azure-openai"); got != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("provider key = %q", got)
	}
}

func TestGitHubCopilotModelAPIRouting(t *testing.T) {
	tests := map[string]string{
		"claude-sonnet-5": "anthropic-messages",
		"gpt-6-astra":     "openai-responses",
		"gpt-4.1":         "openai-completions",
	}
	for model, expected := range tests {
		if got := GitHubCopilotAPI(model); got != expected {
			t.Errorf("%s API = %q, want %q", model, got, expected)
		}
	}
}

func TestMaskedAPIKeyNeverShowsShortKeys(t *testing.T) {
	if got := MaskedAPIKey("abc"); got != "••••" {
		t.Fatalf("short key mask = %q", got)
	}
	if got := MaskedAPIKey("sk-example-1234"); got != "••••1234" {
		t.Fatalf("key mask = %q", got)
	}
}

func TestCommandCodeModelAPIRouting(t *testing.T) {
	env := func(name string) string {
		if name == "CMD_API_KEY" {
			return "cmd-key"
		}
		return ""
	}
	provider, claude, key, err := ConfiguredProvider("command-code", "claude-sonnet-5", "", "", env)
	if err != nil || provider.ID() != "command-code" || key != "cmd-key" || claude.API != "anthropic-messages" {
		t.Fatalf("Claude = %#v %q %v", claude, key, err)
	}
	_, deepseek, _, err := ConfiguredProvider("command-code", "deepseek/deepseek-v4-flash", "", "", env)
	if err != nil || deepseek.API != "openai-completions" {
		t.Fatalf("DeepSeek = %#v %v", deepseek, err)
	}
	_, responsesOnly, _, err := ConfiguredProvider("command-code", "future-model", "openai-responses", "", env)
	if err != nil || responsesOnly.API != "openai-responses" {
		t.Fatalf("Responses-only = %#v %v", responsesOnly, err)
	}
}

func TestConfiguredProviderUsesSavedAPIKey(t *testing.T) {
	t.Setenv("MIDAS_CONFIG_DIR", t.TempDir())
	store := providerauth.New(storage.ConfigDir())
	if err := store.Set("openai", providerauth.Credential{Kind: providerauth.KindAPIKey, Name: "OpenAI", APIKey: "saved-key"}); err != nil {
		t.Fatal(err)
	}
	_, _, key, err := ConfiguredProvider("openai", "gpt-test", "", "", func(string) string { return "" })
	if err != nil || key != "saved-key" {
		t.Fatalf("saved key = %q, %v", key, err)
	}
}

func TestConfiguredProviderUsesSavedAPIKeyPool(t *testing.T) {
	t.Setenv("MIDAS_CONFIG_DIR", t.TempDir())
	store := providerauth.New(storage.ConfigDir())
	if err := store.Set("openai", providerauth.Credential{Kind: providerauth.KindAPIKey, Name: "OpenAI", APIKey: "second", APIKeys: []string{"first", "second"}}); err != nil {
		t.Fatal(err)
	}
	provider, _, key, err := ConfiguredProvider("openai", "gpt-test", "", "", func(string) string { return "" })
	if err != nil || key != "second" {
		t.Fatalf("configured key = %q, %v", key, err)
	}
	if _, ok := provider.(*ai.APIKeyPoolProvider); !ok {
		t.Fatalf("provider = %T, want API key pool", provider)
	}
}

func TestProviderSlug(t *testing.T) {
	if got := ProviderSlug("  My OAuth Provider  "); got != "my-oauth-provider" {
		t.Fatalf("slug = %q", got)
	}
}
