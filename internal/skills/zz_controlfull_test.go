package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFullControlSkillKeepsTheDoctrineAndTheSetupOut checks the ported skill on the
// two things that matter: the coordination doctrine survives, and the setup
// instructions that used to be injected into AGENTS.md are gone.
func TestFullControlSkillKeepsTheDoctrineAndTheSetupOut(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repository, "skills", "control", "SKILL.md")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("full control skill is missing: %v", err)
	}
	text := string(contents)

	installed := filepath.Join(t.TempDir(), ".agents", "skills", "control")
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	entries := Discover(filepath.Dir(filepath.Dir(filepath.Dir(installed))), filepath.Join(t.TempDir(), "config"))
	if len(entries) != 1 || entries[0].Name != "control" {
		t.Fatalf("discovered = %#v", entries)
	}

	// The doctrine that makes acting safe has to be present.
	for _, phrase := range []string{
		"lease", "action fence", "Observation is lock-free", "human attention",
		"Never replay an uncertain action", "embedded browser",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the skill is missing %q", phrase)
		}
	}
	// So do the tiers and the read-only rule for observation.
	for _, phrase := range []string{
		"never changes a setting", "Accessibility", "Screen Recording", "debug endpoint",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the skill does not cover %q", phrase)
		}
	}
	// The full server's capabilities appear here and only here.
	for _, phrase := range []string{"--user-data-dir", "user.js", "CDP"} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the full skill does not mention %q", phrase)
		}
	}
	// No bootstrap into another instruction file: that was the part to drop.
	for _, forbidden := range []string{"AGENTS.md", "Add this to your", "inject"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the skill still carries setup text (%q)", forbidden)
		}
	}
}
