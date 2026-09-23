//go:build !darwin

package cdp

import (
	"fmt"
	"github.com/CarlvinceTan/midas/pkg/control"
	"time"
)

// Relaunch is only implemented on macOS for now: Linux and Windows quit and start
// their browsers differently enough that they need their own implementation
// rather than a shared guess.
func Relaunch(browser control.Browser, userDataDir string, timeout time.Duration) (string, error) {
	return RelaunchWithPort(browser, userDataDir, 0, timeout)
}

// RelaunchWithPort is Relaunch on a chosen port.
func RelaunchWithPort(browser control.Browser, userDataDir string, port int, timeout time.Duration) (string, error) {
	return RelaunchWithFlags(browser, userDataDir, port, nil, timeout)
}

// RelaunchWithFlags is RelaunchWithPort with extra launch flags.
func RelaunchWithFlags(browser control.Browser, userDataDir string, port int, extra []string, timeout time.Duration) (string, error) {
	return "", fmt.Errorf("control: relaunching %s is not implemented on this platform yet", browser.Name)
}

// Quit asks a browser to close.
func Quit(browser control.Browser) error {
	return fmt.Errorf("control: quitting %s is not implemented on this platform yet", browser.Name)
}
