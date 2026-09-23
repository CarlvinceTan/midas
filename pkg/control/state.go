package control

import (
	"cmp"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// State is the ambient context the control skill collects at the start of a task:
// which browsers are open and what they are showing, which applications are
// running, and which permissions are in force.
//
// Collecting it never changes a setting and never activates, selects, or
// navigates anything: it is observation, and it degrades. Whatever a permission
// does not allow is reported in Gaps rather than failing the whole reading.
type State struct {
	Host        string         `json:"host"`
	Platform    string         `json:"platform"`
	Browsers    []BrowserState `json:"browsers,omitempty"`
	Apps        []AppState     `json:"apps,omitempty"`
	Frontmost   string         `json:"frontmost,omitempty"`
	Permissions Permissions    `json:"permissions"`
	Gaps        []string       `json:"gaps,omitempty"`
}

// BrowserState is one browser's windows and tabs.
type BrowserState struct {
	Name    string          `json:"name"`
	Running bool            `json:"running"`
	Windows []BrowserWindow `json:"windows,omitempty"`
	// Error explains why this browser contributed no state, usually a declined
	// consent dialog.
	Error string `json:"error,omitempty"`
}

// BrowserWindow is one window, with the tabs it holds.
type BrowserWindow struct {
	Title  string       `json:"title,omitempty"`
	Active int          `json:"activeTab,omitempty"`
	Tabs   []BrowserTab `json:"tabs,omitempty"`
}

// BrowserTab is one tab. Titles and URLs are context, never identity.
type BrowserTab struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// AppState is one running application.
type AppState struct {
	Name      string `json:"name"`
	Frontmost bool   `json:"frontmost,omitempty"`
	Windows   int    `json:"windows,omitempty"`
}

// Permissions records what the current session is allowed to do.
type Permissions struct {
	// Automation maps an application to whether Apple Events to it are allowed.
	Automation map[string]bool `json:"automation,omitempty"`
	// Accessibility is "granted", "denied", or "unknown"; it gates UI element
	// access and other applications' window titles.
	Accessibility string `json:"accessibility"`
}

// CollectOptions are the inputs to a state reading. Everything is injectable so
// the collector can be tested without a desktop.
type CollectOptions struct {
	// Run executes a script and returns its output. Required.
	Run func(ctx context.Context, script string) (string, error)
	// Browsers to read. Empty means every installed browser.
	Browsers []Browser
	// Running reports whether a browser is up. Defaults to the platform probe.
	Running func(Browser) bool
	// IncludeApps reads the running application list, which needs one consent
	// dialog for System Events the first time.
	IncludeApps bool
	// HostName labels the device. Defaults to the machine's hostname.
	HostName string
}

// Collect reads the current state, degrading rather than failing: a browser whose
// consent was declined appears with an explanation, and the rest of the reading
// still arrives.
func Collect(ctx context.Context, options CollectOptions) (State, error) {
	if options.Run == nil {
		return State{}, fmt.Errorf("control: a script runner is required")
	}
	host := options.HostName
	if host == "" {
		host = hostname()
	}
	state := State{Host: host, Platform: runtime.GOOS, Permissions: Permissions{Automation: map[string]bool{}}}

	browsers := options.Browsers
	if len(browsers) == 0 {
		browsers = Installed()
	}
	running := options.Running
	if running == nil {
		running = Running
	}
	for _, browser := range browsers {
		reading := BrowserState{Name: browser.Name, Running: running(browser)}
		if !reading.Running {
			state.Browsers = append(state.Browsers, reading)
			state.Permissions.Automation[browser.Name] = true
			continue
		}
		script, err := browserStateScript(browser)
		if err != nil {
			reading.Error = err.Error()
			state.Browsers = append(state.Browsers, reading)
			continue
		}
		output, err := options.Run(ctx, script)
		if err != nil {
			// A declined or missing consent is expected, not a failure of the
			// whole reading: report it and carry on.
			reading.Error = err.Error()
			state.Permissions.Automation[browser.Name] = false
			state.Gaps = append(state.Gaps, fmt.Sprintf("%s state unavailable: %s", browser.Name, err.Error()))
			state.Browsers = append(state.Browsers, reading)
			continue
		}
		state.Permissions.Automation[browser.Name] = true
		reading.Windows = parseBrowserWindows(output)
		state.Browsers = append(state.Browsers, reading)
	}

	if options.IncludeApps {
		output, err := options.Run(ctx, appsScript)
		if err != nil {
			state.Gaps = append(state.Gaps, "application list unavailable: "+err.Error())
		} else {
			state.Apps, state.Frontmost = parseApps(output)
		}
	}

	// Accessibility gates window titles and UI elements. Probing it must not
	// change anything, so this only reads a title and reports the outcome:
	// reading one means it is granted, a refusal names the toggle to enable, and
	// no output at all means there was nothing to read.
	switch output, err := options.Run(ctx, accessibilityProbeScript); {
	case err == nil && strings.TrimSpace(output) != "":
		state.Permissions.Accessibility = "granted"
	case err == nil:
		state.Permissions.Accessibility = "unknown"
		state.Gaps = append(state.Gaps, "Accessibility could not be probed: no frontmost window to read a title from")
	default:
		state.Permissions.Accessibility = "denied"
		state.Gaps = append(state.Gaps, "window titles and UI elements need Accessibility, which is off for this binary; enable it in System Settings → Privacy & Security → Accessibility")
	}
	slices.SortStableFunc(state.Apps, func(a, b AppState) int { return cmp.Compare(a.Name, b.Name) })
	return state, nil
}

// browserStateScript returns the AppleScript that reads one browser's windows and
// tabs. Chromium-family browsers and Safari expose the same shapes with different
// property names for the selected tab.
func browserStateScript(browser Browser) (string, error) {
	switch browser.Engine {
	case "chromium":
		return `tell application "` + browser.Name + `"
	set output to ""
	repeat with w in windows
		set output to output & "W" & tab & (name of w) & tab & (active tab index of w) & linefeed
		repeat with t in tabs of w
			set output to output & "T" & tab & (name of t) & tab & (URL of t) & linefeed
		end repeat
	end repeat
	return output
end tell`, nil
	case "safari":
		return `tell application "Safari"
	set output to ""
	repeat with w in windows
		set output to output & "W" & tab & (name of w) & tab & (index of current tab of w) & linefeed
		repeat with t in tabs of w
			set output to output & "T" & tab & (name of t) & tab & (URL of t) & linefeed
		end repeat
	end repeat
	return output
end tell`, nil
	default:
		return "", fmt.Errorf("control: %s has no scripting dictionary, so its state needs an endpoint rather than Apple Events", browser.Name)
	}
}

// appsScript lists running applications. This needs Automation consent for System
// Events but not Accessibility: it reads the process list, not UI elements.
const appsScript = `tell application "System Events"
	set output to ""
	repeat with p in processes
		set output to output & (name of p) & tab & (frontmost of p) & linefeed
	end repeat
	return output
end tell`

// accessibilityProbeScript reads one window title, which only succeeds when
// Accessibility is granted.
const accessibilityProbeScript = `tell application "System Events"
	return name of first window of (first process whose frontmost is true)
end tell`

// parseBrowserWindows reads the tab-separated output of a browser script.
func parseBrowserWindows(output string) []BrowserWindow {
	windows := []BrowserWindow{}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSpace(fields[0]) {
		case "W":
			windows = append(windows, BrowserWindow{Title: strings.TrimSpace(fields[1]), Active: atoi(fields, 2)})
		case "T":
			if len(windows) == 0 {
				continue
			}
			tab := BrowserTab{}
			if len(fields) > 1 {
				tab.Title = strings.TrimSpace(fields[1])
			}
			if len(fields) > 2 {
				tab.URL = strings.TrimSpace(fields[2])
			}
			windows[len(windows)-1].Tabs = append(windows[len(windows)-1].Tabs, tab)
		}
	}
	return windows
}

// parseApps reads the tab-separated application list and reports which app is
// frontmost.
func parseApps(output string) ([]AppState, string) {
	apps := []AppState{}
	frontmost := ""
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		name := strings.TrimSpace(fields[0])
		isFront := strings.EqualFold(strings.TrimSpace(fields[1]), "true")
		if isFront {
			frontmost = name
		}
		apps = append(apps, AppState{Name: name, Frontmost: isFront})
	}
	return apps, frontmost
}

func atoi(fields []string, index int) int {
	if len(fields) <= index {
		return 0
	}
	value := 0
	for _, character := range strings.TrimSpace(fields[index]) {
		if character < '0' || character > '9' {
			return value
		}
		value = value*10 + int(character-'0')
	}
	return value
}

func hostname() string {
	name, err := osHostname()
	if err != nil {
		return ""
	}
	return name
}
