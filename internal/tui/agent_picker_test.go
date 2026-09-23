package tui

import (
	"strings"
	"testing"

	"github.com/CarlvinceTan/midas/internal/profiles"
	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestAgentPickerNavigatesSelectsAndCancels(t *testing.T) {
	rows := []profiles.Profile{{Name: "main", Description: "Codes"}, {Name: "advisor", Description: "Advises"}}
	selected, cancelled := "", 0
	picker := NewAgentPicker(AgentPickerOptions{
		Profiles: rows, Current: "main", Theme: NewOneDarkTheme(ColorTrue),
		OnSelect: func(profile profiles.Profile) { selected = profile.Name }, OnCancel: func() { cancelled++ },
	})
	picker.HandleInput("\x1b[B")
	picker.HandleInput("\r")
	if selected != "advisor" {
		t.Fatalf("selected = %q", selected)
	}
	picker.HandleInput("\x1b")
	if cancelled != 1 {
		t.Fatalf("cancelled = %d", cancelled)
	}
}

func TestAgentPickerRendersStableRows(t *testing.T) {
	picker := NewAgentPicker(AgentPickerOptions{
		Profiles: []profiles.Profile{{Name: "main", Description: "Default interactive coding agent"}},
		Current:  "main", Theme: NewOneDarkTheme(ColorTrue),
	})
	lines := picker.Render(44)
	if len(lines) != 8 {
		t.Fatalf("height = %d", len(lines))
	}
	plain := tuitext.StripTerminalSequences(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "→ Main") || !strings.Contains(plain, "Default interactive") {
		t.Fatalf("render:\n%s", plain)
	}
}
