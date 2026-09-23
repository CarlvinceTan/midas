package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names. They are stable per flavour so a client's request prefix does not
// change under it.
const (
	ToolState       = "control_state"
	ToolPermissions = "control_permissions"
	ToolBrowser     = "control_browser"
)

// ServerOptions configure a Control MCP server.
type ServerOptions struct {
	// Run executes AppleScript. Defaults to the platform runner.
	Run func(ctx context.Context, script string) (string, error)
	// HostName labels the device in state readings.
	HostName string
	// Endpoints manages browser debugging endpoints. Supplying one adds the
	// browser tool; leaving it nil exposes state and guidance only, which is what
	// a program without the endpoint layer wants.
	Endpoints EndpointManager
}

// EndpointManager is the debugging-endpoint layer a full build provides. It is an
// interface so this package stays free of the Chromium machinery, and so the lean
// flavour simply passes nothing.
type EndpointManager interface {
	// Status reports whether a browser has a reachable endpoint.
	Status(browser Browser) EndpointStatus
	// Attach returns the HTTP base of a live endpoint.
	Attach(browser Browser) (string, error)
	// Relaunch quits and restarts the browser so an endpoint exists.
	Relaunch(ctx context.Context, browser Browser, timeout string) (string, error)
	// FirefoxProfiles lists an installed Firefox's profile directories, so the
	// browser tool can pick one when the caller names none.
	FirefoxProfiles(home string) ([]string, error)
	// EnableFirefox writes the preferences that give Firefox an endpoint, and
	// DisableFirefox removes them again.
	EnableFirefox(profileDir string, port int) error
	DisableFirefox(profileDir string) error
	// AutomatedSetup names what this build can do instead of asking the user,
	// empty for anything it cannot.
	AutomatedSetup(browser Browser) string
}

// EndpointStatus is what a browser's debugging endpoint looks like from here.
type EndpointStatus struct {
	Browser string `json:"browser"`
	Engine  string `json:"engine"`
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Port    string `json:"port,omitempty"`
	Profile string `json:"profile,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// NewServer builds the MCP server. The tool list depends only on the
// capabilities, never on what is installed or open.
func NewServer(options ServerOptions) *mcp.Server {
	run := options.Run
	if run == nil {
		run = RunScript
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "control", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Read the desktop an agent runs on: devices, applications, windows and browser tabs. Reading state never changes a setting; a browser debugging endpoint is only for page JavaScript and screenshots, and only after the user agrees.",
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        ToolState,
		Description: "Read ambient state: running browsers with their windows and tabs, running applications, what the session is permitted to see, and any gaps in the reading. Observation only: this never activates, selects, or navigates anything.",
	}, stateTool(run, options.HostName))
	mcp.AddTool(server, &mcp.Tool{
		Name:        ToolPermissions,
		Description: "Report what each browser needs before it can be read or controlled, what is missing right now, and which parts (if any) this build can set up for you.",
	}, permissionsTool(run, options))
	if options.Endpoints != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        ToolBrowser,
			Description: "Inspect a browser's debugging endpoint, attach to it, or enable it. Enabling a Chromium browser means quitting and relaunching it, which closes the user's windows, so ask before calling it; Firefox takes a profile preference instead and needs no restart.",
		}, browserTool(options.Endpoints))
	}
	return server
}

type stateInput struct {
	IncludeApps *bool `json:"includeApps,omitempty" jsonschema:"also read the running application list, which asks for consent for System Events once"`
}

func stateTool(run func(context.Context, string) (string, error), host string) mcp.ToolHandlerFor[stateInput, State] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input stateInput) (*mcp.CallToolResult, State, error) {
		includeApps := true
		if input.IncludeApps != nil {
			includeApps = *input.IncludeApps
		}
		state, err := Collect(ctx, CollectOptions{Run: run, IncludeApps: includeApps, HostName: host})
		if err != nil {
			return nil, State{}, err
		}
		return nil, state, nil
	}
}

type permissionsInput struct {
	Browser string `json:"browser,omitempty" jsonschema:"limit the report to one browser"`
}

type PermissionReport struct {
	Permissions Permissions    `json:"permissions"`
	Browsers    []Instructions `json:"browsers"`
	Gaps        []string       `json:"gaps,omitempty"`
}

func permissionsTool(run func(context.Context, string) (string, error), options ServerOptions) mcp.ToolHandlerFor[permissionsInput, PermissionReport] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input permissionsInput) (*mcp.CallToolResult, PermissionReport, error) {
		state, err := Collect(ctx, CollectOptions{Run: run, Browsers: browsersFor(input.Browser)})
		if err != nil {
			return nil, PermissionReport{}, err
		}
		report := PermissionReport{Permissions: state.Permissions, Gaps: state.Gaps, Browsers: []Instructions{}}
		for _, browser := range Installed() {
			if !matchesBrowser(browser, input.Browser) {
				continue
			}
			instructions := Instruct(browser)
			if options.Endpoints != nil {
				instructions.Automated = options.Endpoints.AutomatedSetup(browser)
			}
			report.Browsers = append(report.Browsers, instructions)
		}
		return nil, report, nil
	}
}

type browserInput struct {
	Action  string `json:"action" jsonschema:"status, attach, relaunch, enable, or disable"`
	Browser string `json:"browser" jsonschema:"browser name, for example Helium or Firefox"`
	Profile string `json:"profile,omitempty" jsonschema:"Firefox or Chromium profile directory to use"`
	Port    int    `json:"port,omitempty" jsonschema:"port for the Firefox endpoint, default 9222"`
	Timeout string `json:"timeout,omitempty" jsonschema:"how long to wait for an endpoint after a relaunch, default 30s"`
}

type BrowserResult struct {
	Status  EndpointStatus `json:"status"`
	Message string         `json:"message,omitempty"`
}

func browserTool(endpoints EndpointManager) mcp.ToolHandlerFor[browserInput, BrowserResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input browserInput) (*mcp.CallToolResult, BrowserResult, error) {
		browser, ok := Find(input.Browser)
		if !ok {
			return nil, BrowserResult{}, fmt.Errorf("unknown browser %q; known: %s", input.Browser, browserNames())
		}
		switch strings.ToLower(strings.TrimSpace(input.Action)) {
		case "", "status":
			return nil, BrowserResult{Status: endpoints.Status(browser)}, nil
		case "attach":
			url, err := endpoints.Attach(browser)
			if err != nil {
				return nil, BrowserResult{Status: endpoints.Status(browser)}, err
			}
			status := endpoints.Status(browser)
			status.URL = url
			return nil, BrowserResult{Status: status, Message: "attached"}, nil
		case "relaunch":
			timeout := strings.TrimSpace(input.Timeout)
			if timeout == "" {
				timeout = "30s"
			}
			url, err := endpoints.Relaunch(ctx, browser, timeout)
			if err != nil {
				return nil, BrowserResult{Status: endpoints.Status(browser)}, err
			}
			status := endpoints.Status(browser)
			status.URL = url
			return nil, BrowserResult{Status: status, Message: "relaunched with its endpoint enabled"}, nil
		case "enable", "disable":
			if browser.Engine != "firefox" {
				return nil, BrowserResult{}, fmt.Errorf("%s does not take profile preferences; use relaunch for a Chromium browser", browser.Name)
			}
			profile := strings.TrimSpace(input.Profile)
			if profile == "" {
				profiles, err := endpoints.FirefoxProfiles("")
				if err != nil {
					return nil, BrowserResult{}, err
				}
				if len(profiles) == 0 {
					return nil, BrowserResult{}, fmt.Errorf("no Firefox profile found; is Firefox installed and has it run once?")
				}
				profile = profiles[0]
			}
			if strings.EqualFold(input.Action, "enable") {
				if err := endpoints.EnableFirefox(profile, input.Port); err != nil {
					return nil, BrowserResult{}, err
				}
				return nil, BrowserResult{Status: endpoints.Status(browser), Message: "preferences written to " + profile + "; restart Firefox once"}, nil
			}
			if err := endpoints.DisableFirefox(profile); err != nil {
				return nil, BrowserResult{}, err
			}
			return nil, BrowserResult{Status: endpoints.Status(browser), Message: "preferences removed from " + profile}, nil
		default:
			return nil, BrowserResult{}, fmt.Errorf("unknown action %q", input.Action)
		}
	}
}

func browsersFor(name string) []Browser {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	browser, ok := Find(name)
	if !ok {
		return nil
	}
	return []Browser{browser}
}

func matchesBrowser(browser Browser, name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	return strings.EqualFold(browser.Name, strings.TrimSpace(name))
}

func browserNames() string {
	names := []string{}
	for _, browser := range KnownBrowsers() {
		names = append(names, browser.Name)
	}
	return strings.Join(names, ", ")
}
