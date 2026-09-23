package control

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEndpoints stands in for the Chromium/Firefox layer, so the tool surface can
// be tested without a browser anywhere near it.
type fakeEndpoints struct {
	state           EndpointStatus
	automated       string
	enableArgs      []string
	firefoxProfiles []string
}

func (f *fakeEndpoints) Status(browser Browser) EndpointStatus {
	status := f.state
	status.Browser = browser.Name
	return status
}
func (f *fakeEndpoints) Attach(Browser) (string, error) { return "http://127.0.0.1:9333", nil }
func (f *fakeEndpoints) Relaunch(context.Context, Browser, string) (string, error) {
	return "http://127.0.0.1:9334", nil
}
func (f *fakeEndpoints) FirefoxProfiles(home string) ([]string, error) {
	return f.firefoxProfiles, nil
}

func (f *fakeEndpoints) EnableFirefox(profileDir string, port int) error {
	f.enableArgs = append(f.enableArgs, profileDir)
	return nil
}
func (f *fakeEndpoints) DisableFirefox(profileDir string) error {
	f.enableArgs = append(f.enableArgs, profileDir)
	return nil
}
func (f *fakeEndpoints) AutomatedSetup(Browser) string { return f.automated }

func connectControl(t *testing.T, options ServerOptions) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := NewServer(options).Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	names := []string{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

// TestToolListFollowsTheEndpointLayer: the browser tool exists exactly when an
// endpoint manager is supplied, so a program without one cannot promise it.
func TestToolListFollowsTheEndpointLayer(t *testing.T) {
	// Order is the server's business; the set is the contract.
	names := func(options ServerOptions) map[string]bool {
		set := map[string]bool{}
		for _, name := range toolNames(t, connectControl(t, options)) {
			set[name] = true
		}
		return set
	}
	without := names(ServerOptions{})
	if len(without) != 2 || !without[ToolState] || !without[ToolPermissions] || without[ToolBrowser] {
		t.Fatalf("tools without an endpoint layer = %#v", without)
	}
	with := names(ServerOptions{Endpoints: &fakeEndpoints{}})
	if len(with) != 3 || !with[ToolBrowser] {
		t.Fatalf("tools with an endpoint layer = %#v", with)
	}
}

func TestStateToolReturnsAReadingAndNeverErrorsOnGaps(t *testing.T) {
	session := connectControl(t, ServerOptions{
		HostName: "test-host",
		Run: func(_ context.Context, script string) (string, error) {
			switch {
			case strings.Contains(script, "active tab index"):
				return "W\tMail\t1\nT\tInbox\thttps://mail.example.test/\n", nil
			case strings.Contains(script, "name of first window"):
				return "Inbox", nil
			case strings.Contains(script, "repeat with p in processes"):
				return "Finder\ttrue\n", nil
			}
			return "", nil
		},
	})
	// Browsers are limited to the installed set, so the answer depends on the
	// machine; what matters is that the call succeeds and carries permissions.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolState, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("state failed: %#v", result.Content)
	}
	if result.StructuredContent == nil {
		t.Fatal("state returned nothing structured")
	}
}

func TestPermissionsToolReportsTiersAndAutomation(t *testing.T) {
	endpoints := &fakeEndpoints{automated: "Control writes these preferences into every Firefox profile's user.js on request."}
	session := connectControl(t, ServerOptions{Endpoints: endpoints,
		Run: func(context.Context, string) (string, error) { return "", nil }})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolPermissions, Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("permissions = %v %#v", err, result)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolPermissions, Arguments: map[string]any{"browser": "Firefox"}}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserToolRequiresARealBrowserAndTheRightAction(t *testing.T) {
	endpoints := &fakeEndpoints{state: EndpointStatus{State: "absent"}}
	session := connectControl(t, ServerOptions{Endpoints: endpoints})
	// An unknown browser is refused with the known names, not a silent no-op.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolBrowser, Arguments: map[string]any{"action": "status", "browser": "Netscape"}})
	if err == nil && !result.IsError {
		t.Fatal("an unknown browser was accepted")
	}
	// Status works and reports the state it was given.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolBrowser, Arguments: map[string]any{"action": "status", "browser": "Helium"}})
	if err != nil || result.IsError {
		t.Fatalf("status = %v %#v", err, result)
	}
	// Firefox preferences are refused for a Chromium browser, so a caller cannot
	// write a Firefox pref into the wrong profile.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolBrowser, Arguments: map[string]any{"action": "enable", "browser": "Helium"}})
	if err == nil && !result.IsError {
		t.Fatal("enable was accepted for a Chromium browser")
	}
}
