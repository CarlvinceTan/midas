package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type terminalCapabilitiesReferenceFixture struct {
	Cases []struct {
		Name           string            `json:"name"`
		Env            map[string]string `json:"env"`
		TmuxHyperlinks bool              `json:"tmuxHyperlinks"`
		Result         struct {
			Images     string `json:"images"`
			TrueColor  bool   `json:"trueColor"`
			Hyperlinks bool   `json:"hyperlinks"`
		} `json:"result"`
	} `json:"cases"`
}

func TestTerminalCapabilitiesMatchReference(t *testing.T) {
	fixtureData, err := os.ReadFile(filepath.Join("testdata", "terminal-capabilities-reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture terminalCapabilitiesReferenceFixture
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"TERM_PROGRAM", "TERMINAL_EMULATOR", "TERM", "COLORTERM", "TMUX", "KITTY_WINDOW_ID",
		"GHOSTTY_RESOURCES_DIR", "WEZTERM_PANE", "WARP_SESSION_ID", "WARP_TERMINAL_SESSION_UUID",
		"ITERM_SESSION_ID", "WT_SESSION", "MIDAS_IMAGE_PROTOCOL", "MIDAS_TRUE_COLOR", "MIDAS_HYPERLINKS",
	}
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			for _, key := range keys {
				t.Setenv(key, "")
			}
			for key, value := range testCase.Env {
				t.Setenv(key, value)
			}
			actual := detectTerminalCapabilitiesWith(func() bool { return testCase.TmuxHyperlinks })
			expected := TerminalCapabilities{
				Images: ImageProtocol(testCase.Result.Images), TrueColor: testCase.Result.TrueColor, Hyperlinks: testCase.Result.Hyperlinks,
			}
			if actual != expected {
				t.Fatalf("capabilities mismatch:\n actual: %#v\nexpected: %#v", actual, expected)
			}
		})
	}
}
