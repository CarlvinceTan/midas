package profiles

import (
	"encoding/json"
	"testing"
)

// TestMaxDepthComesFromTheDefinitionOrDefault: built-ins cap at the default, and a
// definition can raise or lower the cap for its own subtree.
func TestMaxDepthComesFromTheDefinitionOrDefault(t *testing.T) {
	if got := MaxDepth("main"); got != DefaultMaxDepth {
		t.Fatalf("main max depth = %d", got)
	}
	if err := loadCustomJSON(t, `{
	  "shallow": {"mode": "subagent", "maxDepth": 0},
	  "deep": {"mode": "primary", "maxDepth": 5}
	}`); err != nil {
		t.Fatal(err)
	}
	if got := MaxDepth("shallow"); got != DefaultMaxDepth {
		t.Fatalf("shallow max depth = %d", got)
	}
	if got := MaxDepth("deep"); got != 5 {
		t.Fatalf("deep max depth = %d", got)
	}
	if got := MaxDepth("missing"); got != DefaultMaxDepth {
		t.Fatalf("unknown agent max depth = %d", got)
	}
}

// TestCanEnterCanDelegateAndCanDelegateTo pins the mode rules that the command
// line, /agents, and the task tool all enforce.
func TestCanEnterCanDelegateAndCanDelegateTo(t *testing.T) {
	if err := loadCustomJSON(t, `{
	  "planner": {"mode": "primary"},
	  "reviewer": {"mode": "subagent"},
	  "counsel": {"mode": "utility"},
	  "locked": {"mode": "subagent", "tools": "read-only"},
	  "silent": {"mode": "subagent", "tools": "none"}
	}`); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"main": true, "planner": true, "advisor": false, "reviewer": false, "counsel": false, "title": false} {
		if got := CanEnter(name); got != want {
			t.Fatalf("CanEnter(%s) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{"main": true, "planner": true, "reviewer": true, "advisor": false, "locked": false, "silent": false, "title": false, "summary": false} {
		if got := CanDelegate(name); got != want {
			t.Fatalf("CanDelegate(%s) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{"reviewer": true, "advisor": true, "locked": true, "main": false, "planner": false, "counsel": false} {
		if got := CanDelegateTo(name); got != want {
			t.Fatalf("CanDelegateTo(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestDefinitionDefaultsKeepBuiltInBehaviour: a definition that omits maxDepth
// behaves like a built-in.
func TestDefinitionDefaultsKeepBuiltInBehaviour(t *testing.T) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"helper": {"mode": "subagent"}}`), &entries); err != nil {
		t.Fatal(err)
	}
	if err := LoadCustom(entries); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { customProfiles = map[string]Profile{} })
	if got := MaxDepth("helper"); got != DefaultMaxDepth {
		t.Fatalf("helper max depth = %d", got)
	}
	if !CanDelegateTo("helper") || CanEnter("helper") || !CanDelegate("helper") {
		t.Fatal("a plain subagent has the wrong mode rules")
	}
}
