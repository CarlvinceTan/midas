package e2e

import (
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// WaitForSelector state semantics, modeled on Playwright's
// page-wait-for-selector suite.

func TestWaitForSelectorAttachedImmediate(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	ok, err := page.WaitForSelector(testCtx(t), "button", browser.WaitForSelectorOptions{
		State: browser.SelectorStateAttached,
	})
	if err != nil {
		t.Fatalf("wait attached: %v", err)
	}
	if !ok {
		t.Error("expected button to be attached")
	}
}

func TestWaitForSelectorVisibleAppearsLater(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/late-visible.html", `<!DOCTYPE html>
<title>Late</title>
<div id="target" style="display:none">here</div>
<script>setTimeout(() => document.getElementById('target').style.display = '', 500);</script>`)
	gotoPath(t, page, "/late-visible.html")

	start := time.Now()
	ok, err := page.WaitForSelector(testCtx(t), "#target", browser.WaitForSelectorOptions{
		State: browser.SelectorStateVisible,
	})
	if err != nil {
		t.Fatalf("wait visible: %v", err)
	}
	if !ok {
		t.Fatal("expected element to become visible")
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("returned in %s — should have waited for the element to appear", elapsed)
	}
}

func TestWaitForSelectorHiddenWhenRemoved(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/late-hidden.html", `<!DOCTYPE html>
<title>Hide</title>
<div id="target">here</div>
<script>setTimeout(() => document.getElementById('target').remove(), 500);</script>`)
	gotoPath(t, page, "/late-hidden.html")

	ok, err := page.WaitForSelector(testCtx(t), "#target", browser.WaitForSelectorOptions{
		State: browser.SelectorStateHidden,
	})
	if err != nil {
		t.Fatalf("wait hidden: %v", err)
	}
	if !ok {
		t.Error("expected element to become hidden/removed")
	}
}

func TestWaitForSelectorHiddenSatisfiedByDisplayNone(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/already-hidden.html", `<!DOCTYPE html>
<title>Hidden</title>
<div id="target" style="display:none">here</div>`)
	gotoPath(t, page, "/already-hidden.html")

	ok, err := page.WaitForSelector(testCtx(t), "#target", browser.WaitForSelectorOptions{
		State:   browser.SelectorStateHidden,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("wait hidden: %v", err)
	}
	if !ok {
		t.Error("display:none element should satisfy hidden immediately")
	}
}

func TestWaitForSelectorTimesOutWhenAbsent(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	start := time.Now()
	ok, err := page.WaitForSelector(testCtx(t), "#never-appears", browser.WaitForSelectorOptions{
		State:   browser.SelectorStateVisible,
		Timeout: 1 * time.Second,
	})
	if err == nil {
		t.Fatal("expected timeout error for an absent selector")
	}
	if ok {
		t.Error("expected ok=false on timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took %s, expected ~1s", elapsed)
	}
}

func TestWaitForSelectorVisibleAddedToDOMLater(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetContent(t, "/late-added.html", `<!DOCTYPE html>
<title>Add</title>
<div id="root"></div>
<script>setTimeout(() => {
  const b = document.createElement('button');
  b.id = 'added';
  b.textContent = 'Added';
  document.getElementById('root').appendChild(b);
}, 400);</script>`)
	gotoPath(t, page, "/late-added.html")

	ok, err := page.WaitForSelector(testCtx(t), "#added", browser.WaitForSelectorOptions{
		State: browser.SelectorStateVisible,
	})
	if err != nil {
		t.Fatalf("wait for late-added element: %v", err)
	}
	if !ok {
		t.Error("expected late-added element to be found")
	}
}
