package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestInputMasksSecretWithoutChangingSubmittedValue(t *testing.T) {
	submitted := ""
	input := NewInput(InputOptions{Prompt: "Key: ", Secret: true, OnSubmit: func(value string) { submitted = value }})
	input.SetFocused(true)
	input.HandleInput("sëcret")
	rendered := tuitext.StripTerminalSequences(strings.Join(input.Render(40), "\n"))
	if strings.Contains(rendered, "sëcret") || strings.Count(rendered, "•") != 6 {
		t.Fatalf("masked input = %q", rendered)
	}
	input.HandleInput("\r")
	if submitted != "sëcret" {
		t.Fatalf("submitted secret = %q", submitted)
	}
}
