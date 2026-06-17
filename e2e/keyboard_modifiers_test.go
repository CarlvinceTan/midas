package e2e

import (
	"testing"

	"github.com/PolymuxOrg/midas/tools"
)

// Modifier combos. polymux's agent exposes key_down/key_up specifically for
// modifier keys (Shift, Control, Alt, Meta), composed with key presses/clicks.
// These exercise the held-modifier state on a persistent Page — the
// tools.BoundService integration path polymux uses.

func TestModifierControlASelectsAll(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Fill(testCtx(t), "select me"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}

	// Control+A should select the whole field.
	if err := page.KeyDown(testCtx(t), "Control"); err != nil {
		t.Fatalf("keydown Control: %v", err)
	}
	if err := page.KeyPress(testCtx(t), "a"); err != nil {
		t.Fatalf("press a: %v", err)
	}
	if err := page.KeyUp(testCtx(t), "Control"); err != nil {
		t.Fatalf("keyup Control: %v", err)
	}

	selLen := evalInt(t, page, "(() => { const el = document.getElementById('input'); return el.selectionEnd - el.selectionStart; })()")
	if selLen != len("select me") {
		t.Errorf("Control+A selected %d chars, want %d (whole field)", selLen, len("select me"))
	}
}

func TestModifierShiftArrowExtendsSelection(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Fill(testCtx(t), "abcdef"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}
	// Move caret home, then Shift+ArrowRight ×3 to select "abc".
	evalString(t, page, "(() => { const el = document.getElementById('input'); el.setSelectionRange(0,0); return ''; })()")

	if err := page.KeyDown(testCtx(t), "Shift"); err != nil {
		t.Fatalf("keydown Shift: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := page.KeyPress(testCtx(t), "ArrowRight"); err != nil {
			t.Fatalf("press ArrowRight: %v", err)
		}
	}
	if err := page.KeyUp(testCtx(t), "Shift"); err != nil {
		t.Fatalf("keyup Shift: %v", err)
	}

	if got := evalInt(t, page, "(() => { const el = document.getElementById('input'); return el.selectionEnd - el.selectionStart; })()"); got != 3 {
		t.Errorf("Shift+ArrowRight×3 selected %d chars, want 3", got)
	}
}

func TestModifierComboViaTools(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")
	h := requireHarness(t)

	if err := page.Locator("#input").Fill(testCtx(t), "tools select"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := page.Locator("#input").Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}

	svc := tools.NewService(h.bctx)
	// key_down / keys / key_up form the combo through the polymux tool surface.
	if _, err := svc.Execute(testCtx(t), "key_down", map[string]any{"key": "Control"}); err != nil {
		t.Fatalf("key_down tool: %v", err)
	}
	if _, err := svc.Execute(testCtx(t), "keys", map[string]any{"key": "a"}); err != nil {
		t.Fatalf("keys tool: %v", err)
	}
	if _, err := svc.Execute(testCtx(t), "key_up", map[string]any{"key": "Control"}); err != nil {
		t.Fatalf("key_up tool: %v", err)
	}

	selLen := evalInt(t, page, "(() => { const el = document.getElementById('input'); return el.selectionEnd - el.selectionStart; })()")
	if selLen != len("tools select") {
		t.Errorf("Control+A via tools selected %d chars, want %d", selLen, len("tools select"))
	}
}

func TestKeyToolsRegistered(t *testing.T) {
	h := requireHarness(t)
	svc := tools.NewService(h.bctx)
	have := map[string]bool{}
	for _, s := range svc.Specs() {
		have[s.Name] = true
	}
	for _, name := range []string{"key_down", "key_up"} {
		if !have[name] {
			t.Errorf("tool registry missing %q (needed for polymux modifier combos)", name)
		}
	}
}
