package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"

	"github.com/CarlvinceTan/midas/internal/profiles"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func testAgentModels(t *testing.T) AgentModelConfig {
	t.Helper()
	return LoadAgentModelConfig(midassettings.New(t.TempDir()))
}

func TestAgentModelRefFallsBackFromPinToLastUsedToParent(t *testing.T) {
	config := testAgentModels(t)
	// The entry agent uses the model it last used.
	if err := config.SetLastUsed("main", "openai/gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if got := config.Ref("main", ""); got != "openai/gpt-5.6-sol" {
		t.Fatalf("main model = %q", got)
	}
	// A pin outranks Last Used.
	if err := config.SetModel("main", "commandcode2/deepseek-v4-1-flash"); err != nil {
		t.Fatal(err)
	}
	if got := config.Ref("main", ""); got != "commandcode2/deepseek-v4-1-flash" {
		t.Fatalf("pinned main model = %q", got)
	}
	// Subagents inherit the caller's model when they have none of their own.
	for _, name := range []string{"advisor", "explore"} {
		if got := config.Ref(name, "openai/gpt-6-astra"); got != "openai/gpt-6-astra" {
			t.Fatalf("%s model = %q", name, got)
		}
	}
}

func TestAgentModelInheritanceComesFromTheEntryAgent(t *testing.T) {
	config := testAgentModels(t)
	if err := config.SetModel("main", "openai/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"advisor", "explore", "title", "summary"} {
		if got := config.InheritedRef(name, "main", "commandcode2/deepseek-v4-1-flash"); got != "openai/gpt-6-astra" {
			t.Fatalf("%s inherited %q, want main's model", name, got)
		}
	}
	// With no entry agent context, main is the parent. A pinned parent model wins
	// over the session's own.
	if got := config.InheritedRef("advisor", "", "commandcode2/deepseek-v4-1-flash"); got != "openai/gpt-6-astra" {
		t.Fatalf("advisor inherited %q", got)
	}
	unpinned := testAgentModels(t)
	if got := unpinned.InheritedRef("advisor", "", "commandcode2/deepseek-v4-1-flash"); got != "commandcode2/deepseek-v4-1-flash" {
		t.Fatalf("unpinned advisor inherited %q", got)
	}
}

func TestAgentModelDisplayAndChoiceDefaults(t *testing.T) {
	config := testAgentModels(t)
	catalog := []ai.Model{{Provider: "openai", ID: "gpt-6-astra", Name: "GPT-6 Astra", Reasoning: true}}
	if got, want := config.Display("main", catalog), "Default (Last used)"; got != want {
		t.Fatalf("main display = %q, want %q", got, want)
	}
	for name, want := range map[string]string{
		"advisor": "Default (Inherit)",
		"explore": "Default (Inherit)",
		"title":   "Default (Inherit)",
		"summary": "Default (Inherit)",
	} {
		if got := config.Display(name, catalog); got != want {
			t.Fatalf("%s display = %q, want %q", name, got, want)
		}
	}
	if err := config.SetModel("explore", "openai/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetLevel("explore", ai.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if got, want := config.Display("explore", catalog), "GPT-6 Astra · high"; got != want {
		t.Fatalf("pinned explore display = %q, want %q", got, want)
	}

	options := ModelChoiceOptions("advisor", catalog)
	if len(options) != 2 {
		t.Fatalf("choices = %#v", options)
	}
	if options[0].Label != "Default (Inherit)" || options[0].Value != "" {
		t.Fatalf("first choice = %#v", options[0])
	}
	if !strings.Contains(options[0].Description, "main") {
		t.Fatalf("default choice does not name its parent: %#v", options[0])
	}
	if options[1].Label != "GPT-6 Astra" || options[1].Description != "OpenAI" || options[1].Value != "openai/gpt-6-astra" {
		t.Fatalf("model choice = %#v", options[1])
	}
}

func TestAgentModelThinkingLevelPrecedence(t *testing.T) {
	config := testAgentModels(t)
	if err := config.SetModelLevel("openai/gpt-6-astra", ai.ThinkingXHigh); err != nil {
		t.Fatal(err)
	}
	if got := config.Level("explore", "openai/gpt-6-astra", ai.ThinkingMedium); got != ai.ThinkingXHigh {
		t.Fatalf("model level = %q", got)
	}
	if err := config.SetLevel("explore", ai.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	if got := config.Level("explore", "openai/gpt-6-astra", ai.ThinkingMedium); got != ai.ThinkingLow {
		t.Fatalf("agent level = %q", got)
	}
	if got := config.Level("advisor", "unknown/model", ai.ThinkingMedium); got != ai.ThinkingMedium {
		t.Fatalf("fallback level = %q", got)
	}
	for _, value := range []string{"", "  ", "turbo", "OFF"} {
		if got := parseThinkingLevel(value); got != "" {
			t.Fatalf("parseThinkingLevel(%q) = %q", value, got)
		}
	}
	if got := parseThinkingLevel(" xhigh "); got != ai.ThinkingXHigh {
		t.Fatalf("parseThinkingLevel(\" xhigh \") = %q", got)
	}
}

func TestAgentModelSettingsPersistPerAgent(t *testing.T) {
	directory := t.TempDir()
	store := midassettings.New(directory)
	config := LoadAgentModelConfig(store)
	if err := config.SetModel("explore", "openai/gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetLevel("explore", ai.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if err := config.SetLastUsed("main", "openai/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetLastUsed("advisor", "openai/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	reloaded := LoadAgentModelConfig(store)
	if got := reloaded.Ref("explore", ""); got != "openai/gpt-5.6-sol" {
		t.Fatalf("reloaded explore model = %q", got)
	}
	if got := reloaded.Level("explore", "openai/gpt-5.6-sol", ai.ThinkingMedium); got != ai.ThinkingHigh {
		t.Fatalf("reloaded explore level = %q", got)
	}
	if got := reloaded.Ref("main", ""); got != "openai/gpt-6-astra" {
		t.Fatalf("reloaded main model = %q", got)
	}
	// Only entry agents remember a Last Used model, so a subagent never pins
	// itself into settings.
	if got := reloaded.lastUsed["advisor"]; got != "" {
		t.Fatalf("advisor recorded a Last Used model: %q", got)
	}
	// Clearing a pin drops the agent's own level as well.
	if err := config.SetModel("explore", ""); err != nil {
		t.Fatal(err)
	}
	cleared := LoadAgentModelConfig(store)
	if cleared.Pinned("explore") || cleared.Level("explore", "openai/gpt-5.6-sol", ai.ThinkingMedium) != ai.ThinkingMedium {
		t.Fatalf("cleared explore = %#v", cleared)
	}
}

func TestAgentPanelListsEveryAgentWithItsModel(t *testing.T) {
	config := testAgentModels(t)
	if err := config.SetModel("explore", "openai/gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	catalog := []ai.Model{{Provider: "openai", ID: "gpt-6-astra", Name: "GPT-6 Astra", Reasoning: true}}
	options := AgentPanelOptions(profiles.Profiles(), "main", config, catalog)
	if len(options) != 5 {
		t.Fatalf("agent rows = %#v", options)
	}
	byValue := map[string]Option{}
	for _, option := range options {
		byValue[option.Value] = option
	}
	// The name column is padded so the model column lines up, and the row ends with
	// the agent's kind rather than repeating its description.
	nameWidth := tuitext.VisibleWidth("Advisor")
	if got := byValue["main"]; !strings.HasPrefix(got.Label, "Main"+strings.Repeat(" ", nameWidth-4)) ||
		!strings.HasPrefix(got.Description, "Default (Last used)") || !strings.HasSuffix(got.Description, "Primary") {
		t.Fatalf("main row = %#v", got)
	}
	if got := byValue["explore"]; !strings.Contains(got.Description, "GPT-6 Astra · medium") || !strings.HasSuffix(got.Description, "Subagent") {
		t.Fatalf("explore row = %#v", got)
	}
	// The utility helpers are configurable too, so their inherited default is
	// visible next to the interactive profiles, marked as utility agents.
	for _, name := range []string{"title", "summary"} {
		got := byValue[name]
		if !strings.Contains(got.Description, "Default (Inherit)") || !strings.HasSuffix(got.Description, "Utility") {
			t.Fatalf("%s row = %#v", name, got)
		}
	}
	// Every model column starts at the same offset.
	offsets := map[int]bool{}
	for _, option := range options {
		offsets[tuitext.VisibleWidth(option.Label)] = true
	}
	if len(offsets) != 1 {
		t.Fatalf("agent rows are not aligned: %#v", options)
	}
}

// TestConfiguredAgentModelAndReasoningApplyWithoutAPin: an agent defined in
// settings.json runs with the model and reasoning it names, while a choice made in
// /agents still wins over them.
func TestConfiguredAgentModelAndReasoningApplyWithoutAPin(t *testing.T) {
	if err := profiles.LoadCustom(map[string]json.RawMessage{
		"reviewer": json.RawMessage(`{"mode":"subagent","model":"openai/gpt-6-astra","reasoning":"high","parents":["main"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profiles.LoadCustom(nil) })

	store := midassettings.New(t.TempDir())
	config := LoadAgentModelConfig(store)
	if got := config.Ref("reviewer", "openai/other"); got != "openai/gpt-6-astra" {
		t.Fatalf("configured model = %q", got)
	}
	if got := config.Level("reviewer", "openai/gpt-6-astra", ai.ThinkingMedium); got != ai.ThinkingHigh {
		t.Fatalf("configured reasoning = %q", got)
	}
	// The model's own remembered level is only a fallback.
	if got := config.Level("main", "openai/gpt-6-astra", ai.ThinkingMedium); got != ai.ThinkingMedium {
		t.Fatalf("main reasoning = %q", got)
	}
	// A /agents pin outranks the definition's model, and clearing it falls back.
	if err := config.SetModel("reviewer", "openai/gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if got := config.Ref("reviewer", ""); got != "openai/gpt-5.6-sol" {
		t.Fatalf("pinned model = %q", got)
	}
	if err := config.SetModel("reviewer", ""); err != nil {
		t.Fatal(err)
	}
	if got := config.Ref("reviewer", ""); got != "openai/gpt-6-astra" {
		t.Fatalf("model after clearing the pin = %q", got)
	}
	// A subagent that names no model inherits its parent's.
	if got := config.Ref("advisor", "openai/inherited"); got != "openai/inherited" {
		t.Fatalf("inherited model = %q", got)
	}
	// The /agents row shows the resolved model and its level, not just the pin.
	catalog := []ai.Model{{Provider: "openai", ID: "gpt-6-astra", Name: "GPT-6 Astra", Reasoning: true}}
	if got, want := config.Display("reviewer", catalog), "GPT-6 Astra · high"; got != want {
		t.Fatalf("display = %q, want %q", got, want)
	}
	options := AgentPanelOptions(profiles.Profiles(), "main", config, catalog)
	for _, option := range options {
		if option.Value != "reviewer" {
			continue
		}
		if !strings.Contains(option.Description, "GPT-6 Astra · high") || !strings.HasSuffix(option.Description, "Subagent") {
			t.Fatalf("reviewer row = %#v", option)
		}
		return
	}
	t.Fatal("the configured agent is missing from /agents")
}
