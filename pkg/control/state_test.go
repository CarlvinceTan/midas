package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner answers scripts from a table, so the collector can be tested
// without a desktop and without prompting anyone for permission.
func fakeRunner(t *testing.T, answers map[string]string, failures map[string]error) func(context.Context, string) (string, error) {
	t.Helper()
	// Markers overlap (one script mentions both "first window" and "frontmost is
	// true"), so the longest match wins rather than map order.
	longest := func(script string, markers []string) (string, bool) {
		best, found := "", false
		for _, marker := range markers {
			if strings.Contains(script, marker) && (!found || len(marker) > len(best)) {
				best, found = marker, true
			}
		}
		return best, found
	}
	return func(_ context.Context, script string) (string, error) {
		if marker, found := longest(script, keysOf(answers)); found {
			return answers[marker], nil
		}
		if marker, found := longest(script, keysOfErrors(failures)); found {
			return "", failures[marker]
		}
		return "", nil
	}
}

func keysOf(table map[string]string) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	return keys
}

func keysOfErrors(table map[string]error) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	return keys
}

func TestCollectReadsBrowserState(t *testing.T) {
	chromium := Browser{Name: "Helium", Engine: "chromium"}
	output := "W\tDocs and mail\t2\n" +
		"T\tInbox | Mail\t\thttps://mail.example.test/inbox\n" +
		"T\tDesign doc - Docs\thttps://docs.example.test/d/1\n"
	state, err := Collect(context.Background(), CollectOptions{
		Browsers: []Browser{chromium},
		HostName: "test-host",
		Running:  func(Browser) bool { return true },
		Run: fakeRunner(t, map[string]string{
			"active tab index":  output,
			"frontmost is true": "",
		}, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Host != "test-host" || state.Platform == "" {
		t.Fatalf("state = %#v", state)
	}
	if len(state.Browsers) != 1 {
		t.Fatalf("browsers = %#v", state.Browsers)
	}
	browser := state.Browsers[0]
	if len(browser.Windows) != 1 {
		t.Fatalf("windows = %#v", browser.Windows)
	}
	window := browser.Windows[0]
	if window.Title != "Docs and mail" || window.Active != 2 || len(window.Tabs) != 2 {
		t.Fatalf("window = %#v", window)
	}
	if window.Tabs[1].URL != "https://docs.example.test/d/1" {
		t.Fatalf("tab = %#v", window.Tabs[1])
	}
	if !state.Permissions.Automation["Helium"] {
		t.Fatalf("automation = %#v", state.Permissions.Automation)
	}
}

func TestCollectDegradesWhenConsentIsDeclined(t *testing.T) {
	chromium := Browser{Name: "Helium", Engine: "chromium"}
	state, err := Collect(context.Background(), CollectOptions{
		Browsers: []Browser{chromium},
		Running:  func(Browser) bool { return true },
		Run: fakeRunner(t, nil, map[string]error{
			"active tab index": errors.New("Helium got an error: Not authorized to send Apple events to Helium. (-1743)"),
		}),
	})
	if err != nil {
		t.Fatalf("a declined consent must not fail the reading: %v", err)
	}
	if len(state.Browsers) != 1 || state.Browsers[0].Error == "" {
		t.Fatalf("browsers = %#v", state.Browsers)
	}
	if state.Permissions.Automation["Helium"] {
		t.Fatal("automation reported as granted after a decline")
	}
	if len(state.Gaps) == 0 || !strings.Contains(state.Gaps[0], "Helium") {
		t.Fatalf("gaps = %#v", state.Gaps)
	}
}

func TestCollectSkipsBrowsersThatAreNotRunning(t *testing.T) {
	// A browser that is not running contributes no state and consumes no consent.
	state, err := Collect(context.Background(), CollectOptions{
		Browsers: []Browser{{Name: "Firefox", BinaryPath: "/nonexistent/firefox", Engine: "firefox"}},
		Running:  func(Browser) bool { return false },
		Run:      fakeRunner(t, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Browsers) != 1 || state.Browsers[0].Running || state.Browsers[0].Error != "" {
		t.Fatalf("browsers = %#v", state.Browsers)
	}
	// Firefox has no Apple Events route at all, which is reported rather than
	// discovered as an error. The running probe looks for the binary path, so the
	// test uses its own process, which is certainly running.
	running := Browser{Name: "Firefox", BinaryPath: "/Applications/Firefox.app/Contents/MacOS/firefox", Engine: "firefox"}
	state, err = Collect(context.Background(), CollectOptions{
		Browsers: []Browser{running},
		Running:  func(Browser) bool { return true },
		Run:      fakeRunner(t, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Browsers[0].Error, "no scripting dictionary") {
		t.Fatalf("firefox error = %q", state.Browsers[0].Error)
	}
}

func TestCollectReadsAppsAndReportsAccessibility(t *testing.T) {
	state, err := Collect(context.Background(), CollectOptions{
		IncludeApps: true,
		Browsers:    []Browser{},
		Running:     func(Browser) bool { return false },
		Run: fakeRunner(t, map[string]string{
			"repeat with p in processes": "Finder\ttrue\nHelium\tfalse\n",
			"name of first window":       "Inbox",
		}, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Apps) != 2 || state.Frontmost != "Finder" {
		t.Fatalf("apps = %#v frontmost = %q", state.Apps, state.Frontmost)
	}
	// Only one app is frontmost, and it is the one the script reported.
	if !state.Apps[0].Frontmost || state.Apps[0].Name != "Finder" || state.Apps[1].Frontmost {
		t.Fatalf("apps = %#v", state.Apps)
	}
	if state.Permissions.Accessibility != "granted" {
		t.Fatalf("accessibility = %q", state.Permissions.Accessibility)
	}

	// Denied Accessibility is reported with the exact thing to enable, not as a
	// failure of the reading.
	denied, err := Collect(context.Background(), CollectOptions{
		IncludeApps: true,
		Browsers:    []Browser{},
		Running:     func(Browser) bool { return false },
		Run: fakeRunner(t, nil, map[string]error{
			"name of first window": errors.New("System Events got an error: osascript is not allowed assistive access. (-25211)"),
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Permissions.Accessibility != "denied" {
		t.Fatalf("accessibility = %q", denied.Permissions.Accessibility)
	}
	found := false
	for _, gap := range denied.Gaps {
		if strings.Contains(gap, "Accessibility") && strings.Contains(gap, "System Settings") {
			found = true
		}
	}
	if !found {
		t.Fatalf("gaps = %#v", denied.Gaps)
	}
}

func TestParsersToleratePartialOutput(t *testing.T) {
	if windows := parseBrowserWindows(""); len(windows) != 0 {
		t.Fatalf("empty output = %#v", windows)
	}
	// A tab before any window, a malformed line, and a missing URL are all
	// survivable: observation must never fail on odd output.
	windows := parseBrowserWindows("T\torphan\nnonsense\nW\tSolo\nT\tOnly title\t\n")
	if len(windows) != 1 || len(windows[0].Tabs) != 1 {
		t.Fatalf("windows = %#v", windows)
	}
	if windows[0].Tabs[0].Title != "Only title" || windows[0].Tabs[0].URL != "" {
		t.Fatalf("tab = %#v", windows[0].Tabs[0])
	}
	apps, frontmost := parseApps("\nFinder\tFALSE\n\t\n")
	if len(apps) != 1 || frontmost != "" || apps[0].Frontmost {
		t.Fatalf("apps = %#v frontmost = %q", apps, frontmost)
	}
}
