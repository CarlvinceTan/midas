package e2e

import (
	"strings"
	"testing"
	"time"
)

// Actionability tests modeled on Playwright's page-click suite: click must
// scroll into view, wait for visibility/enabled/hit-target, and must have no
// side effects after a timeout.

func TestClickScrollsElementIntoView(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/scrollable.html")

	if err := page.Locator("#bottom-button").Click(testCtx(t)); err != nil {
		t.Fatalf("click offscreen button: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Bottom clicked" {
		t.Errorf("window.result = %q, want %q", got, "Bottom clicked")
	}
	if y := evalInt(t, page, "window.scrollY"); y == 0 {
		t.Error("page did not scroll before clicking offscreen element")
	}
}

func TestClickWaitsForOverlayToDisappear(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/overlay.html?temp")

	// Overlay removes itself after 600ms; click must wait it out, not misclick.
	if err := page.Locator("#target").Click(testCtx(t)); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Errorf("window.result = %q, want %q", got, "Clicked")
	}
}

func TestClickPermanentOverlayFailsWithoutSideEffects(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/overlay.html")

	ctx, cancel := contextWithTimeout(2 * time.Second)
	defer cancel()
	err := page.Locator("#target").Click(ctx)
	if err == nil {
		t.Fatal("click on permanently covered button should fail")
	}
	// Playwright names the occluding element in the error; midas's
	// actionability layer documents the same ("click point covered by ...").
	if !strings.Contains(err.Error(), "covered") && !strings.Contains(err.Error(), "context deadline") {
		t.Errorf("error should mention occlusion or timeout, got: %v", err)
	}

	// Negative contract: after a failed click, no late click may land.
	time.Sleep(500 * time.Millisecond)
	if got := evalString(t, page, "window.result"); got != "Was not clicked" {
		t.Errorf("click landed after timeout: window.result = %q", got)
	}
}

func TestClickWaitsForDisabledButtonToEnable(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/disabled.html?enable")

	if err := page.Locator("#btn").Click(testCtx(t)); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Errorf("window.result = %q, want %q", got, "Clicked")
	}
}

func TestClickDisabledButtonTimesOutWithoutSideEffects(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/disabled.html")

	ctx, cancel := contextWithTimeout(1500 * time.Millisecond)
	defer cancel()
	if err := page.Locator("#btn").Click(ctx); err == nil {
		t.Fatal("click on permanently disabled button should fail")
	}
	time.Sleep(300 * time.Millisecond)
	if got := evalString(t, page, "window.result"); got != "Was not clicked" {
		t.Errorf("click landed on disabled button: window.result = %q", got)
	}
}

func TestClickHiddenElementWaitsForVisibility(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/hidden-button.html", `<!DOCTYPE html>
<title>Hidden</title>
<button id="btn" style="display:none" onclick="window.result='Clicked'">Hidden button</button>
<script>
  window.result = 'Was not clicked';
  setTimeout(() => document.getElementById('btn').style.display = '', 500);
</script>`)
	gotoPath(t, page, "/hidden-button.html")

	if err := page.Locator("#btn").Click(testCtx(t)); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Errorf("window.result = %q, want %q", got, "Clicked")
	}
}

func TestDblClickFiresDblClickEvent(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/dblclick.html", `<!DOCTYPE html>
<title>Dblclick</title>
<button id="btn">Double me</button>
<script>
  window.clicks = 0; window.dblclicks = 0;
  const btn = document.getElementById('btn');
  btn.addEventListener('click', () => window.clicks++);
  btn.addEventListener('dblclick', () => window.dblclicks++);
</script>`)
	gotoPath(t, page, "/dblclick.html")

	if err := page.Locator("#btn").DblClick(testCtx(t)); err != nil {
		t.Fatalf("dblclick: %v", err)
	}
	if got := evalInt(t, page, "window.dblclicks"); got != 1 {
		t.Errorf("dblclick count = %d, want 1", got)
	}
	if got := evalInt(t, page, "window.clicks"); got != 2 {
		t.Errorf("click count = %d, want 2 (two clicks of the double-click)", got)
	}
}

func TestClickByCoordinates(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	box, err := page.Locator("button").BoundingBox(testCtx(t))
	if err != nil {
		t.Fatalf("bounding box: %v", err)
	}
	if box.Width <= 0 || box.Height <= 0 {
		t.Fatalf("bounding box has empty geometry: %+v", box)
	}
	if err := page.Click(testCtx(t), box.X+box.Width/2, box.Y+box.Height/2, 1); err != nil {
		t.Fatalf("coordinate click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Errorf("window.result = %q, want %q", got, "Clicked")
	}
}

func TestClickShadowDOMButton(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/shadow.html")

	if err := page.DeepLocator("#shadow-button").Click(testCtx(t)); err != nil {
		t.Fatalf("click shadow button: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Shadow clicked" {
		t.Errorf("window.result = %q, want %q", got, "Shadow clicked")
	}
}

func TestClickNonexistentSelectorFails(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	ctx, cancel := contextWithTimeout(1500 * time.Millisecond)
	defer cancel()
	if err := page.Locator("#no-such-element").Click(ctx); err == nil {
		t.Fatal("click on nonexistent selector should fail")
	}
}

func TestHoverTriggersHoverState(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/hover.html", `<!DOCTYPE html>
<title>Hover</title>
<button id="btn">Hover me</button>
<script>
  window.hovered = false;
  document.getElementById('btn').addEventListener('mouseenter', () => window.hovered = true);
</script>`)
	gotoPath(t, page, "/hover.html")

	if err := page.Locator("#btn").Hover(testCtx(t)); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if !evalBool(t, page, "window.hovered") {
		t.Error("mouseenter did not fire on hover")
	}
}
