package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverUsesMidasAndProjectSkillDirectories(t *testing.T) {
	root, config := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(config, "skills", "notion"), "# notion")
	writeSkill(t, filepath.Join(config, "skills", "career"), "---\nname: job-search\ndescription: jobs\n---\n# Career")
	writeSkill(t, filepath.Join(root, ".agents", "skills", "research"), "# research")
	writeSkill(t, filepath.Join(root, ".midas", "skills", "notion-local"), "---\nname: notion\n---\n# local wins")
	if err := os.MkdirAll(filepath.Join(config, "skills", "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries := Discover(root, config)
	if len(entries) != 3 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Name != "job-search" || entries[1].Name != "notion" || entries[2].Name != "research" {
		t.Fatalf("names = %#v", entries)
	}
	if entries[1].Scope != ScopeLocal || !strings.Contains(entries[1].Path, "notion-local") {
		t.Fatalf("local precedence = %#v", entries[1])
	}
}

func TestAppendPromptListsInstructionFiles(t *testing.T) {
	prompt := AppendPrompt("base", []Entry{{Name: "research", Path: "/skills/research"}})
	if !strings.Contains(prompt, "base") || !strings.Contains(prompt, "research: /skills/research/SKILL.md") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func writeSkill(t *testing.T, directory, content string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
