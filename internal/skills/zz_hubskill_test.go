package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHubSkillIsDiscoverableAndComplete checks the shipped hub skill against what
// the hub actually exposes, so the documentation cannot drift from the tools.
func TestHubSkillIsDiscoverableAndComplete(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repository, "skills", "hub", "SKILL.md")
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("hub skill is missing: %v", err)
	}
	// Discovery reads <root>/.agents/skills, so the shipped skill is installed by
	// copying it there or into the global skills directory.
	installed := t.TempDir()
	if err := os.MkdirAll(filepath.Join(installed, ".agents", "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installed, ".agents", "skills", "hub"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, ".agents", "skills", "hub", "SKILL.md"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	entries := Discover(installed, filepath.Join(installed, "config"))
	if len(entries) != 1 || entries[0].Name != "hub" {
		t.Fatalf("discovered = %#v", entries)
	}

	// Every tool the hub serves has to be mentioned, or the skill is incomplete.
	text := string(contents)
	for _, tool := range []string{
		"hub_bridges", "hub_accounts", "hub_threads", "hub_messages",
		"hub_send", "hub_mark_read", "hub_login",
	} {
		if !strings.Contains(text, tool) {
			t.Errorf("the hub skill never mentions %s", tool)
		}
	}
	// And the behaviours that are easy to get wrong.
	for _, phrase := range []string{"render", "Nothing is attached by default", "Subscribe"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the hub skill does not cover %q", phrase)
		}
	}
}
