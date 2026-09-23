package tui

import (
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

// TextView is a small read-only overlay body used for status and list commands.
// It deliberately owns no data discovery; callers provide Midas-scoped text.
type TextView struct {
	FocusState
	Text     string
	OnCancel func()
}

func (v *TextView) Invalidate() {}

func (v *TextView) HandleInput(data string) {
	if (MatchesKey(data, "escape") || MatchesKey(data, "ctrl+c")) && v.OnCancel != nil {
		v.OnCancel()
	}
}

func (v *TextView) Render(width int) []string {
	width = max(1, width)
	if v.Text == "" {
		return []string{""}
	}
	lines := make([]string, 0)
	for _, logical := range strings.Split(v.Text, "\n") {
		wrapped := tuitext.WrapTextWithAnsi(logical, width)
		if len(wrapped) == 0 {
			lines = append(lines, "")
		} else {
			lines = append(lines, wrapped...)
		}
	}
	return lines
}
