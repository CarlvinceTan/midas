package cdp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Firefox is the one browser with no scripting dictionary, so it has no Apple
// Events route and needs its debugging endpoint instead. Unlike Chromium, Firefox
// takes that endpoint from a *preference*, so it can be enabled once and stays
// enabled across restarts with no launcher and no flag. That makes it the only
// browser Midas should automate the setup for.
const (
	firefoxBlockStart = "// Midas control: debugging endpoint (managed, do not edit by hand)"
	firefoxBlockEnd   = "// Midas control: end"
	// DefaultFirefoxPort is the port written when the caller does not choose one.
	DefaultFirefoxPort = 9222
)

// FirefoxProfiles lists the profile directories of an installed Firefox, read
// from profiles.ini. An empty result means Firefox is not installed or has never
// run.
func FirefoxProfiles(home string) ([]string, error) {
	if strings.TrimSpace(home) == "" {
		resolved, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = resolved
	}
	base := filepath.Join(home, "Library", "Application Support", "Firefox")
	data, err := os.ReadFile(filepath.Join(base, "profiles.ini"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("control: read Firefox profiles: %w", err)
	}
	profiles := []string{}
	for _, section := range strings.Split(string(data), "[") {
		path, isRelative := "", true
		for _, line := range strings.Split(section, "\n") {
			key, value, found := strings.Cut(strings.TrimSpace(line), "=")
			if !found {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "path":
				path = strings.TrimSpace(value)
			case "isrelative":
				isRelative = strings.TrimSpace(value) != "0"
			}
		}
		if path == "" {
			continue
		}
		// Firefox paths in profiles.ini are relative to the Firefox data dir.
		if isRelative {
			path = filepath.Join(base, path)
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			profiles = append(profiles, path)
		}
	}
	return profiles, nil
}

// EnableFirefoxEndpoint writes the preferences that give Firefox a debugging
// endpoint, into every profile it finds or the profile the caller names. It is
// idempotent and only ever touches its own block in user.js.
func EnableFirefoxEndpoint(profileDir string, port int) error {
	if port <= 0 {
		port = DefaultFirefoxPort
	}
	var lines []string
	lines = append(lines,
		`user_pref("devtools.debugger.remote-enabled", true);`,
		fmt.Sprintf(`user_pref("devtools.debugger.remote-port", %d);`, port),
		// Without this Firefox asks before accepting a debugger connection, which
		// an unattended agent cannot answer.
		`user_pref("devtools.debugger.prompt-connection", false);`,
	)
	return writeFirefoxBlock(profileDir, lines)
}

// DisableFirefoxEndpoint removes that block, leaving every other preference as it
// was, so the change is reversible.
func DisableFirefoxEndpoint(profileDir string) error {
	path := filepath.Join(profileDir, "user.js")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control: read %s: %w", path, err)
	}
	kept := []string{}
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), firefoxBlockStart):
			inBlock = true
			continue
		case inBlock && strings.HasPrefix(strings.TrimSpace(line), firefoxBlockEnd):
			inBlock = false
			continue
		case inBlock:
			continue
		}
		kept = append(kept, line)
	}
	trimmed := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if trimmed == "" {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(trimmed+"\n"), 0o600)
}

// writeFirefoxBlock replaces the managed block in a profile's user.js.
func writeFirefoxBlock(profileDir string, lines []string) error {
	if _, err := os.Stat(profileDir); err != nil {
		return fmt.Errorf("control: %s is not a Firefox profile", profileDir)
	}
	path := filepath.Join(profileDir, "user.js")
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control: read %s: %w", path, err)
	}

	// Keep every preference Midas does not own, in place.
	kept := []string{}
	inBlock := false
	for _, line := range strings.Split(existing, "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), firefoxBlockStart):
			inBlock = true
			continue
		case inBlock && strings.HasPrefix(strings.TrimSpace(line), firefoxBlockEnd):
			inBlock = false
			continue
		case inBlock:
			continue
		}
		kept = append(kept, line)
	}
	body := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	block := firefoxBlockStart + "\n" + strings.Join(lines, "\n") + "\n" + firefoxBlockEnd
	if body != "" {
		block = body + "\n\n" + block
	}
	return os.WriteFile(path, []byte(block+"\n"), 0o600)
}
