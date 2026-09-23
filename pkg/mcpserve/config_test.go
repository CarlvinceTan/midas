package mcpserve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// TestTokenStaysInTheServerFileWhenNoAgentLaunchedIt: another agent's server keeps
// its own configuration, byte for byte as before the merge.
func TestTokenStaysInTheServerFileWhenNoAgentLaunchedIt(t *testing.T) {
	directory := t.TempDir()
	getenv := testEnv(map[string]string{"MIDAS_CONFIG_DIR": directory})
	options := Options{Name: "vault"}
	token, err := ensureToken(getenv, options)
	if err != nil || strings.TrimSpace(token) == "" {
		t.Fatalf("token = %q, %v", token, err)
	}
	path := filepath.Join(directory, "vault.json")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the token was not written to the server's own file: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(stored, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["token"] != token {
		t.Fatalf("stored = %s", stored)
	}
	if _, err := os.Stat(filepath.Join(directory, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("a standalone server touched the agent's settings file")
	}
	// The token is reused on the next start rather than regenerated.
	again, err := ensureToken(getenv, options)
	if err != nil || again != token {
		t.Fatalf("second token = %q, %v", again, err)
	}
}

// TestTokenLivesInTheAgentSettingsWhenMidasLaunchedIt: the whole point of the
// merge is that Midas' configuration is one file.
func TestTokenLivesInTheAgentSettingsWhenMidasLaunchedIt(t *testing.T) {
	directory := t.TempDir()
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"currency":"AUD","agentModels":{"main":"openai/gpt-5.6-sol"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(map[string]string{"MIDAS_CONFIG_DIR": directory, "MIDAS_MCP_SETTINGS": settings})
	options := Options{Name: "control"}
	token, err := ensureToken(getenv, options)
	if err != nil || strings.TrimSpace(token) == "" {
		t.Fatalf("token = %q, %v", token, err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"currency", "agentModels"} {
		if _, ok := stored[key]; !ok {
			t.Fatalf("%s was dropped: %s", key, raw)
		}
	}
	var section map[string]map[string]any
	if err := json.Unmarshal(stored["mcp"], &section); err != nil {
		t.Fatalf("mcp section = %s: %v", stored["mcp"], err)
	}
	if section["control"]["token"] != token {
		t.Fatalf("token was not stored under mcp.control: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(directory, "control.json")); !os.IsNotExist(err) {
		t.Fatal("the agent's settings file was not the only file written")
	}
	if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, %v", info.Mode().Perm(), err)
	}
}

// TestTokenMovesFromTheServerFileOnFirstWrite: a server configured before the
// merge is read from its own file, and its next write carries it into the shared
// file so nothing has to be re-created by hand.
func TestTokenMovesFromTheServerFileOnFirstWrite(t *testing.T) {
	directory := t.TempDir()
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"currency":"AUD"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(directory, "hub.json")
	if err := os.WriteFile(legacy, []byte(`{"token":"legacy-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := testEnv(map[string]string{"MIDAS_CONFIG_DIR": directory, "MIDAS_MCP_SETTINGS": settings})
	options := Options{Name: "hub"}
	token, err := ensureToken(getenv, options)
	if err != nil {
		t.Fatal(err)
	}
	if token != "legacy-token" {
		t.Fatalf("token = %q, want the configured one", token)
	}
	// ensureToken only writes when it has to, so the migration happens on the next
	// write of any setting.
	if err := writeSettings(getenv, options, map[string]any{"token": token, "listen": "127.0.0.1:8787"}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(stored, &parsed); err != nil {
		t.Fatal(err)
	}
	var section struct {
		Hub struct {
			Token  string `json:"token"`
			Listen string `json:"listen"`
		} `json:"hub"`
	}
	if err := json.Unmarshal(parsed["mcp"], &section); err != nil {
		t.Fatalf("mcp section = %s: %v", parsed["mcp"], err)
	}
	if section.Hub.Token != "legacy-token" || section.Hub.Listen != "127.0.0.1:8787" {
		t.Fatalf("migrated settings = %s", stored)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("the old file was removed: %v", err)
	}
}

// TestPrintConfigStillSpeaksStandardMCP: the snippet is for other agents, so it is
// unchanged by the merge.
func TestPrintConfigStillSpeaksStandardMCP(t *testing.T) {
	directory := t.TempDir()
	getenv := testEnv(map[string]string{"MIDAS_CONFIG_DIR": directory})
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err := printConfig(stdout, getenv, Options{Name: "control"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		MCPServers map[string]struct {
			Command []string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("print-config output = %s: %v", data, err)
	}
	if command := entry.MCPServers["control"].Command; len(command) != 1 || command[0] != "control" {
		t.Fatalf("snippet = %s", data)
	}
}
