package tui

import (
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

func TestToastUsesLegacyPaddingAndWarningColor(t *testing.T) {
	line := renderToast("No provider selected.", ToastWarning, toastWidth("No provider selected.", 78))
	if tuitext.StripTerminalSequences(line) != " No provider selected. " || line[:10] != "\x1b[43m\x1b[30m" {
		t.Fatalf("toast = %q", line)
	}
}
