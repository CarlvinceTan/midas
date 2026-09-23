package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// browser is a real Chromium the test launched itself, with its own profile and a
// debugging endpoint. Nothing of the developer's browser is involved, which is what
// makes the control tests safe to run on a working machine.
type browser struct {
	Path    string
	Profile string
	command *exec.Cmd
}

// startBrowser launches a Chromium found on this machine, or fails with what to
// install. Setting MIDAS_E2E_SKIP_BROWSER=1 skips the browser tests instead of
// failing, for an environment that has no browser at all.
func startBrowser(t *testing.T) *browser {
	t.Helper()
	path := findBrowser()
	if path == "" {
		if os.Getenv("MIDAS_E2E_SKIP_BROWSER") != "" {
			t.Skip("no Chromium found and MIDAS_E2E_SKIP_BROWSER is set")
		}
		t.Fatal("no Chromium found: set MIDAS_E2E_BROWSER to a chrome/chromium binary, " +
			"install Playwright's chromium, or set MIDAS_E2E_SKIP_BROWSER=1 to skip browser tests")
	}
	// The profile is not a t.TempDir: the browser may still be flushing it when the
	// test ends, and this suite cleans it up itself after the process group is gone.
	profile, err := os.MkdirTemp("", "midas-e2e-profile-")
	if err != nil {
		t.Fatal(err)
	}
	// Port zero lets the browser choose, and it writes the port and its websocket
	// path into DevToolsActivePort, which is exactly what control reads.
	arguments := []string{"--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check"}
	if os.Geteuid() == 0 {
		// A container runs as root, where Chromium refuses to start with its sandbox
		// on. The browser here is a throwaway with its own profile, so that is the
		// only thing this gives up.
		arguments = append(arguments, "--no-sandbox", "--disable-dev-shm-usage")
	}
	if strings.Contains(filepath.Base(path), "headless") {
		arguments = append(arguments, "about:blank")
	} else {
		arguments = append(arguments, "--headless=new", "about:blank")
	}
	command := exec.Command(path, arguments...)
	startInGroup(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	instance := &browser{Path: path, Profile: profile, command: command}
	t.Cleanup(func() {
		stopGroup(command)
		// A browser that was killed mid-write can leave a file behind, and a leftover
		// profile is not worth failing a test over.
		_ = os.RemoveAll(profile)
	})
	// The endpoint file is the browser's own signal that it is up.
	waitFor(t, "the browser to write its debugging endpoint", func() bool {
		_, err := os.Stat(filepath.Join(profile, "DevToolsActivePort"))
		return err == nil
	})
	return instance
}

// findBrowser looks for a Chromium this machine already has, preferring an explicit
// setting over anything discovered.
func findBrowser() string {
	if configured := strings.TrimSpace(os.Getenv("MIDAS_E2E_BROWSER")); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err == nil {
		// Playwright's cache is the usual place a test machine already has one.
		for _, pattern := range []string{
			filepath.Join(home, "Library/Caches/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-mac-arm64/chrome-headless-shell"),
			filepath.Join(home, "Library/Caches/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-mac-x64/chrome-headless-shell"),
			filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac/Chromium.app/Contents/MacOS/Chromium"),
			filepath.Join(home, ".cache/ms-playwright/chromium_headless_shell-*/chrome-linux/chrome-headless-shell"),
			filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux/chrome"),
		} {
			if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
				return matches[0]
			}
		}
	}
	for _, path := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	return ""
}

// controlFor starts the control server pointed at a browser the test launched.
func controlFor(t *testing.T, launched *browser, extra map[string]string) *server {
	t.Helper()
	env := map[string]string{
		"CONTROL_BROWSER_BINARY":  launched.Path,
		"CONTROL_BROWSER_NAME":    "Chromium",
		"CONTROL_BROWSER_ENGINE":  "chromium",
		"CONTROL_BROWSER_PROFILE": launched.Profile,
	}
	for key, value := range extra {
		env[key] = value
	}
	return startServer(t, "control", env)
}

// TestControlReadsItsEnvironment: the state and permissions tools describe the
// machine the agent runs on without changing anything.
func TestControlReadsItsEnvironment(t *testing.T) {
	launched := startBrowser(t)
	instance := controlFor(t, launched, nil)

	state := callTool(t, instance, "control_state", map[string]any{})
	// The shape is what a client renders: the reading, whatever it found, and the
	// gaps it knows about.
	if _, ok := state["gaps"]; !ok {
		t.Fatalf("the state has no gaps field: %#v", state)
	}
	encoded, _ := json.Marshal(state)
	if !strings.Contains(string(encoded), "Chromium") {
		t.Fatalf("the state does not mention the browser it was pointed at: %s", encoded)
	}

	permissions := callTool(t, instance, "control_permissions", map[string]any{})
	browsers := list(t, permissions, "browsers")
	if len(browsers) == 0 {
		t.Fatalf("permissions = %#v", permissions)
	}
	found := false
	for _, entry := range browsers {
		if field(t, entry, "browser") == "Chromium" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the environment browser is missing from permissions: %#v", browsers)
	}
}

// TestControlAttachesToARealBrowserEndpoint: the browser tool finds the endpoint a
// running Chromium wrote, confirms it answers, and reports it.
func TestControlAttachesToARealBrowserEndpoint(t *testing.T) {
	launched := startBrowser(t)
	instance := controlFor(t, launched, nil)

	status := callTool(t, instance, "control_browser", map[string]any{"action": "status", "browser": "Chromium"})
	endpoint, _ := status["status"].(map[string]any)
	if endpoint == nil {
		t.Fatalf("status = %#v", status)
	}
	if field(t, endpoint, "port") == "" || field(t, endpoint, "url") == "" {
		t.Fatalf("the endpoint was not discovered: %#v", endpoint)
	}

	attached := callTool(t, instance, "control_browser", map[string]any{"action": "attach", "browser": "Chromium"})
	attachedStatus, _ := attached["status"].(map[string]any)
	if attachedStatus == nil || field(t, attachedStatus, "port") != field(t, endpoint, "port") {
		t.Fatalf("attach = %#v", attached)
	}
	// The URL control reports is the one the browser actually serves: the CDP HTTP
	// endpoint answers with the browser's version and its websocket address.
	if response := get(t, strings.TrimSuffix(field(t, endpoint, "url"), "/")+"/json/version"); !strings.Contains(response, "webSocketDebuggerUrl") {
		t.Fatalf("the endpoint does not answer like a CDP endpoint: %s", response)
	}

	// A browser that is not installed is reported as absent rather than guessed at.
	absent := callTool(t, instance, "control_browser", map[string]any{"action": "status", "browser": "Firefox"})
	absentStatus, _ := absent["status"].(map[string]any)
	if absentStatus == nil || field(t, absentStatus, "state") != "absent" {
		t.Fatalf("an uninstalled browser = %#v", absent)
	}
	// A browser nobody has heard of is refused by name.
	if message := callToolError(t, instance, "control_browser", map[string]any{"action": "status", "browser": "Netscape"}); strings.TrimSpace(message) == "" {
		t.Fatal("an unknown browser name was accepted")
	}
}

// TestControlEnablesAndDisablesAFirefoxProfile: Firefox takes a preference rather
// than a launch flag, and that path is testable without Firefox installed by
// pointing the server at a profile directory of its own.
func TestControlEnablesAndDisablesAFirefoxProfile(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "e2e.default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "prefs.js"), []byte("// profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Firefox records its profiles in profiles.ini, which is how control finds them
	// without being told.
	ini := "[Profile0]\nName=e2e\nIsRelative=0\nPath=" + profile + "\nDefault=1\n"
	if err := os.WriteFile(filepath.Join(home, "Library", "Application Support", "Firefox", "profiles.ini"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
	// A Firefox "binary" the tool can see: the path only has to exist for the
	// profile-preference path, which never launches it.
	fakeBinary := writeFile(t, t.TempDir(), "firefox", []byte("#!/bin/sh\nexit 0\n"))
	instance := startServer(t, "control", map[string]string{
		"HOME":                    home,
		"CONTROL_BROWSER_BINARY":  fakeBinary,
		"CONTROL_BROWSER_NAME":    "Firefox",
		"CONTROL_BROWSER_ENGINE":  "firefox",
		"CONTROL_BROWSER_PROFILE": profile,
	})

	callTool(t, instance, "control_browser", map[string]any{"action": "enable", "browser": "Firefox", "profile": profile, "port": 9222})
	// The preferences go to user.js, which is the file Firefox applies on top of the
	// profile's own prefs.js; the profile's own file is left alone.
	userJS, err := os.ReadFile(filepath.Join(profile, "user.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userJS), "devtools.debugger.remote-enabled") || !strings.Contains(string(userJS), "9222") {
		t.Fatalf("the endpoint preference was not written: %s", userJS)
	}
	prefs, err := os.ReadFile(filepath.Join(profile, "prefs.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prefs), "devtools.debugger") {
		t.Fatalf("the profile's own preferences were touched: %s", prefs)
	}

	// Disabling without naming a profile uses the discovered one, which is the path a
	// user takes.
	callTool(t, instance, "control_browser", map[string]any{"action": "disable", "browser": "Firefox"})
	userJS, err = os.ReadFile(filepath.Join(profile, "user.js"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(userJS), "devtools.debugger.remote-enabled") {
		t.Fatalf("the preference survived disable: %s", userJS)
	}
	// A Chromium browser is told what to do instead of being handed a preference.
	chromium := writeFile(t, t.TempDir(), "chromium", []byte("#!/bin/sh\nexit 0\n"))
	chromiumInstance := startServer(t, "control", map[string]string{
		"CONTROL_BROWSER_BINARY": chromium, "CONTROL_BROWSER_ENGINE": "chromium",
	})
	if message := callToolError(t, chromiumInstance, "control_browser", map[string]any{"action": "enable", "browser": "Chromium"}); !strings.Contains(message, "relaunch") {
		t.Fatalf("enabling a Chromium browser = %q", message)
	}
}

// TestControlStateHonoursItsFlags: the state tool reads the application list only
// when asked, which is what keeps a read from prompting for consent the user did
// not agree to.
func TestControlStateHonoursItsFlags(t *testing.T) {
	instance := startServer(t, "control", nil)

	withApps := callTool(t, instance, "control_state", map[string]any{"includeApps": true})
	withoutApps := callTool(t, instance, "control_state", map[string]any{"includeApps": false})
	// Both readings describe the same machine: the host is always there, and the
	// application list is only in the one that asked for it.
	if _, ok := withApps["host"]; !ok {
		t.Fatalf("state has no host: %#v", withApps)
	}
	// Where the platform cannot read the application list, the reading says so in
	// its gaps rather than quietly leaving the field out.
	if _, ok := withApps["apps"].([]any); !ok {
		gaps := strings.Join(gapList(withApps), " ")
		if !strings.Contains(gaps, "application list") {
			t.Fatalf("applications were neither read nor explained: %#v", withApps)
		}
	}
	if _, ok := withoutApps["apps"]; ok {
		t.Fatalf("applications were read without being asked for: %#v", withoutApps)
	}
	if gaps := strings.Join(gapList(withoutApps), " "); strings.Contains(gaps, "application list") {
		t.Fatalf("a reading that did not ask for applications still reports them: %#v", withoutApps)
	}

	// Permissions for one browser name only that browser.
	report := callTool(t, instance, "control_permissions", map[string]any{"browser": "Safari"})
	for _, entry := range list(t, report, "browsers") {
		if field(t, entry, "browser") != "Safari" {
			t.Fatalf("a filtered report mentioned %q", field(t, entry, "browser"))
		}
	}
}

// TestControlRelaunchesABrowserWithItsEndpoint: control can start a browser with
// its debugging endpoint itself, which is the path a container or an agent takes
// when nothing is running yet.
func TestControlRelaunchesABrowserWithItsEndpoint(t *testing.T) {
	path := findBrowser()
	if path == "" {
		if os.Getenv("MIDAS_E2E_SKIP_BROWSER") != "" {
			t.Skip("no Chromium found and MIDAS_E2E_SKIP_BROWSER is set")
		}
		t.Fatal("no Chromium found: set MIDAS_E2E_BROWSER or MIDAS_E2E_SKIP_BROWSER")
	}
	profile, err := os.MkdirTemp("", "midas-e2e-relaunch-")
	if err != nil {
		t.Fatal(err)
	}
	// The browser is named something no installed application is called: the
	// quit-then-relaunch path asks the desktop to quit a browser by name, and this
	// test must not be able to touch the developer's own browser.
	instance := startServer(t, "control", map[string]string{
		"CONTROL_BROWSER_BINARY":  path,
		"CONTROL_BROWSER_NAME":    "Midas E2E Browser",
		"CONTROL_BROWSER_ENGINE":  "chromium",
		"CONTROL_BROWSER_PROFILE": profile,
		"HOME":                    t.TempDir(),
	})
	t.Cleanup(func() {
		stopProcessesForProfile(t, profile)
		_ = os.RemoveAll(profile)
	})

	// Relaunching is implemented for the macOS desktop. Elsewhere the tool says so,
	// which is what is asserted there instead of a silent gap.
	if runtime.GOOS != "darwin" {
		message := callToolError(t, instance, "control_browser", map[string]any{"action": "relaunch", "browser": "Midas E2E Browser"})
		if !strings.Contains(message, "not implemented") {
			t.Fatalf("relaunch on %s = %q", runtime.GOOS, message)
		}
		t.Skipf("relaunching has no %s implementation yet; the tool reported that", runtime.GOOS)
	}

	// Nothing is running yet, so there is no endpoint to find.
	before := callTool(t, instance, "control_browser", map[string]any{"action": "status", "browser": "Midas E2E Browser"})
	beforeStatus, _ := before["status"].(map[string]any)
	if beforeStatus == nil || field(t, beforeStatus, "port") != "" {
		t.Fatalf("a browser nobody started already has an endpoint: %#v", before)
	}

	relaunched := callTool(t, instance, "control_browser", map[string]any{
		"action": "relaunch", "browser": "Midas E2E Browser", "timeout": "30s",
	})
	status, _ := relaunched["status"].(map[string]any)
	if status == nil || field(t, status, "port") == "" || field(t, status, "url") == "" {
		t.Fatalf("relaunch = %#v", relaunched)
	}
	// The browser control started is really listening.
	if body := get(t, strings.TrimSuffix(field(t, status, "url"), "/")+"/json/version"); !strings.Contains(body, "webSocketDebuggerUrl") {
		t.Fatalf("the relaunched browser does not answer: %s", body)
	}
}

// gapList reads the gaps a state reading reported.
func gapList(state map[string]any) []string {
	gaps, _ := state["gaps"].([]any)
	texts := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		if text, ok := gap.(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// stopProcessesForProfile ends anything still running with this profile, which is
// how a test cleans up a browser a tool started rather than the test.
func stopProcessesForProfile(t *testing.T, profile string) {
	t.Helper()
	command := exec.Command("pkill", "-f", profile)
	_ = command.Run()
}
