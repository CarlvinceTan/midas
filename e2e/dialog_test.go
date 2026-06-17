package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// Dialog handling. Listeners are dispatched off the CDP read loop, so a
// listener may call dialog.Accept/Dismiss directly (the natural pattern).

func TestDialogAlertCapturedAndAccepted(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/dialogs.html")

	got := make(chan string, 1)
	remove := page.AddDialogListener(func(d *browser.Dialog) {
		got <- d.Message()
		_ = d.Accept(context.Background())
	})
	defer remove()

	// Trigger asynchronously so the click/eval call returns immediately.
	if err := page.Evaluate(testCtx(t), "setTimeout(() => alert('alert message'), 0); true", new(bool)); err != nil {
		t.Fatalf("trigger alert: %v", err)
	}

	select {
	case msg := <-got:
		if msg != "alert message" {
			t.Errorf("dialog message = %q, want 'alert message'", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("alert dialog was never delivered to the listener")
	}
}

func TestDialogConfirmAcceptReturnsTrue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/dialogs.html")

	remove := page.AddDialogListener(func(d *browser.Dialog) {
		_ = d.Accept(context.Background())
	})
	defer remove()

	// confirm() blocks until handled; with an accepting listener it returns true.
	var result bool
	if err := page.Evaluate(testCtx(t), "confirm('proceed?')", &result); err != nil {
		t.Fatalf("evaluate confirm: %v", err)
	}
	if !result {
		t.Error("confirm should return true when the dialog is accepted")
	}
}

func TestDialogConfirmDismissReturnsFalse(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/dialogs.html")

	remove := page.AddDialogListener(func(d *browser.Dialog) {
		_ = d.Dismiss(context.Background())
	})
	defer remove()

	var result bool
	if err := page.Evaluate(testCtx(t), "confirm('proceed?')", &result); err != nil {
		t.Fatalf("evaluate confirm: %v", err)
	}
	if result {
		t.Error("confirm should return false when the dialog is dismissed")
	}
}

func TestDialogPromptAcceptsText(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/dialogs.html")

	remove := page.AddDialogListener(func(d *browser.Dialog) {
		if d.Type() != browser.DialogTypePrompt {
			t.Errorf("dialog type = %q, want prompt", d.Type())
		}
		if d.DefaultPrompt() != "default text" {
			t.Errorf("default prompt = %q, want 'default text'", d.DefaultPrompt())
		}
		_ = d.Accept(context.Background(), "typed answer")
	})
	defer remove()

	var result string
	if err := page.Evaluate(testCtx(t), "prompt('your answer', 'default text')", &result); err != nil {
		t.Fatalf("evaluate prompt: %v", err)
	}
	if result != "typed answer" {
		t.Errorf("prompt result = %q, want 'typed answer'", result)
	}
}

func TestDialogTriggeredByClick(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/dialogs.html")

	remove := page.AddDialogListener(func(d *browser.Dialog) {
		_ = d.Accept(context.Background())
	})
	defer remove()

	// Clicking the confirm button runs confirm() in its onclick; the accepting
	// listener lets the click complete and sets window.result to "accepted".
	if err := page.Locator("#confirm").Click(testCtx(t)); err != nil {
		t.Fatalf("click confirm button: %v", err)
	}
	waitForCondition(t, page, "window.result === 'accepted'", 5*time.Second)
}
