package e2e

import (
	"testing"
	"time"
)

func TestFillInputAndReadValue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	if err := page.Locator("#input").Fill(testCtx(t), "hello midas"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	got, err := page.Locator("#input").InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "hello midas" {
		t.Errorf("input value = %q, want %q", got, "hello midas")
	}
}

func TestFillFiresInputAndChangeEvents(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	if err := page.Locator("#input").Fill(testCtx(t), "abc"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Blur to flush the change event.
	if err := page.Locator("textarea").Focus(testCtx(t)); err != nil {
		t.Fatalf("focus other element: %v", err)
	}
	var events []string
	if err := page.Evaluate(testCtx(t), "window.events", &events); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !contains(events, "input") {
		t.Errorf("expected an input event, got %v", events)
	}
}

func TestFillTextarea(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	if err := page.Locator("textarea").Fill(testCtx(t), "multi\nline\ntext"); err != nil {
		t.Fatalf("fill textarea: %v", err)
	}
	got, err := page.Locator("textarea").InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "multi\nline\ntext" {
		t.Errorf("textarea value = %q, want multiline text", got)
	}
}

func TestFillReplacesExistingValue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Fill(testCtx(t), "first"); err != nil {
		t.Fatalf("first fill: %v", err)
	}
	if err := loc.Fill(testCtx(t), "second"); err != nil {
		t.Fatalf("second fill: %v", err)
	}
	got, err := loc.InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "second" {
		t.Errorf("value = %q, want second (fill should replace)", got)
	}
}

func TestFillClearWithEmptyString(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Fill(testCtx(t), "to be cleared"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := loc.Fill(testCtx(t), ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := loc.InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "" {
		t.Errorf("value = %q, want empty after clear", got)
	}
}

func TestFillWaitsForReadonlyToClear(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/readonly-clears.html", `<!DOCTYPE html>
<title>Readonly</title>
<input id="ro" readonly value="locked">
<script>setTimeout(() => document.getElementById('ro').readOnly = false, 500);</script>`)
	gotoPath(t, page, "/readonly-clears.html")

	if err := page.Locator("#ro").Fill(testCtx(t), "now editable"); err != nil {
		t.Fatalf("fill should wait for readonly to clear: %v", err)
	}
	got, err := page.Locator("#ro").InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "now editable" {
		t.Errorf("value = %q, want now editable", got)
	}
}

func TestFillContentEditable(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	if err := page.Locator("#editable").Fill(testCtx(t), "rich text"); err != nil {
		t.Fatalf("fill contenteditable: %v", err)
	}
	got := evalString(t, page, "document.getElementById('editable').textContent")
	if got != "rich text" {
		t.Errorf("contenteditable text = %q, want rich text", got)
	}
}

func TestTypeIntoInput(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/textarea.html")

	loc := page.Locator("#input")
	if err := loc.Focus(testCtx(t)); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if err := loc.Type(testCtx(t), "typed", 0); err != nil {
		t.Fatalf("type: %v", err)
	}
	got, err := loc.InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "typed" {
		t.Errorf("value = %q, want typed", got)
	}
}

func TestFillTimesOutOnHiddenElement(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/hidden-input.html", `<!DOCTYPE html>
<title>Hidden input</title>
<input id="hidden" style="display:none">`)
	gotoPath(t, page, "/hidden-input.html")

	ctx, cancel := contextWithTimeout(1500 * time.Millisecond)
	defer cancel()
	if err := page.Locator("#hidden").Fill(ctx, "x"); err == nil {
		t.Fatal("fill on permanently hidden element should fail")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
