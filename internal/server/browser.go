package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/control"
	"github.com/CarlvinceTan/midas/pkg/control/cdp"
)

// startBrowser brings the shared browser up with its debugging endpoint and
// returns the endpoint URL. The environment owns the profile, so a restart keeps
// the logins an agent established, and no agent has to know any of this.
func startBrowser(ctx context.Context, config BrowserConfig, tuning Tuning) (string, error) {
	if _, err := os.Stat(config.Binary); err != nil {
		return "", fmt.Errorf("server: browser binary %s is missing from the image", config.Binary)
	}
	// The engine is Chromium-family in every deployment we target: the URL is what
	// the CDP client needs, and the name is only used for messages.
	browser := control.Browser{Name: filepathBase(config.Binary), BinaryPath: config.Binary, Engine: "chromium"}
	if endpoint, _, live := cdp.LiveEndpoint(browser, config.ProfileDir); live {
		return "http://127.0.0.1:" + endpoint, nil
	}
	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	return cdp.RelaunchWithFlags(browser, config.ProfileDir, config.Port, tuning.BrowserFlags(config.Binary, config.ProfileDir, config.Port), timeout)
}

func filepathBase(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
