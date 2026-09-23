package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVaultSkillMatchesTheVaultServer checks the skill against the tools the
// server actually exposes, and against the rules that keep secrets out of
// transcripts.
func TestVaultSkillMatchesTheVaultServer(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repository, "skills", "vault", "SKILL.md")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("vault skill is missing: %v", err)
	}
	text := string(contents)

	installed := filepath.Join(t.TempDir(), ".agents", "skills", "vault")
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	entries := Discover(filepath.Dir(filepath.Dir(filepath.Dir(installed))), filepath.Join(t.TempDir(), "config"))
	if len(entries) != 1 || entries[0].Name != "vault" {
		t.Fatalf("discovered = %#v", entries)
	}

	// The workflow tools it names have to be the real ones.
	for _, tool := range []string{
		"vault_unlock", "vault_lock", "vault_list", "vault_password",
		"vault_totp", "vault_recovery_codes", "vault_put",
	} {
		if !strings.Contains(text, tool) {
			t.Errorf("the vault skill never mentions %s", tool)
		}
	}
	// And the rules that matter, each one asserted so it cannot quietly vanish.
	for _, rule := range []string{
		"never written anywhere",
		"how long it stays valid",
		"never echo a password",
		"Ask before writing",
		"per session",
	} {
		if !strings.Contains(text, rule) {
			t.Errorf("the vault skill does not state %q", rule)
		}
	}
}
