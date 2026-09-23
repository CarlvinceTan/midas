package control

import (
	"strings"
	"testing"
)

func TestKnownBrowsersCarryTheNamesUsersSee(t *testing.T) {
	for _, browser := range KnownBrowsers() {
		if browser.Name == "" || browser.BundlePath == "" || browser.BinaryPath == "" {
			t.Fatalf("incomplete browser entry: %#v", browser)
		}
		// The executable lives inside the bundle; its file name does not always
		// match the app's (Firefox ships a lowercase binary).
		if !strings.HasSuffix(browser.BundlePath, ".app") || !strings.HasPrefix(browser.BinaryPath, browser.BundlePath+"/Contents/") {
			t.Fatalf("browser paths disagree: %#v", browser)
		}
	}
	if _, ok := Find("helium"); !ok {
		t.Fatal("lookup is not case-insensitive")
	}
	if _, ok := Find("nope"); ok {
		t.Fatal("an unknown browser was found")
	}
}

// TestInstructionsNameTheRealMechanismPerBrowser keeps the guidance honest: no
// browser claims a setting it does not have, and every entry says what the
// endpoint is actually for.
func TestInstructionsNameTheRealMechanismPerBrowser(t *testing.T) {
	for _, browser := range KnownBrowsers() {
		instructions := Instruct(browser)
		if instructions.Browser != browser.Name || len(instructions.Steps) == 0 {
			t.Fatalf("instructions = %#v", instructions)
		}
		if browser.Engine == "firefox" {
			if !strings.Contains(instructions.Setting, "devtools.debugger.remote-enabled") {
				t.Fatalf("firefox setting = %q", instructions.Setting)
			}
			if !strings.Contains(instructions.Note, "no scripting dictionary") {
				t.Fatalf("firefox note = %q", instructions.Note)
			}
		}
		if browser.Engine == "chromium" {
			if instructions.Setting != "" {
				t.Fatalf("%s claims a setting that does not exist: %q", browser.Name, instructions.Setting)
			}
			// State and navigation need no port, so an agent must not ask for a
			// relaunch it does not need.
			for _, want := range []string{"Apple Events", "debug port"} {
				if !strings.Contains(instructions.Note, want) {
					t.Fatalf("%s note is missing %q: %q", browser.Name, want, instructions.Note)
				}
			}
		}
		// The lean layer never promises automation it cannot perform.
		if instructions.Automated != "" {
			t.Fatalf("%s advertises automation from the lean layer: %q", browser.Name, instructions.Automated)
		}
	}
	chrome, _ := Find("Google Chrome")
	if note := Instruct(chrome).Note; !strings.Contains(note, "136") || !strings.Contains(note, "user-data-dir") {
		t.Fatalf("chrome note = %q", note)
	}
	safari, _ := Find("Safari")
	if instructions := Instruct(safari); !strings.Contains(instructions.Note, "no port") {
		t.Fatalf("safari note = %q", instructions.Note)
	}
}
