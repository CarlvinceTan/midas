// Package control gives an agent programmatic access to the desktop it runs on:
// which devices, windows, and browser tabs exist, and how to act on them. It is
// a library with no agent-specific policy, so the MCP server, the TUI and any
// other caller share one implementation.
package control

import (
	"os"
	"os/exec"
	"strings"
)

// Browser is an installed browser the control layer can attach to.
type Browser struct {
	// Name is the display name, exactly as the app shows it, so a setup result
	// can carry the same name the user already knows.
	Name string
	// BundlePath is the .app bundle, e.g. /Applications/Helium.app.
	BundlePath string
	// BinaryPath is the executable inside the bundle.
	BinaryPath string
	// Engine is "chromium" or "firefox": they differ in how the debugging
	// endpoint is enabled.
	Engine string
	// ProfileDir is the user-data directory the endpoint is discovered in. It is
	// empty for the known desktop browsers, which each have their own convention,
	// and set for a browser named by the environment.
	ProfileDir string
}

// Environment variables that name the browser control should use. A deployment
// without a browser in the usual place — a container, a CI runner, a second
// Chromium — sets the binary, and optionally the name, engine, and profile.
const (
	BrowserBinaryEnv  = "CONTROL_BROWSER_BINARY"
	BrowserNameEnv    = "CONTROL_BROWSER_NAME"
	BrowserEngineEnv  = "CONTROL_BROWSER_ENGINE"
	BrowserProfileEnv = "CONTROL_BROWSER_PROFILE"
)

// KnownBrowsers lists the browsers to look for, in the order they are preferred.
// A browser named by the environment comes first, so a deployment can point
// control at its own binary without pretending it is one of the desktop ones.
func KnownBrowsers() []Browser {
	browsers := []Browser{
		{Name: "Helium", BundlePath: "/Applications/Helium.app", BinaryPath: "/Applications/Helium.app/Contents/MacOS/Helium", Engine: "chromium"},
		{Name: "Google Chrome", BundlePath: "/Applications/Google Chrome.app", BinaryPath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", Engine: "chromium"},
		{Name: "Brave Browser", BundlePath: "/Applications/Brave Browser.app", BinaryPath: "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", Engine: "chromium"},
		{Name: "Microsoft Edge", BundlePath: "/Applications/Microsoft Edge.app", BinaryPath: "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", Engine: "chromium"},
		{Name: "Firefox", BundlePath: "/Applications/Firefox.app", BinaryPath: "/Applications/Firefox.app/Contents/MacOS/firefox", Engine: "firefox"},
		{Name: "Safari", BundlePath: "/Applications/Safari.app", BinaryPath: "/Applications/Safari.app/Contents/MacOS/Safari", Engine: "safari"},
	}
	if custom, ok := BrowserFromEnvironment(); ok {
		browsers = append([]Browser{custom}, browsers...)
	}
	return browsers
}

// BrowserFromEnvironment reads the browser a deployment named, if any. The binary
// is required; the name, engine, and profile have sensible defaults for a
// Chromium that keeps its own user-data directory.
func BrowserFromEnvironment() (Browser, bool) {
	binary := strings.TrimSpace(os.Getenv(BrowserBinaryEnv))
	if binary == "" {
		return Browser{}, false
	}
	browser := Browser{
		Name: "Chromium", BinaryPath: binary, Engine: "chromium",
		ProfileDir: strings.TrimSpace(os.Getenv(BrowserProfileEnv)),
	}
	if name := strings.TrimSpace(os.Getenv(BrowserNameEnv)); name != "" {
		browser.Name = name
	}
	if engine := strings.TrimSpace(os.Getenv(BrowserEngineEnv)); engine != "" {
		browser.Engine = engine
	}
	return browser, true
}

// Installed lists the browsers actually present on this machine.
func Installed() []Browser {
	installed := []Browser{}
	for _, browser := range KnownBrowsers() {
		if _, err := os.Stat(browser.BinaryPath); err == nil {
			installed = append(installed, browser)
		}
	}
	return installed
}

// Find looks a browser up by name, case-insensitively.
func Find(name string) (Browser, bool) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	for _, browser := range KnownBrowsers() {
		if strings.ToLower(browser.Name) == wanted {
			return browser, true
		}
	}
	return Browser{}, false
}

// Running reports whether a browser process is up. It is part of the lean layer
// because state collection asks it before touching any browser, and a browser
// that is not running must not cost a consent dialog.
func Running(browser Browser) bool {
	command := exec.Command("pgrep", "-f", browser.BinaryPath)
	return command.Run() == nil
}
