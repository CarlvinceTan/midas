package tui

import (
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestTextViewWrapsAndCancels(t *testing.T) {
	cancelled := false
	view := &TextView{Text: "one two three four\n\nlast", OnCancel: func() { cancelled = true }}
	lines := view.Render(8)
	if len(lines) < 4 {
		t.Fatalf("lines = %#v", lines)
	}
	for _, line := range lines {
		if tuitext.VisibleWidth(line) > 8 {
			t.Fatalf("wide line = %q", line)
		}
	}
	view.HandleInput("\x1b")
	if !cancelled {
		t.Fatal("escape did not cancel")
	}
}
