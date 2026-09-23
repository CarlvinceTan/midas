package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func testEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// TestLocationFollowsTheLauncher: a server Midas started reads the shared
// settings file, and one started any other way keeps its own file.
func TestLocationFollowsTheLauncher(t *testing.T) {
	base := t.TempDir()
	standalone := testEnv(map[string]string{"MIDAS_CONFIG_DIR": base})
	path, keys := Location(standalone, "hub")
	if path != filepath.Join(base, "hub.json") || keys != nil {
		t.Fatalf("standalone location = %q, %v", path, keys)
	}
	shared := filepath.Join(t.TempDir(), "settings.json")
	launched := testEnv(map[string]string{"MIDAS_CONFIG_DIR": base, OverrideEnv: shared})
	path, keys = Location(launched, "hub")
	if path != shared || len(keys) != 2 || keys[0] != Section || keys[1] != "hub" {
		t.Fatalf("launched location = %q, %v", path, keys)
	}
	if SettingsPath(standalone) != filepath.Join(base, SettingsFile) {
		t.Fatalf("settings path = %q", SettingsPath(standalone))
	}
}

// TestUpdatePreservesOtherSections is the property the merge depends on: a
// server writing its own settings must not disturb the agent's.
func TestUpdatePreservesOtherSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	initial := `{
  "currency": "AUD",
  "agentModels": {"main": "openai/gpt-5.6-sol"},
  "mcp": {"control": {"token": "abc"}},
  "mcpServers": {"hub": {"command": ["hub"]}}
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Update(path, []string{Section, "hub"}, func(existing json.RawMessage) (any, error) {
		return map[string]any{"listen": "127.0.0.1:8765", "bridges": map[string]any{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"currency", "agentModels", "mcpServers"} {
		if _, ok := stored[key]; !ok {
			t.Fatalf("%s was dropped by the update: %s", key, data)
		}
	}
	// The sibling server's token survives, and the new section is readable back.
	control, err := Read(path, []string{Section, "control"})
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(control, &token); err != nil || token.Token != "abc" {
		t.Fatalf("control settings = %s, %v", control, err)
	}
	hub, err := Read(path, []string{Section, "hub"})
	if err != nil {
		t.Fatal(err)
	}
	var listen struct {
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal(hub, &listen); err != nil || listen.Listen != "127.0.0.1:8765" {
		t.Fatalf("hub settings = %s, %v", hub, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("settings mode = %o", mode)
	}
}

// TestUpdateCreatesMissingFilesAndSections covers first use: no file, no section.
func TestUpdateCreatesMissingFilesAndSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	err := Update(path, []string{Section, "vault"}, func(existing json.RawMessage) (any, error) {
		if existing != nil {
			t.Fatalf("expected no existing settings, got %s", existing)
		}
		return map[string]any{"token": "generated"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Read(path, []string{Section, "vault"})
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(stored, &settings); err != nil || settings.Token != "generated" {
		t.Fatalf("stored = %s, %v", stored, err)
	}
}

// TestReadMissingKeyIsNotAnError keeps startup quiet for servers that have never
// been configured.
func TestReadMissingKeyIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"currency":"AUD"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{{Section, "hub"}, {"mcpServers"}} {
		value, err := Read(path, keys)
		if err != nil || value != nil {
			t.Fatalf("Read(%v) = %s, %v", keys, value, err)
		}
	}
	if value, err := Read(filepath.Join(t.TempDir(), "absent.json"), nil); err != nil || value != nil {
		t.Fatalf("Read of a missing file = %s, %v", value, err)
	}
}

// TestUpdateWithNoKeysReplacesTheWholeFile covers servers that own their file.
func TestUpdateWithNoKeysReplacesTheWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	if err := Update(path, nil, func(json.RawMessage) (any, error) {
		return map[string]any{"token": "abc", "bridges": map[string]any{}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	value, err := Read(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(value, &config); err != nil {
		t.Fatal(err)
	}
	if config["token"] != "abc" {
		t.Fatalf("stored = %s", value)
	}
}
