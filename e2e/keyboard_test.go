package e2e

import (
	"strings"
	"testing"
)

func TestPressEnterSubmitsForm(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/form.html")

	if err := page.Locator("#username").Fill(testCtx(t), "carl"); err != nil {
		t.Fatalf("fill username: %v", err)
	}
	if err := page.Locator("#email").Fill(testCtx(t), "carl@example.com"); err != nil {
		t.Fatalf("fill email: %v", err)
	}
	if err := page.Locator("#username").Press(testCtx(t), "Enter"); err != nil {
		t.Fatalf("press Enter: %v", err)
	}
	status := evalString(t, page, "document.getElementById('status').textContent")
	if !strings.HasPrefix(status, "submitted:") {
		t.Errorf("form not submitted on Enter, status = %q", status)
	}
}

func TestPressNamedKeys(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/keyboard.html")

	loc := page.Locator("textarea")
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if err := page.KeyPress(testCtx(t), key); err != nil {
			t.Fatalf("press %q: %v", key, err)
		}
	}
	log := evalString(t, page, "window.getLog()")
	if !strings.Contains(log, "keydown: a") {
		t.Errorf("keyboard log missing keydown for 'a':\n%s", log)
	}
	if !strings.Contains(log, "input: abc") {
		t.Errorf("keyboard log missing input value abc:\n%s", log)
	}
}

func TestPressBackspaceDeletesCharacter(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Fill(testCtx(t), "abcd"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}
	// Move caret to end, then backspace once.
	if err := page.Evaluate(testCtx(t), "(() => { const el = document.getElementById('input'); el.focus(); el.setSelectionRange(4,4); return true; })()", new(bool)); err != nil {
		t.Fatalf("set caret: %v", err)
	}
	if err := page.KeyPress(testCtx(t), "Backspace"); err != nil {
		t.Fatalf("press Backspace: %v", err)
	}
	got, err := loc.InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "abc" {
		t.Errorf("value = %q, want abc after backspace", got)
	}
}

func TestTypeWithDelayProducesCorrectValue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/keyboard.html")

	loc := page.Locator("textarea")
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if err := loc.Type(testCtx(t), "hi", 0); err != nil {
		t.Fatalf("type: %v", err)
	}
	got, err := loc.InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "hi" {
		t.Errorf("value = %q, want hi", got)
	}
}
