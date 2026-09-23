package main

import (
	"testing"

	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/internal/tui"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestAgentModelRuntimeResolvesStoredCredentials(t *testing.T) {
	store := midassettings.New(t.TempDir())
	config := tui.LoadAgentModelConfig(store)
	getenv := func(string) string { return "" }
	if _, _, _, err := agentModelRuntime(config, "", nil, getenv, "openai"); err == nil {
		t.Fatal("empty model reference resolved")
	}
	if _, _, _, err := agentModelRuntime(config, "gpt-5.6-sol", nil, getenv, ""); err == nil {
		t.Fatal("reference without a provider resolved")
	}
	provider, model, _, err := agentModelRuntime(config, "openai/gpt-5.6-sol", []ai.Model{{Provider: "openai", ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"}}, getenv, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if provider == nil || model.ID != "gpt-5.6-sol" || model.Name != "GPT-5.6 Sol" || model.Provider != "openai" {
		t.Fatalf("runtime = %#v", model)
	}
}
