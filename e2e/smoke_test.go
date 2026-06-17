package e2e

import (
	"strings"
	"testing"
)

func TestSmokeGotoClickEvaluate(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	title, err := page.Title(testCtx(t))
	if err != nil {
		t.Fatalf("title: %v", err)
	}
	if title != "Button test" {
		t.Fatalf("title = %q, want %q", title, "Button test")
	}

	if err := page.Locator("button").Click(testCtx(t)); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Fatalf("window.result = %q, want %q", got, "Clicked")
	}
}

func TestSmokeFillAndInputValue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	if err := page.Locator("#name").Fill(testCtx(t), "midas"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	got, err := page.Locator("#name").InputValue(testCtx(t))
	if err != nil {
		t.Fatalf("input value: %v", err)
	}
	if got != "midas" {
		t.Fatalf("input value = %q, want %q", got, "midas")
	}
}

func TestSmokeSnapshotContainsButton(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(snap.FormattedTree, "Click target") {
		t.Fatalf("snapshot tree missing button text:\n%s", snap.FormattedTree)
	}
	if len(snap.XPathMap) == 0 {
		t.Fatal("snapshot XPathMap is empty")
	}
}
