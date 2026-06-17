package e2e

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// TestSliderSeek exercises the "address the component, not the pixel" design for
// valued widgets (sliders / scrubbers), with the headline case being a moving
// video timeline whose thumb is in continuous motion.
//
// It demonstrates, against a real browser:
//   - the value-API rung (native <input type=range> via Fill),
//   - the keyboard rung (digit / Home keys -> value, position-independent),
//   - the rail-geometry rung (compute target on the *stationary* rail, click/drag
//     there — never the moving thumb),
//
// and that the naive approach (resolve-then-click the moving thumb) hits the
// actionability stability gate because the thumb never holds still.
//
// The fixture's window.player mirrors HTMLMediaElement (currentTime/duration),
// so these same assertions transfer 1:1 to a real <video> scrubber.
func TestSliderSeek(t *testing.T) {
	page := newPage(t)
	// Pin the viewport so rail geometry is deterministic across machines.
	if err := page.SetViewportSize(testCtx(t), 1000, 700, 1); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	gotoPath(t, page, "/slider.html")

	const videoDuration = 120.0
	const moveTol = 4.0 // ±4s of a 120s timeline (~3.3%); covers seek + playback drift.

	// --- Finding: Fill() does NOT work on a native <input type=range>. Fill's
	// actionability preflight requires the element to be *editable*, and a range
	// input is not text-editable, so it polls until timeout. This is the concrete
	// gap a value-aware SetValue() would close (set .value + dispatch input/change
	// without the editability gate). We pin the behavior here. ---
	t.Run("native_fill_blocked_by_editability_gate", func(t *testing.T) {
		// Bound tightly: the real timeout is the editability check, not us.
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		err := page.Locator("#native").Fill(ctx, "60")
		if err == nil {
			t.Fatalf("expected Fill on a range input to be rejected by the editability gate, but it succeeded")
		}
		t.Logf("Fill on <input type=range> rejected as expected: %v", err)
	})

	// --- Rung 2 on the native range: keyboard works with zero special handling,
	// because the browser's own range implementation handles Home/End/Arrows. ---
	t.Run("native_keyboard_value", func(t *testing.T) {
		if err := page.Locator("#native").Press(testCtx(t), "End"); err != nil { // -> max
			t.Fatalf("press End: %v", err)
		}
		assertNear(t, "native value after End", evalFloat(t, page,
			"Number(document.getElementById('native').value)"), 100, 0.5)
		if err := page.Locator("#native").Press(testCtx(t), "Home"); err != nil { // -> min
			t.Fatalf("press Home: %v", err)
		}
		assertNear(t, "native value after Home", evalFloat(t, page,
			"Number(document.getElementById('native').value)"), 0, 0.5)
	})

	// --- Rung 3 on a *static* ARIA slider: click the rail at a computed fraction ---
	t.Run("aria_static_rail_click", func(t *testing.T) {
		box := mustBox(t, page, "#aria-rail")
		tx := box.X + 0.30*box.Width
		ty := box.Y + box.Height/2
		if err := page.Click(testCtx(t), tx, ty, 1); err != nil {
			t.Fatalf("click aria rail: %v", err)
		}
		got := evalFloat(t, page, "window.ariaSlider.value")
		assertNear(t, "aria value after rail click @30%", got, 30, 2)
	})

	// --- Rung 2 on the static slider: keyboard is purely value-based ---
	t.Run("aria_static_keyboard", func(t *testing.T) {
		if err := page.Locator("#aria-rail").Press(testCtx(t), "Home"); err != nil {
			t.Fatalf("press Home: %v", err)
		}
		if err := page.Locator("#aria-rail").Press(testCtx(t), "8"); err != nil { // digit -> 80%
			t.Fatalf("press 8: %v", err)
		}
		got := evalFloat(t, page, "window.ariaSlider.value")
		assertNear(t, "aria value after Home then digit-8", got, 80, 0.5)
	})

	// --- Establish that the playhead really is moving (and fast enough to defeat
	// the <1px/80ms stability gate), so the wall below is a real demonstration. ---
	t.Run("playhead_is_actually_moving", func(t *testing.T) {
		a := evalFloat(t, page, "window.player.currentTime")
		time.Sleep(200 * time.Millisecond)
		b := evalFloat(t, page, "window.player.currentTime")
		if b <= a {
			t.Fatalf("playhead not advancing: %.3f -> %.3f", a, b)
		}
		x1 := thumbLeftPx(t, page)
		time.Sleep(80 * time.Millisecond)
		x2 := thumbLeftPx(t, page)
		if math.Abs(x2-x1) < 1 {
			t.Fatalf("thumb moved <1px/80ms (%.2f -> %.2f) — won't exercise the stability gate", x1, x2)
		}
		t.Logf("playhead advancing %.2fs -> %.2fs; thumb %.2fpx -> %.2fpx over 80ms (Δ=%.2fpx)",
			a, b, x1, x2, math.Abs(x2-x1))
	})

	// --- The wall: a locator click aimed at the moving thumb can't satisfy
	// actionability, because the thumb never holds still for the 80ms window. ---
	t.Run("naive_thumb_click_hits_actionability_wall", func(t *testing.T) {
		// Bound tightly so we don't burn the full 30s actionability budget.
		ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := page.Locator("#video-thumb").Click(ctx)
		if err == nil {
			t.Fatalf("expected click on the moving thumb to fail the stability gate, but it succeeded")
		}
		t.Logf("naive moving-thumb click failed as designed after %s: %v",
			time.Since(start).Round(time.Millisecond), err)
	})

	// --- Rung 3 on the moving timeline: target the stationary rail at a fraction.
	// We never look at the thumb's live position. ---
	t.Run("seek_via_rail_geometry", func(t *testing.T) {
		const frac = 0.5
		box := mustBox(t, page, "#video-rail")
		tx := box.X + frac*box.Width
		ty := box.Y + box.Height/2
		if err := page.Click(testCtx(t), tx, ty, 1); err != nil {
			t.Fatalf("click video rail: %v", err)
		}
		got := evalFloat(t, page, "window.player.currentTime")
		assertNear(t, "currentTime after rail seek @50%", got, frac*videoDuration, moveTol)
	})

	// --- Drag-to-fraction on the moving timeline: press the rail, move to target,
	// release. The drag "grabs" the scrubber; the thumb's start position is
	// irrelevant. ---
	t.Run("seek_via_drag_to_fraction", func(t *testing.T) {
		box := mustBox(t, page, "#video-rail")
		ty := box.Y + box.Height/2
		fromX := box.X + 0.15*box.Width
		toX := box.X + 0.80*box.Width
		if err := page.DragAndDrop(testCtx(t), fromX, ty, toX, ty, 12); err != nil {
			t.Fatalf("drag video rail: %v", err)
		}
		got := evalFloat(t, page, "window.player.currentTime")
		assertNear(t, "currentTime after drag seek -> 80%", got, 0.80*videoDuration, moveTol)
	})

	// --- Keyboard on the moving timeline: digit key seeks by value, completely
	// independent of where the thumb currently is. ---
	t.Run("seek_via_keyboard_digit", func(t *testing.T) {
		if err := page.Locator("#video-rail").Press(testCtx(t), "3"); err != nil { // -> 30%
			t.Fatalf("press 3: %v", err)
		}
		got := evalFloat(t, page, "window.player.currentTime")
		assertNear(t, "currentTime after digit-3 seek", got, 0.30*videoDuration, moveTol)
	})
}

// evalFloat evaluates a JS expression and returns the numeric (float64) result.
func evalFloat(t *testing.T, page *browser.Page, expression string) float64 {
	t.Helper()
	var out float64
	if err := page.Evaluate(testCtx(t), expression, &out); err != nil {
		t.Fatalf("evaluate %q: %v", expression, err)
	}
	return out
}

// thumbLeftPx returns the live viewport-x of the video thumb's left edge.
func thumbLeftPx(t *testing.T, page *browser.Page) float64 {
	t.Helper()
	return evalFloat(t, page, "document.getElementById('video-thumb').getBoundingClientRect().left")
}

// mustBox resolves an element's bounding box or fails the test.
func mustBox(t *testing.T, page *browser.Page, selector string) *browser.ScreenshotClip {
	t.Helper()
	box, err := page.Locator(selector).BoundingBox(testCtx(t))
	if err != nil {
		t.Fatalf("bounding box %s: %v", selector, err)
	}
	return box
}

// assertNear logs the measured value and fails if it is not within tol of want.
func assertNear(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f ± %.3f (off by %.3f)", label, got, want, tol, math.Abs(got-want))
		return
	}
	t.Logf("%s = %.3f (want %.3f ± %.3f) ✓", label, got, want, tol)
}
