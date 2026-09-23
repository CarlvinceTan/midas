package tui

import (
	"strings"
	"testing"
)

// TestRenderMarkdownSurvivesSplitRunes: provider deltas and pastes can end or
// begin mid-rune, and the inline styler used to slice one byte too far and panic.
func TestRenderMarkdownSurvivesSplitRunes(t *testing.T) {
	for _, source := range []string{
		"\xff",
		"caf\xe9",
		"**bold** \xff",
		"\xe2\x82",
		"`code` \xf0\x9f",
		"\xff\xfe\xfd",
	} {
		lines := RenderMarkdown(source, 20, nil)
		if len(lines) == 0 {
			t.Fatalf("RenderMarkdown(%q) rendered nothing", source)
		}
		// A replacement character is acceptable for invalid input; the point is
		// that rendering finished and produced lines.
		_ = strings.Join(lines, "\n")
	}
}

// TestEditorVerticalMovementAfterEdits: the cached layout is invalidated by an
// edit, so moving up and down before the next frame cannot index stale bytes.
func TestEditorVerticalMovementAfterEdits(t *testing.T) {
	editor := NewEditor(EditorOptions{})
	editor.SetText("first line\nsecond line\nthird line")
	editor.Render(40)
	// Delete most of the text, then move as a keystroke would, before any render.
	for index := 0; index < 2; index++ {
		editor.HandleInput("\x7f")
	}
	for _, key := range []string{"\x1b[A", "\x1b[B", "\x1b[A", "\x1b[B"} {
		editor.HandleInput(key)
	}
	if editor.Text() == "" {
		t.Fatal("the editor lost all of its text")
	}
}
