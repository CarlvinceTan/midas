package profiles

import (
	"strings"
	"testing"
)

func TestEntryAgentsDefaultToTheirLastUsedModel(t *testing.T) {
	for _, name := range []string{"main"} {
		if !IsEntryAgent(name) {
			t.Fatalf("%s is not an entry agent", name)
		}
		if got, want := DefaultModelLabel(name), "Default (Last used)"; got != want {
			t.Fatalf("%s default label = %q, want %q", name, got, want)
		}
		if parents := ParentAgents(name); len(parents) != 0 {
			t.Fatalf("%s inherits from %#v", name, parents)
		}
	}
	if got := EntryAgents(); strings.Join(got, ",") != "main" {
		t.Fatalf("entry agents = %#v", got)
	}
}

func TestSubagentsInheritTheirCallersModel(t *testing.T) {
	for _, test := range []struct {
		name    string
		parents []string
		label   string
	}{
		{name: "advisor", parents: []string{"main"}, label: "Default (Inherit)"},
		{name: "explore", parents: []string{"main"}, label: "Default (Inherit)"},
		{name: "title", parents: []string{"main"}, label: "Default (Inherit)"},
		{name: "summary", parents: []string{"main"}, label: "Default (Inherit)"},
	} {
		if got, want := strings.Join(ParentAgents(test.name), ","), strings.Join(test.parents, ","); got != want {
			t.Fatalf("%s parents = %q, want %q", test.name, got, want)
		}
		if got, want := DefaultModelLabel(test.name), test.label; got != want {
			t.Fatalf("%s default label = %q, want %q", test.name, got, want)
		}
		if IsEntryAgent(test.name) {
			t.Fatalf("%s counted as an entry agent", test.name)
		}
	}
}

func TestParentAgentsCopiesAndIgnoresUnknownNames(t *testing.T) {
	parents := ParentAgents("advisor")
	parents[0] = "mutated"
	if got := ParentAgents("advisor"); len(got) != 1 || got[0] != "main" {
		t.Fatalf("parent agents leaked a mutation: %#v", got)
	}
	if got := ParentAgents("missing"); len(got) != 0 {
		t.Fatalf("unknown agent parents = %#v", got)
	}
	if got, want := DefaultModelLabel("missing"), "Default"; got != want {
		t.Fatalf("unknown agent label = %q, want %q", got, want)
	}
}
