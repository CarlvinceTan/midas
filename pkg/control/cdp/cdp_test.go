package cdp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/control"
)

func helium() control.Browser {
	return control.Browser{Name: "Helium", BundlePath: "/Applications/Helium.app", BinaryPath: "/Applications/Helium.app/Contents/MacOS/Helium", Engine: "chromium"}
}

func TestEndpointReadsTheDevToolsActivePortFile(t *testing.T) {
	dir := t.TempDir()
	browser := helium()
	if _, _, ok := Endpoint(browser, dir); ok {
		t.Fatal("an endpoint was reported without the file")
	}
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte("52550\n/devtools/browser/abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port, path, ok := Endpoint(browser, dir)
	if !ok || port != "52550" || path != "/devtools/browser/abc" {
		t.Fatalf("endpoint = %q, %q, %v", port, path, ok)
	}
	// The file alone is not an endpoint: nothing is listening on that port, which
	// is exactly the stale-file case a browser leaves behind when it quits.
	if _, err := EndpointURL(browser, dir); err == nil {
		t.Fatal("a stale DevToolsActivePort file was treated as a live endpoint")
	}
}

func TestLiveEndpointRequiresSomethingToAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Browser":"Chrome/153"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(port+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, ok := LiveEndpoint(helium(), dir); !ok || got != port {
		t.Fatalf("live endpoint = %q, %v", got, ok)
	}
	if url, err := EndpointURL(helium(), dir); err != nil || url != server.URL {
		t.Fatalf("url = %q, %v", url, err)
	}
}

func TestWaitForEndpointTimesOutInsteadOfPretending(t *testing.T) {
	if _, err := WaitForEndpoint(helium(), t.TempDir(), 300*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "did not publish") {
		t.Fatalf("wait = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Browser":"Chrome/153"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(port+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if url, err := WaitForEndpoint(helium(), dir, time.Second); err != nil || url != server.URL {
		t.Fatalf("wait = %q, %v", url, err)
	}
}

func TestRelaunchAndQuitRefuseUnsupportedEngines(t *testing.T) {
	firefox, _ := control.Find("Firefox")
	if _, err := Relaunch(firefox, "", time.Second); err == nil || !strings.Contains(err.Error(), "profile preference") {
		t.Fatalf("firefox relaunch = %v", err)
	}
	missing := control.Browser{Name: "Helium", BinaryPath: "/nope/Helium", Engine: "chromium"}
	if _, err := Relaunch(missing, "", time.Second); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing browser relaunch = %v", err)
	}
}

// TestAutomatedSetupExistsOnlyWhereItCanBeDone keeps the promise honest: this
// layer only claims what it can actually do.
func TestAutomatedSetupExistsOnlyWhereItCanBeDone(t *testing.T) {
	safari, _ := control.Find("Safari")
	if got := AutomatedSetup(safari); got != "" {
		t.Fatalf("safari automated setup = %q", got)
	}
	for _, name := range []string{"Helium", "Firefox"} {
		browser, _ := control.Find(name)
		if got := AutomatedSetup(browser); got == "" {
			t.Fatalf("%s has no automated setup", name)
		}
	}
	firefox, _ := control.Find("Firefox")
	if got := AutomatedSetup(firefox); !strings.Contains(got, "user.js") {
		t.Fatalf("firefox automated setup = %q", got)
	}
}
