// Package cdp is the Chromium DevTools Protocol layer: discovering a browser's
// debugging endpoint, waiting for one, and enabling it when it is missing.
//
// It is a separate package from the state reader on purpose. What the control
// skill needs — which windows and tabs exist, and how to drive them — arrives
// over Apple Events with no endpoint at all, so a lean build links this package
// only when it actually wants JavaScript, screenshots, or Firefox.
package cdp

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/control"
)

// DefaultChromeProfile is where Google Chrome keeps its user data on macOS.
const DefaultChromeProfile = "Library/Application Support/Google/Chrome"

// ProfileDir is where a Chromium browser keeps its user data when the caller does
// not choose one. Chromium writes DevToolsActivePort there, which is how an
// attach finds the endpoint without guessing ports.
func ProfileDir(browser control.Browser, home string) string {
	if explicit := strings.TrimSpace(browser.ProfileDir); explicit != "" {
		// A browser named by the environment knows where its profile is; guessing
		// the desktop convention for it would be wrong.
		return explicit
	}
	if strings.TrimSpace(home) == "" {
		resolved, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = resolved
	}
	name := ProfileDirName(browser)
	if name == "" {
		// Firefox keeps profiles elsewhere, and guessing the home directory as a
		// profile is worse than saying nothing.
		return ""
	}
	return filepath.Join(home, name)
}

// ProfileDirName is the profile directory relative to a home directory.
func ProfileDirName(browser control.Browser) string {
	switch browser.Name {
	case "Helium":
		return "Library/Application Support/net.imput.helium"
	case "Google Chrome":
		return DefaultChromeProfile
	case "Brave Browser":
		return "Library/Application Support/BraveSoftware/Brave-Browser"
	case "Microsoft Edge":
		return "Library/Application Support/Microsoft Edge"
	}
	return ""
}

// DebugPortPath is the file a Chromium browser writes with the port and browser
// WebSocket path of its debugging endpoint, empty when it has not been enabled.
func DebugPortPath(browser control.Browser, userDataDir string) string {
	dir := userDataDir
	if dir == "" {
		dir = ProfileDir(browser, "")
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "DevToolsActivePort")
}

// Endpoint reads an enabled debugging endpoint: the port and the browser's
// WebSocket path. ok is false when the browser is not running with one.
func Endpoint(browser control.Browser, userDataDir string) (port string, path string, ok bool) {
	file := DebugPortPath(browser, userDataDir)
	if file == "" {
		return "", "", false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", "", false
	}
	port = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		path = strings.TrimSpace(lines[1])
	}
	return port, path, true
}

// LiveEndpoint reports an endpoint only when it actually answers. A browser that
// quits or is killed leaves DevToolsActivePort behind, and trusting that stale
// file is how an attach ends up talking to a port nobody is listening on.
func LiveEndpoint(browser control.Browser, userDataDir string) (port string, path string, ok bool) {
	port, path, ok = Endpoint(browser, userDataDir)
	if !ok {
		return "", "", false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + port + "/json/version")
	if err != nil {
		return "", "", false
	}
	// The body is drained and closed so the probe does not leave the connection
	// half-read; the contents are not needed.
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return "", "", false
	}
	return port, path, true
}

// EndpointURL is the HTTP base of a live endpoint.
func EndpointURL(browser control.Browser, userDataDir string) (string, error) {
	port, _, ok := LiveEndpoint(browser, userDataDir)
	if !ok {
		return "", fmt.Errorf("control: %s is not running with a reachable debugging endpoint", browser.Name)
	}
	return "http://127.0.0.1:" + port, nil
}

// WaitForEndpoint polls until the browser publishes a live endpoint, which is how
// a relaunch confirms the flag took effect instead of assuming it did.
func WaitForEndpoint(browser control.Browser, userDataDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if port, _, ok := LiveEndpoint(browser, userDataDir); ok {
			return "http://127.0.0.1:" + port, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("control: %s did not publish a debugging endpoint within %s", browser.Name, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// AutomatedSetup names what this layer can do for a browser beyond instructing
// the user, so a lean build can leave it empty rather than promise it.
func AutomatedSetup(browser control.Browser) string {
	switch browser.Engine {
	case "firefox":
		return "Control writes these preferences into every Firefox profile's user.js on request, so no manual about:config work is needed and they survive restarts."
	case "chromium":
		return "Control can quit and relaunch it for you on request, and every window opened afterwards is reachable without another restart."
	}
	return ""
}
