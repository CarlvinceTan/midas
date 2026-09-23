package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeRegistry writes a catalog file and points the hub at it.
func writeRegistry(t *testing.T, instance *Hub, entries []CatalogEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	encoded, err := json.Marshal(map[string]any{"bridges": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	config := instance.Config()
	config.Registry = path
	if err := WriteConfig(instance.ConfigPath(), config); err != nil {
		t.Fatal(err)
	}
	return path
}

// reopen loads the hub again, picking up configuration written by a test.
func reopen(t *testing.T, instance *Hub) *Hub {
	t.Helper()
	reopened, err := Open(func(name string) string {
		switch name {
		case "MIDAS_CONFIG_DIR":
			return filepath.Dir(instance.ConfigPath())
		case "HUB_STATE_DIR":
			return instance.StateDir()
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	return reopened
}

func mockCatalogEntry(t *testing.T) CatalogEntry {
	t.Helper()
	return CatalogEntry{
		Name: "mock", Description: "reference bridge", Source: mockBridgePath(t),
		Command: []string{"mock-bridge"},
	}
}

func TestAvailableIsEmptyWithoutARegistry(t *testing.T) {
	instance, _ := testHub(t)
	entries, err := instance.Available(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("an unconfigured hub offered %#v", entries)
	}
	// An install without a source cannot be guessed, and says what to do.
	if _, err := instance.Install(context.Background(), InstallOptions{Name: "mock"}); err == nil ||
		!strings.Contains(err.Error(), "no registry is configured") {
		t.Fatalf("install without a source = %v", err)
	}
}

func TestRegistryListsAndInstallsByName(t *testing.T) {
	instance, getenv := testHub(t)
	entry := mockCatalogEntry(t)
	registryPath := writeRegistry(t, instance, []CatalogEntry{entry})

	// Reopen against the same directories so the new config is loaded.
	instance = reopen(t, instance)
	_ = getenv
	if got := instance.Config().Registry; got != registryPath {
		t.Fatalf("registry = %q", got)
	}

	available, err := instance.Available(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].Name != "mock" || available[0].Installed {
		t.Fatalf("available = %#v", available)
	}
	// Installing by name alone resolves source, command, and checksum from the
	// catalog, so an agent never has to be told a path.
	status, err := instance.Install(context.Background(), InstallOptions{Name: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if status.Name != "mock" || status.State != "stopped" {
		t.Fatalf("installed = %#v", status)
	}
	config := instance.Config()
	if stored := config.Bridges["mock"]; stored.Source != entry.Source || len(stored.Command) != 1 {
		t.Fatalf("stored bridge = %#v", stored)
	}
	// The listing now reports it as installed rather than offering it again.
	available, err = instance.Available(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || !available[0].Installed {
		t.Fatalf("available after install = %#v", available)
	}
	// And it works.
	accounts, err := instance.Accounts(context.Background())
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts = %#v, %v", accounts, err)
	}
}

func TestUnknownRegistryNameExplainsWhatIsAvailable(t *testing.T) {
	instance, _ := testHub(t)
	writeRegistry(t, instance, []CatalogEntry{mockCatalogEntry(t)})
	instance = reopen(t, instance)

	_, err := instance.Install(context.Background(), InstallOptions{Name: "telegram"})
	if err == nil || !strings.Contains(err.Error(), "available: mock") {
		t.Fatalf("unknown name = %v", err)
	}
}

func TestRegistryCacheAvoidsRefetchingAndRefreshForcesIt(t *testing.T) {
	var requests atomic.Int64
	entry := mockCatalogEntry(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(writer).Encode(map[string]any{"bridges": []CatalogEntry{entry}})
	}))
	defer server.Close()

	instance, _ := testHub(t)
	config := instance.Config()
	config.Registry = server.URL
	if err := WriteConfig(instance.ConfigPath(), config); err != nil {
		t.Fatal(err)
	}
	instance = reopen(t, instance)

	if _, err := instance.Available(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Available(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("second listing fetched again: %d requests", got)
	}
	if _, err := instance.Available(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("refresh did not fetch: %d requests", got)
	}
}

func TestRegistryRejectsEntriesThatCannotBeInstalled(t *testing.T) {
	instance, _ := testHub(t)
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, []byte(`{"bridges":[{"name":"broken"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := instance.Config()
	config.Registry = path
	if err := WriteConfig(instance.ConfigPath(), config); err != nil {
		t.Fatal(err)
	}
	instance = reopen(t, instance)

	if _, err := instance.Available(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "without a name, source, or command") {
		t.Fatalf("broken registry = %v", err)
	}
}

func TestBridgesToolOffersTheRegistry(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	// Without a registry the tool says so rather than searching.
	empty := callTool(t, session, "hub_bridges", map[string]any{"action": "available"})
	if !strings.Contains(empty, "no registry is configured") {
		t.Fatalf("available without a registry = %s", empty)
	}

	// With one, the tool lists the catalog, and installs by name alone.
	writeRegistry(t, instance, []CatalogEntry{mockCatalogEntry(t)})
	configured := reopen(t, instance)
	listed := callTool(t, connect(t, configured), "hub_bridges", map[string]any{"action": "available"})
	if !strings.Contains(listed, `"name":"mock"`) || !strings.Contains(listed, "reference bridge") {
		t.Fatalf("available = %s", listed)
	}
	installed := callTool(t, connect(t, configured), "hub_bridges", map[string]any{"action": "install", "name": "mock"})
	if !strings.Contains(installed, `"state":"stopped"`) {
		t.Fatalf("install by name = %s", installed)
	}
}
