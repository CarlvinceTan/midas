// Package instructions discovers and composes Midas-owned agent instruction
// files. More specific project files are appended after global instructions so
// they take precedence when instructions conflict.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const FileName = "AGENTS.md"

// candidateNames is the lookup order inside one directory. The first existing
// file wins, so AGENTS.override.md masks AGENTS.md and the CLAUDE.md fallbacks
// only apply to a directory that has no AGENTS file at all.
var candidateNames = []string{"AGENTS.override.md", FileName, "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Entry is one loaded instruction file in effective precedence order.
type Entry struct {
	Path    string
	Content string
	Scope   Scope
}

// Load reads the global Midas instructions first, followed by one instruction
// file per directory from the filesystem root down to the working directory, so
// the working directory's own file lands last and takes precedence. Missing
// files are ignored; an existing file that cannot be read is reported instead
// of silently dropping user instructions.
func Load(root, configDir string) ([]Entry, error) {
	type candidate struct {
		path  string
		scope Scope
	}
	candidates := make([]candidate, 0, 8)
	if configDir = strings.TrimSpace(configDir); configDir != "" {
		path, found, err := firstInstructionFile(configDir)
		if err != nil {
			return nil, err
		}
		if found {
			candidates = append(candidates, candidate{path: path, scope: ScopeGlobal})
		}
	}
	if root = strings.TrimSpace(root); root != "" {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("instructions: resolve %s: %w", root, err)
		}
		// Walk from the working directory up to the filesystem root, then emit
		// the matches in reverse so the most general file comes first.
		found := make([]string, 0, 4)
		for directory := filepath.Clean(absolute); ; {
			path, exists, err := firstInstructionFile(directory)
			if err != nil {
				return nil, err
			}
			if exists {
				found = append(found, path)
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
		for index := len(found) - 1; index >= 0; index-- {
			candidates = append(candidates, candidate{path: found[index], scope: ScopeProject})
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	entries := make([]Entry, 0, len(candidates))
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate.path)
		if err != nil {
			return nil, fmt.Errorf("instructions: resolve %s: %w", candidate.path, err)
		}
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("instructions: read %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("instructions: inspect %s: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("instructions: %s is a directory", path)
		}
		seen[path] = struct{}{}
		entries = append(entries, Entry{Path: path, Content: strings.TrimSpace(string(data)), Scope: candidate.scope})
	}
	return entries, nil
}

// firstInstructionFile returns the first instruction candidate that exists in
// directory, in candidateNames order. A file that exists but is a directory is
// reported as an error rather than skipped.
func firstInstructionFile(directory string) (string, bool, error) {
	for _, name := range candidateNames {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("instructions: inspect %s: %w", path, err)
		}
		if info.IsDir() {
			return "", false, fmt.Errorf("instructions: %s is a directory", path)
		}
		return path, true, nil
	}
	return "", false, nil
}

// AppendPrompt appends loaded instruction files to an agent profile prompt.
func AppendPrompt(base string, entries []Entry) string {
	if len(entries) == 0 {
		return base
	}
	var prompt strings.Builder
	prompt.WriteString(strings.TrimSpace(base))
	prompt.WriteString("\n\nMidas instruction files are listed from general to specific. Follow all of them; when they conflict, the later file takes precedence.\n")
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		fmt.Fprintf(&prompt, "\n<instructions path=%q>\n%s\n</instructions>\n", entry.Path, content)
	}
	return strings.TrimSpace(prompt.String())
}

// DisplayPaths formats loaded paths for the TUI startup summary.
func DisplayPaths(entries []Entry, root string) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, displayPath(entry.Path, root))
	}
	return result
}

// displayPath shortens an instruction path for display. Paths are shown
// relative to the working directory, but a file at or under the home directory
// keeps the home-relative "~" form when the working directory is the home
// directory itself, so ~/.midas/AGENTS.md never collapses to ./.midas/AGENTS.md.
func displayPath(path, root string) string {
	home := homeDir()
	if !sameDir(root, home) {
		if relative, ok := containedRelative(root, path); ok {
			return "." + string(filepath.Separator) + relative
		}
	}
	if relative, ok := containedRelative(home, path); ok {
		return "~" + string(filepath.Separator) + relative
	}
	return path
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Clean(home)
}

func sameDir(base, path string) bool {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	return filepath.Clean(base) == filepath.Clean(path)
}

func containedRelative(base, path string) (string, bool) {
	if strings.TrimSpace(base) == "" {
		return "", false
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}
