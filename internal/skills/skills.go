// Package skills discovers Midas-native skill directories and exposes their
// instruction files to the agent runtime without depending on Pi or OpenCode.
package skills

import (
	"bufio"
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Scope string

const (
	ScopeGlobal Scope = "global"
	ScopeLocal  Scope = "local"
)

// Entry is one discoverable skill backed by a SKILL.md file.
type Entry struct {
	Name  string
	Path  string
	Scope Scope
}

// Discover lists project Midas skills, global Midas skills, and project agent
// skills. The first skill with a given name wins, matching the legacy Midas
// precedence while ignoring unreadable or incomplete directories.
func Discover(root, configDir string) []Entry {
	directories := []struct {
		path  string
		scope Scope
	}{
		{filepath.Join(root, ".midas", "skills"), ScopeLocal},
		{filepath.Join(configDir, "skills"), ScopeGlobal},
		{filepath.Join(root, ".agents", "skills"), ScopeLocal},
	}
	byName := make(map[string]Entry)
	for _, directory := range directories {
		children, err := os.ReadDir(directory.path)
		if err != nil {
			continue
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), ".") || !child.IsDir() {
				continue
			}
			instruction := filepath.Join(directory.path, child.Name(), "SKILL.md")
			if info, err := os.Stat(instruction); err != nil || info.IsDir() {
				continue
			}
			name := skillName(instruction, child.Name())
			if _, exists := byName[name]; exists {
				continue
			}
			byName[name] = Entry{Name: name, Path: filepath.Dir(instruction), Scope: directory.scope}
		}
	}
	result := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		result = append(result, entry)
	}
	slices.SortFunc(result, func(a, b Entry) int { return cmp.Compare(a.Name, b.Name) })
	return result
}

func skillName(path, fallback string) string {
	file, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	// Front matter is small, but a long description is not impossible: the buffer is
	// raised so a line that does not fit cannot silently truncate the name.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return fallback
	}
	name := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			if name != "" {
				return name
			}
			return fallback
		}
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "name") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if value != "" {
			name = value
		}
	}
	return fallback
}

// AppendPrompt tells the native agent which skill files it may load on demand.
func AppendPrompt(base string, entries []Entry) string {
	if len(entries) == 0 {
		return base
	}
	var prompt strings.Builder
	prompt.WriteString(strings.TrimSpace(base))
	prompt.WriteString("\n\nAvailable Midas skills:\n")
	for _, entry := range entries {
		fmt.Fprintf(&prompt, "- %s: %s\n", entry.Name, filepath.Join(entry.Path, "SKILL.md"))
	}
	prompt.WriteString("When a request clearly matches a skill, read its complete SKILL.md before acting and follow it within the user's request.")
	return prompt.String()
}
