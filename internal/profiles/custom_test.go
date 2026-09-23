package profiles

import (
	"encoding/json"
	"testing"
)

func loadCustomJSON(t *testing.T, document string) error {
	t.Helper()
	t.Cleanup(func() { customProfiles = map[string]Profile{} })
	var entries map[string]json.RawMessage
	if err := json.Unmarshal([]byte(document), &entries); err != nil {
		t.Fatal(err)
	}
	return LoadCustom(entries)
}

// TestCustomAgentIsResolvableAndKeepsItsConfiguration covers the shape a user
// writes in settings.json.
func TestCustomAgentIsResolvableAndKeepsItsConfiguration(t *testing.T) {
	err := loadCustomJSON(t, `{
	  "reviewer": {
	    "description": "Reviews diffs for correctness.",
	    "mode": "subagent",
	    "model": "openai/gpt-5.6-sol",
	    "reasoning": "high",
	    "prompt": "You review diffs.",
	    "tools": "read-only",
	    "parents": ["explore"],
	    "maxDepth": 1
	  },
	  "planner": {"mode": "primary", "description": "Plans work."}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	reviewer, err := ResolveProfile("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.Mode != ModeSubagent || reviewer.SystemPrompt != "You review diffs." || !reviewer.ReadOnly {
		t.Fatalf("reviewer = %#v", reviewer)
	}
	if ConfiguredModel("reviewer") != "openai/gpt-5.6-sol" || ConfiguredReasoning("reviewer") != "high" {
		t.Fatalf("reviewer defaults = %q / %q", ConfiguredModel("reviewer"), ConfiguredReasoning("reviewer"))
	}
	if got := ParentAgents("reviewer"); len(got) != 1 || got[0] != "explore" {
		t.Fatalf("reviewer parents = %#v", got)
	}
	if reviewer.MaxDepth != 1 {
		t.Fatalf("reviewer max depth = %d", reviewer.MaxDepth)
	}
	if IsEntryAgent("reviewer") || IsUtilityAgent("reviewer") {
		t.Fatal("a subagent was treated as an entry or utility agent")
	}
	if got, want := DefaultModelLabel("reviewer"), "Default (Inherit)"; got != want {
		t.Fatalf("reviewer label = %q, want %q", got, want)
	}
	if ToolsMode("reviewer") != ToolsReadOnly {
		t.Fatalf("reviewer tools = %q", ToolsMode("reviewer"))
	}

	// A primary agent is an entry point and defaults to its last used model.
	planner, err := ResolveProfile("planner")
	if err != nil {
		t.Fatal(err)
	}
	if planner.Mode != ModePrimary || !IsEntryAgent("planner") {
		t.Fatalf("planner = %#v", planner)
	}
	if got, want := DefaultModelLabel("planner"), "Default (Last used)"; got != want {
		t.Fatalf("planner label = %q, want %q", got, want)
	}
	if planner.MaxDepth != DefaultMaxDepth {
		t.Fatalf("planner max depth = %d, want the default", planner.MaxDepth)
	}
	if planner.SystemPrompt == "" {
		t.Fatal("a prompt-less agent got an empty system prompt")
	}
	// Custom agents are listed with the built-ins.
	names := map[string]bool{}
	for _, profile := range Profiles() {
		names[profile.Name] = true
	}
	for _, name := range []string{"main", "advisor", "planner", "reviewer"} {
		if !names[name] {
			t.Fatalf("%s missing from the catalog: %#v", name, names)
		}
	}
}

// TestCustomAgentValidationRejectsMistakesAtStartup keeps a typo from silently
// producing a broken agent.
func TestCustomAgentValidationRejectsMistakesAtStartup(t *testing.T) {
	for name, document := range map[string]string{
		"built-in name":    `{"main": {"mode": "primary"}}`,
		"bad mode":         `{"x": {"mode": "helper"}}`,
		"bad tools":        `{"x": {"tools": "some"}}`,
		"bad reasoning":    `{"x": {"reasoning": "very-high"}}`,
		"negative depth":   `{"x": {"maxDepth": -1}}`,
		"unknown parent":   `{"x": {"parents": ["nobody"]}}`,
		"invalid name":     `{"a b": {}}`,
		"not an object":    `{"x": 3}`,
		"empty agent name": `{"  ": {}}`,
	} {
		if err := loadCustomJSON(t, document); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	// A definition that names no parent inherits from main, like the built-ins.
	if err := loadCustomJSON(t, `{"x": {"mode": "subagent"}}`); err != nil {
		t.Fatal(err)
	}
	if got := ParentAgents("x"); len(got) != 1 || got[0] != "main" {
		t.Fatalf("default parents = %#v", got)
	}
	// Loading an empty configuration clears the registry again: a user who removes
	// an agent in settings.json must not keep it for the rest of the process.
	if err := loadCustomJSON(t, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProfile("x"); err == nil {
		t.Fatal("a removed agent is still resolvable")
	}
}
