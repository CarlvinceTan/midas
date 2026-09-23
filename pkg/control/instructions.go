package control

import "fmt"

// Instructions explains how to make a browser reachable without an agent
// installing anything. Two browsers have a real setting; Chromium deliberately
// has none, which is why its entry names a launch flag instead of pretending to
// offer a toggle.
type Instructions struct {
	Browser string `json:"browser"`
	Engine  string `json:"engine"`
	// Setting is what the user changes, when a setting exists.
	Setting string `json:"setting,omitempty"`
	// Steps are the manual steps, in order.
	Steps []string `json:"steps"`
	// Automated names what a full build can do instead of asking the user. The
	// lean layer leaves it empty: it cannot relaunch a browser or write Firefox
	// preferences, and promising otherwise would be a lie in a lean binary.
	Automated string `json:"automated,omitempty"`
	// Note carries the caveat that makes this browser different.
	Note string `json:"note,omitempty"`
}

// Instruct returns the setup path for a browser.
func Instruct(browser Browser) Instructions {
	switch browser.Engine {
	case "firefox":
		return Instructions{
			Browser: browser.Name, Engine: browser.Engine,
			Setting: "devtools.debugger.remote-enabled = true",
			Steps: []string{
				"Open about:config.",
				"Set devtools.debugger.remote-enabled to true.",
				"Set devtools.debugger.remote-port to a port you keep, e.g. 9222.",
				"Restart Firefox once.",
			},
			Note: "Firefox has no scripting dictionary, so unlike Safari and Chromium it cannot be read or driven without an endpoint. The preference route is durable: no launcher and no flag.",
		}
	case "safari":
		return Instructions{
			Browser: browser.Name, Engine: browser.Engine,
			Setting: "Develop → Allow Remote Automation",
			Steps: []string{
				"Settings → Advanced → turn on \"Show features for web developers\" if the Develop menu is not visible.",
				"Develop → Allow Remote Automation for WebDriver control, and leave the Develop menu's JavaScript-from-Apple-Events toggle on for scripting.",
				"Approve the \"wants to control Safari\" dialog the first time Control asks.",
			},
			Note: "Safari has no Chrome DevTools Protocol: Control drives it through Apple Events, which needs no port at all.",
		}
	default:
		return Instructions{
			Browser: browser.Name, Engine: browser.Engine,
			Steps: []string{
				"Quit " + browser.Name + " completely.",
				"Start it with --remote-debugging-port=0 (for example a shell alias, a shortcut, or a .desktop entry).",
			},
			Note: fmt.Sprintf("Windows and tabs need no endpoint at all: %s answers Apple Events, so Control reads and drives them after the one-time \"wants to control\" consent. A debug port is only for executing JavaScript in a page, screenshots, and console or network inspection. Chromium ignores the flag when that profile is already running, and Google Chrome 136 and later refuse it on the default profile, so Chrome needs its own --user-data-dir and a one-time sign-in; Helium does not.", browser.Name),
		}
	}
}
