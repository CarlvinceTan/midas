//go:build darwin

package cdp

import (
	"fmt"
	"github.com/CarlvinceTan/midas/pkg/control"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Relaunch quits a Chromium browser and starts it again with its debugging
// endpoint enabled, then waits for that endpoint to answer.
//
// It starts the vendor's own binary, so nothing is installed, copied, or
// pinned: the browser the user already has is the one that comes back, with the
// same profile and the same session. What it cannot do is enable the endpoint on
// a browser that is *already* running — Chromium hands a second launch off to the
// running process and ignores the flag — which is why this quits first, and why
// it must be the user's decision rather than something an agent does quietly.
func Relaunch(browser control.Browser, userDataDir string, timeout time.Duration) (string, error) {
	return RelaunchWithPort(browser, userDataDir, 0, timeout)
}

// RelaunchWithPort is Relaunch on a chosen port. Zero asks the browser to pick
// one, which is what a server wants: no port to collide with, and the endpoint is
// discovered from DevToolsActivePort either way.
func RelaunchWithPort(browser control.Browser, userDataDir string, port int, timeout time.Duration) (string, error) {
	return RelaunchWithFlags(browser, userDataDir, port, nil, timeout)
}

// RelaunchWithFlags is RelaunchWithPort with extra launch flags, which is how a
// server adds the container-specific ones without the desktop path inheriting
// them.
func RelaunchWithFlags(browser control.Browser, userDataDir string, port int, extra []string, timeout time.Duration) (string, error) {
	if browser.Engine != "chromium" {
		return "", fmt.Errorf("control: %s does not take an endpoint flag; Firefox uses a profile preference and Safari a Develop-menu toggle", browser.Name)
	}
	if _, err := os.Stat(browser.BinaryPath); err != nil {
		return "", fmt.Errorf("control: %s is not installed at %s", browser.Name, browser.BinaryPath)
	}
	_ = Quit(browser)
	// A browser that ignores the quit would hold the profile and swallow the flag.
	deadline := time.Now().Add(10 * time.Second)
	for control.Running(browser) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if control.Running(browser) {
		return "", fmt.Errorf("control: %s did not quit, so its endpoint cannot be enabled", browser.Name)
	}

	arguments := []string{fmt.Sprintf("--remote-debugging-port=%d", port)}
	if strings.TrimSpace(userDataDir) != "" {
		arguments = append(arguments, "--user-data-dir="+userDataDir)
	}
	arguments = append(arguments, extra...)
	process := exec.Command(browser.BinaryPath, arguments...)
	if err := process.Start(); err != nil {
		return "", fmt.Errorf("control: start %s with its endpoint: %w", browser.Name, err)
	}
	go func() { _ = process.Wait() }()
	return WaitForEndpoint(browser, userDataDir, timeout)
}

// Quit asks a browser to close, which is what makes a relaunch possible.
func Quit(browser control.Browser) error {
	return exec.Command("osascript", "-e", "tell application \""+browser.Name+"\" to quit").Run()
}
