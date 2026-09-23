package main

import (
	"context"
	"fmt"
	"time"

	"github.com/CarlvinceTan/midas/pkg/control"
	"github.com/CarlvinceTan/midas/pkg/control/cdp"
)

// endpoints adapts the Chromium/Firefox endpoint layer to the interface the MCP
// surface expects. Housekeeping is a full build's privilege: this file is the only
// place that links it.
type endpoints struct{}

func (endpoints) Status(browser control.Browser) control.EndpointStatus {
	status := control.EndpointStatus{Browser: browser.Name, Engine: browser.Engine}
	if profile := cdp.ProfileDir(browser, ""); profile != "" {
		status.Profile = profile
	}
	if browser.BinaryPath == "" {
		status.State = "unknown"
		return status
	}
	port, _, ok := cdp.LiveEndpoint(browser, "")
	if ok {
		status.State, status.Port = "attached", port
		status.URL = "http://127.0.0.1:" + port
		return status
	}
	if port, _, stale := cdp.Endpoint(browser, ""); stale {
		// The file is there but nothing answers: the browser quit without cleaning
		// up, which is a different thing from never having an endpoint.
		status.State, status.Port = "stale", port
		status.Detail = "the browser left a DevToolsActivePort file behind but is not answering; relaunch it"
		return status
	}
	switch browser.Engine {
	case "firefox":
		status.Detail = "Firefox takes its endpoint from a profile preference; use enable with one of its profiles"
	case "safari":
		status.Detail = "Safari has no debugging endpoint; Control drives it over Apple Events"
	default:
		status.Detail = "no endpoint; relaunch the browser to enable one"
	}
	status.State = "absent"
	if browser.Engine == "chromium" || browser.Engine == "firefox" {
		status.Detail = status.Detail + fmt.Sprintf(" (profile %s)", status.Profile)
	}
	return status
}

func (endpoints) Attach(browser control.Browser) (string, error) {
	return cdp.EndpointURL(browser, "")
}

func (endpoints) Relaunch(ctx context.Context, browser control.Browser, timeout string) (string, error) {
	wait := 30 * time.Second
	if parsed, err := time.ParseDuration(timeout); err == nil && parsed > 0 {
		wait = parsed
	}
	// The profile has to be the one the endpoint is discovered in: a browser named
	// through the environment keeps its profile where the caller said, and a browser
	// started without that flag writes its endpoint somewhere else entirely.
	return cdp.Relaunch(browser, cdp.ProfileDir(browser, ""), wait)
}

func (endpoints) FirefoxProfiles(home string) ([]string, error) {
	return cdp.FirefoxProfiles(home)
}

func (endpoints) EnableFirefox(profileDir string, port int) error {
	return cdp.EnableFirefoxEndpoint(profileDir, port)
}

func (endpoints) DisableFirefox(profileDir string) error {
	return cdp.DisableFirefoxEndpoint(profileDir)
}

func (endpoints) AutomatedSetup(browser control.Browser) string {
	return cdp.AutomatedSetup(browser)
}

var _ control.EndpointManager = endpoints{}
