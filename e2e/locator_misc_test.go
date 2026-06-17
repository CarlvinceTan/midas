package e2e

import (
	"strings"
	"testing"
)

func TestLocatorCount(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/links.html")

	n, err := page.Locator("nav a").Count(testCtx(t))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3 nav links", n)
	}
}

func TestLocatorNthAndFirst(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/links.html")

	first, err := page.Locator("nav a").First().TextContent(testCtx(t))
	if err != nil {
		t.Fatalf("first text: %v", err)
	}
	if strings.TrimSpace(first) != "Button page" {
		t.Errorf("first link text = %q, want 'Button page'", first)
	}
	second, err := page.Locator("nav a").Nth(1).TextContent(testCtx(t))
	if err != nil {
		t.Fatalf("nth text: %v", err)
	}
	if strings.TrimSpace(second) != "Form page" {
		t.Errorf("second link text = %q, want 'Form page'", second)
	}
}

func TestLocatorCheckAndUncheck(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/checkbox.html")

	box := page.Locator("#check")
	if checked, _ := box.IsChecked(testCtx(t)); checked {
		t.Fatal("precondition: #check should start unchecked")
	}
	if err := box.Check(testCtx(t)); err != nil {
		t.Fatalf("check: %v", err)
	}
	if checked, _ := box.IsChecked(testCtx(t)); !checked {
		t.Error("expected checked after Check()")
	}
	if err := box.Uncheck(testCtx(t)); err != nil {
		t.Fatalf("uncheck: %v", err)
	}
	if checked, _ := box.IsChecked(testCtx(t)); checked {
		t.Error("expected unchecked after Uncheck()")
	}
}

func TestLocatorCheckIsIdempotent(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/checkbox.html")

	box := page.Locator("#checked-box") // starts checked
	if checked, _ := box.IsChecked(testCtx(t)); !checked {
		t.Fatal("precondition: #checked-box should start checked")
	}
	if err := box.Check(testCtx(t)); err != nil {
		t.Fatalf("check on already-checked: %v", err)
	}
	if checked, _ := box.IsChecked(testCtx(t)); !checked {
		t.Error("Check() on already-checked box should remain checked")
	}
}

func TestLocatorRadioCheck(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/checkbox.html")

	if err := page.Locator("#radio2").Check(testCtx(t)); err != nil {
		t.Fatalf("check radio: %v", err)
	}
	if checked, _ := page.Locator("#radio2").IsChecked(testCtx(t)); !checked {
		t.Error("expected radio2 checked")
	}
	if checked, _ := page.Locator("#radio1").IsChecked(testCtx(t)); checked {
		t.Error("radio1 should be unchecked after radio2 selected")
	}
}

func TestLocatorSelectOption(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/select.html")

	if err := page.Locator("#single").SelectOption(testCtx(t), "banana"); err != nil {
		t.Fatalf("select option: %v", err)
	}
	got := evalString(t, page, "document.getElementById('single').value")
	if got != "banana" {
		t.Errorf("select value = %q, want banana", got)
	}
	var events []string
	if err := page.Evaluate(testCtx(t), "window.events", &events); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !contains(events, "change") {
		t.Errorf("expected a change event on select, got %v", events)
	}
}

func TestLocatorSelectOptionByLabel(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/select.html")

	// SelectOption matches by value OR visible text.
	if err := page.Locator("#single").SelectOption(testCtx(t), "Cherry"); err != nil {
		t.Fatalf("select by label: %v", err)
	}
	got := evalString(t, page, "document.getElementById('single').value")
	if got != "cherry" {
		t.Errorf("select value = %q, want cherry", got)
	}
}

func TestLocatorTextContentInnerTextInnerHTML(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/content.html", `<!DOCTYPE html>
<title>Content</title>
<div id="box"><b>bold</b> and normal</div>`)
	gotoPath(t, page, "/content.html")

	box := page.Locator("#box")
	if got, _ := box.TextContent(testCtx(t)); strings.TrimSpace(got) != "bold and normal" {
		t.Errorf("TextContent = %q", got)
	}
	if got, _ := box.InnerText(testCtx(t)); !strings.Contains(got, "bold and normal") {
		t.Errorf("InnerText = %q", got)
	}
	if got, _ := box.InnerHTML(testCtx(t)); !strings.Contains(got, "<b>bold</b>") {
		t.Errorf("InnerHTML = %q, want it to contain the <b> markup", got)
	}
}

func TestLocatorBoundingBox(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	box, err := page.Locator("button").BoundingBox(testCtx(t))
	if err != nil {
		t.Fatalf("bounding box: %v", err)
	}
	if box.Width <= 0 || box.Height <= 0 {
		t.Errorf("bounding box has non-positive dimensions: %+v", box)
	}
}

func TestLocatorIsVisible(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/vis.html", `<!DOCTYPE html>
<title>Vis</title>
<div id="shown">shown</div>
<div id="gone" style="display:none">gone</div>`)
	gotoPath(t, page, "/vis.html")

	if v, _ := page.Locator("#shown").IsVisible(testCtx(t)); !v {
		t.Error("#shown should be visible")
	}
	if v, _ := page.Locator("#gone").IsVisible(testCtx(t)); v {
		t.Error("#gone (display:none) should not be visible")
	}
}

func TestLocatorCountZeroForMissing(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	n, err := page.Locator(".does-not-exist").Count(testCtx(t))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 for a missing selector", n)
	}
}
