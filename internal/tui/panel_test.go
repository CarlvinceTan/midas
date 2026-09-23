package tui

import (
	"strings"
	"testing"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

type panelFixture struct {
	FocusState
	inputs []string
}

func (p *panelFixture) Render(int) []string {
	return []string{"hello", "a line that is deliberately long"}
}
func (p *panelFixture) Invalidate()             {}
func (p *panelFixture) HandleInput(data string) { p.inputs = append(p.inputs, data) }

func TestPanelMatchesRoundedOverlayGeometry(t *testing.T) {
	child := &panelFixture{}
	panel := NewPanel("models", child)
	panel.Theme = NewOneDarkTheme(ColorTrue)
	lines := panel.Render(18)
	if len(lines) != 4 {
		t.Fatalf("lines = %#v", lines)
	}
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = tuitext.StripTerminalSequences(line)
		plain[index] = strings.ReplaceAll(plain[index], ContentStartMarker, "")
		plain[index] = strings.ReplaceAll(plain[index], ContentEndMarker, "")
		plain[index] = strings.ReplaceAll(plain[index], tuitext.DecorationMarker, "")
		if width := tuitext.VisibleWidth(line); width != 18 {
			t.Fatalf("line %d width = %d: %q", index, width, line)
		}
	}
	if !strings.HasSuffix(plain[0], "╭─ Models ───────╮") || !strings.HasSuffix(plain[3], "╰────────────────╯") {
		t.Fatalf("frame = %#v", plain)
	}
	if !strings.Contains(plain[2], "a line that i…") {
		t.Fatalf("truncation = %q", plain[2])
	}
	panel.SetFocused(true)
	if !child.IsFocused() {
		t.Fatal("focus was not delegated")
	}
	panel.HandleInput("x")
	if len(child.inputs) != 1 || child.inputs[0] != "x" {
		t.Fatalf("inputs = %#v", child.inputs)
	}
}

func TestOneDarkThemeUsesReferenceColors(t *testing.T) {
	trueColor := NewOneDarkTheme(ColorTrue)
	if got := trueColor.FG("accent", "x"); got != "\x1b[38;2;97;175;239mx\x1b[39m" {
		t.Fatalf("accent = %q", got)
	}
	indexed := NewOneDarkTheme(Color256)
	if got := indexed.FG("accent", "x"); !strings.HasPrefix(got, "\x1b[38;5;") {
		t.Fatalf("indexed accent = %q", got)
	}
}
