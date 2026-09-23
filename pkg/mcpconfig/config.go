// Package mcpconfig locates the settings an MCP server keeps for itself.
//
// A server started on its own — by another agent, or by hand — keeps its own
// file beside the agent's config, such as hub.json or vault.json. When Midas
// launches a server it points MIDAS_MCP_SETTINGS at its settings.json instead, so
// everything the user configured lives in one file: the launch list under
// "mcpServers" and each server's own settings under "mcp".
package mcpconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SettingsFile is the agent's own configuration file, which holds the MCP
	// sections when Midas is the launcher.
	SettingsFile = "settings.json"
	// OverrideEnv tells a server to read its settings from a shared file. Midas
	// sets it for every server it launches; a server started any other way ignores
	// it and keeps its own file.
	OverrideEnv = "MIDAS_MCP_SETTINGS"
	// Section is the top-level object that holds each server's own settings.
	Section = "mcp"
)

// SettingsPath is where the agent's settings file lives: the config directory,
// which is MIDAS_CONFIG_DIR or ~/.midas.
func SettingsPath(getenv func(string) string) string {
	return filepath.Join(ConfigDir(getenv), SettingsFile)
}

// ConfigDir returns the agent's config directory.
func ConfigDir(getenv func(string) string) string {
	if directory := strings.TrimSpace(getenv("MIDAS_CONFIG_DIR")); directory != "" {
		return directory
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".midas"
	}
	return filepath.Join(home, ".midas")
}

// Override returns the shared settings file a launcher pointed this process at,
// or "" when the server is running on its own.
func Override(getenv func(string) string) string {
	return strings.TrimSpace(getenv(OverrideEnv))
}

// Location returns where one server keeps its settings and the keys that address
// them inside that file. Keys are nil when the server owns the whole file, which
// is the case whenever it was not started by Midas.
func Location(getenv func(string) string, name string) (path string, keys []string) {
	if override := Override(getenv); override != "" {
		return override, []string{Section, name}
	}
	return filepath.Join(ConfigDir(getenv), name+".json"), nil
}

// LegacyPath is the server's own file, which is where settings lived before the
// merge and which still holds them for every other agent.
func LegacyPath(getenv func(string) string, name string) string {
	return filepath.Join(ConfigDir(getenv), name+".json")
}

// Read returns the settings object at keys, or nil when the file or the key is
// absent. Keys address nested objects; nil keys mean the whole file is the
// object.
func Read(path string, keys []string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcpconfig: read %s: %w", path, err)
	}
	var root json.RawMessage = data
	for _, key := range keys {
		if len(root) == 0 {
			return nil, nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(root, &object); err != nil {
			return nil, fmt.Errorf("mcpconfig: %s: %w", path, err)
		}
		next, ok := object[key]
		if !ok {
			return nil, nil
		}
		root = next
	}
	return root, nil
}

// Update replaces the object at keys, preserving every other key in the file, and
// writes it back atomically with owner-only permissions. mutate receives the
// existing object, which may be nil, and returns the replacement; returning nil
// removes the key.
func Update(path string, keys []string, mutate func(existing json.RawMessage) (any, error)) error {
	if len(keys) == 0 {
		replacement, err := mutate(nil)
		if err != nil {
			return err
		}
		return writeFile(path, replacement)
	}
	root, err := readObject(path)
	if err != nil {
		return err
	}
	existing := lookup(root, keys)
	replacement, err := mutate(existing)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(replacement)
	if err != nil {
		return err
	}
	if replacement == nil {
		deleteNested(root, keys)
	} else if err := storeNested(root, keys, encoded); err != nil {
		return err
	}
	return writeFile(path, root)
}

func readObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mcpconfig: read %s: %w", path, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("mcpconfig: decode %s: %w", path, err)
	}
	if object == nil {
		object = map[string]json.RawMessage{}
	}
	return object, nil
}

func lookup(root map[string]json.RawMessage, keys []string) json.RawMessage {
	current := root
	for index, key := range keys {
		value, ok := current[key]
		if !ok {
			return nil
		}
		if index == len(keys)-1 {
			return value
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(value, &nested) != nil {
			return nil
		}
		current = nested
	}
	return nil
}

func storeNested(root map[string]json.RawMessage, keys []string, value json.RawMessage) error {
	if len(keys) == 1 {
		root[keys[0]] = value
		return nil
	}
	var nested map[string]json.RawMessage
	if existing, ok := root[keys[0]]; ok {
		if err := json.Unmarshal(existing, &nested); err != nil {
			return fmt.Errorf("mcpconfig: %q is not an object", keys[0])
		}
	}
	if nested == nil {
		nested = map[string]json.RawMessage{}
	}
	if err := storeNested(nested, keys[1:], value); err != nil {
		return err
	}
	encoded, err := json.Marshal(nested)
	if err != nil {
		return err
	}
	root[keys[0]] = encoded
	return nil
}

func deleteNested(root map[string]json.RawMessage, keys []string) {
	if len(keys) == 1 {
		delete(root, keys[0])
		return
	}
	var nested map[string]json.RawMessage
	if existing, ok := root[keys[0]]; ok {
		if json.Unmarshal(existing, &nested) != nil {
			return
		}
	}
	if nested == nil {
		return
	}
	deleteNested(nested, keys[1:])
	if len(nested) == 0 {
		delete(root, keys[0])
		return
	}
	if encoded, err := json.Marshal(nested); err == nil {
		root[keys[0]] = encoded
	}
}

// writeFile replaces a file atomically with owner-only permissions.
func writeFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("mcpconfig: create %s: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("mcpconfig: create a temporary file in %s: %w", directory, err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("mcpconfig: write %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("mcpconfig: sync %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("mcpconfig: close %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("mcpconfig: replace %s: %w", path, err)
	}
	return nil
}
