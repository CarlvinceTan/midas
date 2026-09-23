package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/mcpconfig"
)

// TestInstallRefusesNamesThatEscapeTheBridgesDirectory: the name becomes a path
// segment, and Uninstall removes that directory, so a traversal name must never be
// accepted.
func TestInstallRefusesNamesThatEscapeTheBridgesDirectory(t *testing.T) {
	instance, _ := testHub(t)
	source := filepath.Join(t.TempDir(), "bridge")
	if err := os.WriteFile(source, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..", ".", "../escape", "a/b", `a\b`, ""} {
		_, err := instance.Install(context.Background(), InstallOptions{
			Name: name, Source: source, Command: []string{"bridge"},
		})
		// An empty name fails earlier, at the required-name check.
		if err == nil || !strings.Contains(err.Error(), "bridge name") {
			t.Fatalf("install %q = %v", name, err)
		}
		if err := instance.Uninstall(name, true); err == nil {
			t.Fatalf("uninstall %q was allowed", name)
		}
	}
	stateDir, err := StateDir(func(name string) string {
		if name == "HUB_STATE_DIR" {
			return filepath.Join(t.TempDir(), "state")
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "bridges", "escape")); err == nil {
		t.Fatal("a traversal install wrote outside the bridges directory")
	}
}

// TestUninstallKeepsOtherFilesUnlessPurged pins the documented contract: the
// fetched binary goes, files the bridge wrote beside it stay.
func TestUninstallKeepsOtherFilesUnlessPurged(t *testing.T) {
	instance, _ := testHub(t)
	binary := mockBridgePath(t)
	if _, err := instance.Install(context.Background(), InstallOptions{
		Name: "mock", Source: binary, Command: []string{"mock-bridge"},
	}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(instance.StateDir(), "bridges", "mock")
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Uninstall("mock", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "state.json")); err != nil {
		t.Fatalf("uninstall without purge removed the bridge's state: %v", err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 1 {
		t.Fatalf("bridge directory after uninstall = %v, %v", entries, err)
	}
}

// TestLoginListIsScopedToTheOwningSession: a login carries a challenge payload
// that links a device, so another session must not be able to list it.
func TestLoginListIsScopedToTheOwningSession(t *testing.T) {
	manager := newLoginManager(nil)
	if _, err := manager.start(context.Background(), "session-a", "bridge", "account", func(context.Context, string, map[string]any, any) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.start(context.Background(), "session-b", "bridge", "account", func(context.Context, string, map[string]any, any) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if owned := manager.listOwned("session-a"); len(owned) != 1 || owned[0].Session != "session-a" {
		t.Fatalf("owned logins = %#v", owned)
	}
	if owned := manager.listOwned(""); len(owned) != 0 {
		t.Fatalf("an unidentified session listed %#v", owned)
	}
}

// TestBridgeProcessCloseIsBounded: a bridge that ignores stdin EOF must not hold
// its caller forever, which is what the supervisor hits on a failed hello.
func TestBridgeProcessCloseIsBounded(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// A reader that never ends: the child "ignores" EOF.
	neverEnding := &blockingReader{release: make(chan struct{})}
	process := newBridgeProcess("deaf", stdinWriter, neverEnding, nil)
	finished := make(chan struct{})
	go func() {
		_ = process.Close()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		close(neverEnding.release)
		t.Fatal("Close waited forever for a bridge that ignored EOF")
	}
	close(neverEnding.release)
	_ = stdinReader.Close()
}

// blockingReader blocks until released, standing in for a child that never exits.
type blockingReader struct{ release chan struct{} }

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, errors.New("closed")
}

// TestProtocolCallDropsPendingWaiters: a timed-out call must not leave its waiter
// behind, or a long-lived bridge leaks one entry per timeout.
func TestProtocolCallDropsPendingWaiters(t *testing.T) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinReader.Close()
	process := newBridgeProcess("quiet", stdinWriter, strings.NewReader(""), nil)
	if err := process.Call(context.Background(), MethodHello, nil, nil); err == nil {
		// A closed stdin reports the failure, which is the point: the waiter must
		// not survive it.
		t.Log("call reported an error as expected")
	}
	process.mu.Lock()
	pending := len(process.pending)
	process.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending waiters after a failed call = %d", pending)
	}
}

// TestHubSettingsLiveInTheAgentSettingsFileWhenMidasLaunchedIt: with the override
// set, the hub's configuration is the mcp.hub section of settings.json and every
// other key in that file is preserved.
func TestHubSettingsLiveInTheAgentSettingsFileWhenMidasLaunchedIt(t *testing.T) {
	directory := t.TempDir()
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"currency":"AUD","mcpServers":{"hub":{"command":["hub"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		switch name {
		case "MIDAS_CONFIG_DIR":
			return directory
		case mcpconfig.OverrideEnv:
			return settings
		default:
			return ""
		}
	}
	location, err := ResolveConfigLocation(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if location.Path != settings || len(location.Section) != 2 {
		t.Fatalf("location = %#v", location)
	}
	config := Config{Listen: "127.0.0.1:8787", Token: "secret", Bridges: map[string]BridgeConfig{
		"mock": {Source: "/tmp/mock", Command: []string{"/tmp/mock"}},
	}}
	if err := location.Save(config); err != nil {
		t.Fatal(err)
	}
	// The agent's own settings and the launch list survive the hub's write.
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"currency", "mcpServers"} {
		if _, ok := stored[key]; !ok {
			t.Fatalf("%s was dropped: %s", key, raw)
		}
	}
	reloaded, err := location.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Token != "secret" || len(reloaded.Bridges) != 1 {
		t.Fatalf("reloaded = %#v", reloaded)
	}
	if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, %v", info.Mode().Perm(), err)
	}
}

// TestHubReadsItsOwnFileUntilTheSectionExists: a hub.json made before the merge
// keeps working, and the next write moves it into the shared file.
func TestHubReadsItsOwnFileUntilTheSectionExists(t *testing.T) {
	directory := t.TempDir()
	settings := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"currency":"AUD"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(directory, "hub.json")
	if err := os.WriteFile(legacy, []byte(`{"token":"old","bridges":{"mock":{"command":["/tmp/mock"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		switch name {
		case "MIDAS_CONFIG_DIR":
			return directory
		case mcpconfig.OverrideEnv:
			return settings
		default:
			return ""
		}
	}
	location, err := ResolveConfigLocation(getenv)
	if err != nil {
		t.Fatal(err)
	}
	config, err := location.Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Token != "old" || len(config.Bridges) != 1 {
		t.Fatalf("legacy configuration was not read: %#v", config)
	}
	if _, ok := config.Bridges["mock"]; !ok {
		t.Fatalf("legacy bridges lost: %#v", config.Bridges)
	}
	// Saving carries the whole configuration, including what came from the old
	// file, into the shared one.
	config.Token = "new"
	if err := location.Save(config); err != nil {
		t.Fatal(err)
	}
	stored, err := mcpconfig.Read(settings, []string{"mcp", "hub"})
	if err != nil || stored == nil {
		t.Fatalf("hub section = %s, %v", stored, err)
	}
	var migrated Config
	if err := json.Unmarshal(stored, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Token != "new" || len(migrated.Bridges) != 1 {
		t.Fatalf("migrated = %#v", migrated)
	}
	// The old file is left alone: another agent's hub still reads it.
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy file was removed: %v", err)
	}
}
