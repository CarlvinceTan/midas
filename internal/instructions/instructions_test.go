package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsGlobalThenAncestorInstructionsInPrecedenceOrder(t *testing.T) {
	base := t.TempDir()
	config := filepath.Join(base, "home", ".midas")
	root := filepath.Join(base, "project", "nested")
	writeInstruction(t, filepath.Join(config, FileName), "global")
	writeInstruction(t, filepath.Join(base, FileName), "outer")
	writeInstruction(t, filepath.Join(base, "project", "CLAUDE.md"), "middle")
	writeInstruction(t, filepath.Join(root, FileName), "inner")

	entries, err := Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %#v", entries)
	}
	want := []string{"global", "outer", "middle", "inner"}
	for index, content := range want {
		if entries[index].Content != content {
			t.Fatalf("precedence = %#v", entries)
		}
	}
	if entries[0].Scope != ScopeGlobal {
		t.Fatalf("global scope = %#v", entries[0])
	}
	for _, entry := range entries[1:] {
		if entry.Scope != ScopeProject {
			t.Fatalf("project scope = %#v", entry)
		}
	}

	prompt := AppendPrompt("base profile", entries)
	positions := make([]int, len(want))
	for index, content := range want {
		positions[index] = strings.Index(prompt, content)
		if positions[index] < 0 {
			t.Fatalf("prompt is missing %q: %q", content, prompt)
		}
	}
	if !strings.Contains(prompt, "base profile") {
		t.Fatalf("prompt = %q", prompt)
	}
	for index := 1; index < len(positions); index++ {
		if positions[index-1] >= positions[index] {
			t.Fatalf("prompt order = %v in %q", positions, prompt)
		}
	}
}

func TestLoadPrefersAGENTSThenCLAUDEWithinADirectory(t *testing.T) {
	base := t.TempDir()
	config := filepath.Join(base, "config")
	root := filepath.Join(base, "project")
	writeInstruction(t, filepath.Join(config, FileName), "global")

	// CLAUDE.md alone in a directory is picked up...
	writeInstruction(t, filepath.Join(root, "CLAUDE.md"), "claude")
	entries, err := Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Content != "claude" {
		t.Fatalf("CLAUDE.md fallback = %#v", entries)
	}

	// ...but AGENTS.md masks it, and AGENTS.override.md masks AGENTS.md.
	writeInstruction(t, filepath.Join(root, FileName), "agents")
	entries, err = Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Content != "agents" {
		t.Fatalf("AGENTS.md precedence = %#v", entries)
	}
	writeInstruction(t, filepath.Join(root, "AGENTS.override.md"), "override")
	entries, err = Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Content != "override" {
		t.Fatalf("AGENTS.override.md precedence = %#v", entries)
	}
}

func TestLoadIgnoresMissingFilesAndDeduplicatesTheGlobalFile(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, ".midas")
	writeInstruction(t, filepath.Join(config, FileName), "shared")

	entries, err := Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "shared" {
		t.Fatalf("entries = %#v", entries)
	}

	// A config directory that is also the working directory resolves to one
	// file, which the global scope already claimed.
	writeInstruction(t, filepath.Join(root, FileName), "local")
	entries, err = Load(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "local" || entries[0].Scope != ScopeGlobal {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestDisplayPathsKeepsHomeRelativeFormWhenRootIsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".midas")
	writeInstruction(t, filepath.Join(config, FileName), "global")

	entries, err := Load(home, config)
	if err != nil {
		t.Fatal(err)
	}
	paths := DisplayPaths(entries, home)
	want := "~" + string(filepath.Separator) + filepath.Join(".midas", FileName)
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("display paths = %#v", paths)
	}
}

func TestDisplayPathsPrefersProjectRelativeFormInsideHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".midas")
	root := filepath.Join(home, "project")
	writeInstruction(t, filepath.Join(config, FileName), "global")
	writeInstruction(t, filepath.Join(root, FileName), "project")

	entries, err := Load(root, config)
	if err != nil {
		t.Fatal(err)
	}
	paths := DisplayPaths(entries, root)
	wantGlobal := "~" + string(filepath.Separator) + filepath.Join(".midas", FileName)
	wantProject := "." + string(filepath.Separator) + FileName
	if len(paths) != 2 || paths[0] != wantGlobal || paths[1] != wantProject {
		t.Fatalf("display paths = %#v", paths)
	}
}

func writeInstruction(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
