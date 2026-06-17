package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// TestYouTubeSeek drives a REAL YouTube video and seeks its progress bar with
// the same two strategies proven deterministically in TestSliderSeek:
//
//   - rail geometry: compute target-x on the stationary .ytp-progress-bar and
//     click there (never the moving scrubber thumb),
//   - keyboard: focus the player and press a digit (YouTube's 0-9 = 0%..90%).
//
// After each seek it reads the real HTMLMediaElement.currentTime to confirm the
// playhead landed near the target — exactly the assertion the fixture models.
//
// Network + live YouTube, so it is opt-in and tolerant of consent/bot walls:
//
//	MIDAS_E2E_YOUTUBE=1 go test ./e2e -run TestYouTubeSeek -v -count=1
func TestYouTubeSeek(t *testing.T) {
	if os.Getenv("MIDAS_E2E_YOUTUBE") == "" {
		t.Skip("set MIDAS_E2E_YOUTUBE=1 to run the live YouTube seek test")
	}
	// Tight bound: digit seek is percentage-exact and a rail click is pixel-exact;
	// the only slack is YouTube snapping to the nearest keyframe.
	const seekTolSec = 10.0
	page := newPage(t)

	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShort()
	if err := page.SetViewportSize(short, 1280, 720, 1); err != nil {
		t.Fatalf("set viewport: %v", err)
	}

	// Big Buck Bunny (Blender, ~10 min) — long enough that seeks are unambiguous.
	// The watch page is top-level, so the <video> and .ytp-progress-bar live in
	// the main frame (the /embed/ page returns Error 153 without a parent origin).
	const videoID = "aqz-KE-bpKQ"
	url := "https://www.youtube.com/watch?v=" + videoID + "&hl=en"

	navCtx, cancelNav := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelNav()
	if _, err := page.Goto(navCtx, url); err != nil {
		t.Fatalf("goto youtube watch: %v", err)
	}
	if title, err := page.Title(short); err == nil {
		t.Logf("loaded %s — title %q", page.URL(), title)
	}

	// Best-effort consent/cookie dialog dismissal (EU "before you continue").
	// JS-click is fine here — the consent gate isn't what we're testing.
	dismissYouTubeConsent(page)

	// Wait for a <video> with real metadata (duration). If this never happens we
	// were almost certainly blocked (consent/bot wall) — surface a diagnosis.
	dur, err := waitForFloat(page, `(() => { const v = document.querySelector('video'); return v && isFinite(v.duration) ? v.duration : 0; })()`, 30*time.Second)
	if err != nil || dur <= 0 {
		t.Fatalf("no playable <video> with duration appeared (likely consent/bot wall): %v\n%s",
			err, ytDiagnose(page))
	}
	t.Logf("video ready: duration = %.1fs", dur)

	// Kick playback / dismiss any poster overlay and give the player keyboard
	// focus by clicking its center once.
	player := ytRect(t, page, ".html5-video-player, #movie_player, video")
	playerCX := player.X + player.Width/2
	playerCY := player.Y + player.Height/2
	_ = page.Click(short, playerCX, playerCY, 1)
	time.Sleep(800 * time.Millisecond)

	// ---- Strategy 1: rail-geometry seek to 60% ----
	t.Run("rail_geometry_seek", func(t *testing.T) {
		// Hover the player so the control bar (and progress bar) are shown.
		_ = page.Hover(short, playerCX, player.Y+player.Height-20)
		time.Sleep(400 * time.Millisecond)

		bar := ytRect(t, page, ".ytp-progress-bar-container, .ytp-progress-bar")
		const frac = 0.60
		tx := bar.X + frac*bar.Width
		ty := bar.Y + bar.Height/2
		before := mustFloat(t, page, "document.querySelector('video').currentTime")
		if err := page.Click(short, tx, ty, 1); err != nil {
			t.Fatalf("click progress bar: %v", err)
		}
		// Seeking buffers asynchronously; wait until currentTime jumps near target.
		want := frac * dur
		got, err := waitForFloat(page,
			fmt.Sprintf("document.querySelector('video').currentTime > %.1f ? document.querySelector('video').currentTime : 0", want-40),
			15*time.Second)
		if err != nil {
			t.Fatalf("currentTime did not reach ~%.0fs after rail seek (was %.1f): %v", want, before, err)
		}
		reportSeek(t, "rail-geometry @60%", before, got, want, seekTolSec)
	})

	// ---- Strategy 2: keyboard digit seek to 20% ----
	t.Run("keyboard_digit_seek", func(t *testing.T) {
		// Re-focus the player, then press "2" (YouTube: digit N -> N*10%).
		_ = page.Click(short, playerCX, playerCY, 1)
		time.Sleep(200 * time.Millisecond)
		before := mustFloat(t, page, "document.querySelector('video').currentTime")
		if err := page.KeyPress(short, "2"); err != nil {
			t.Fatalf("press digit 2: %v", err)
		}
		want := 0.20 * dur
		got, err := waitForFloat(page,
			fmt.Sprintf("Math.abs(document.querySelector('video').currentTime - %.1f) < %.1f ? document.querySelector('video').currentTime : -1", want, 0.12*dur),
			15*time.Second)
		if err != nil {
			t.Fatalf("currentTime did not reach ~%.0fs after digit seek (was %.1f): %v", want, before, err)
		}
		reportSeek(t, "keyboard digit-2 (20%)", before, got, want, seekTolSec)
	})

	// The grab-the-moving-scrubber case lives in its own test
	// (TestYouTubeDragMovingScrubber) on a short clip, where the scrubber visibly
	// travels in a couple of seconds — a 10-minute video's thumb barely moves at
	// 1x, and chaining it after these seeks can wedge the player buffering.
}

// TestYouTubeDragMovingScrubber grabs the *moving* YouTube scrubber and slides
// it to a target. It uses a short, fully-buffering clip so the scrubber clearly
// travels to a new position within a couple of seconds (and a fresh page avoids
// the buffering stall a far seek can leave behind). The flow: play → let it
// travel to a clearly different position → grab the dot at its LIVE position
// while still moving → slide to 75% → confirm the seek.
//
//	MIDAS_E2E_YOUTUBE=1 go test ./e2e -run TestYouTubeDragMovingScrubber -v
func TestYouTubeDragMovingScrubber(t *testing.T) {
	if os.Getenv("MIDAS_E2E_YOUTUBE") == "" {
		t.Skip("set MIDAS_E2E_YOUTUBE=1 to run the live YouTube drag test")
	}
	const seekTolSec = 3.0
	const minTravelPx = 60.0 // the scrubber must clearly move before we grab it

	page := newPage(t)
	short, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := page.SetViewportSize(short, 1280, 720, 1); err != nil {
		t.Fatalf("set viewport: %v", err)
	}

	const videoID = "jNQXAC9IVRw" // "Me at the zoo", ~19s — short enough to move fast.
	navCtx, cancelNav := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelNav()
	if _, err := page.Goto(navCtx, "https://www.youtube.com/watch?v="+videoID+"&hl=en"); err != nil {
		t.Fatalf("goto youtube: %v", err)
	}
	dismissYouTubeConsent(page)
	dur, err := waitForFloat(page, `(() => { const v = document.querySelector('video'); return v && isFinite(v.duration) ? v.duration : 0; })()`, 30*time.Second)
	if err != nil || dur <= 0 {
		t.Fatalf("no playable <video> with duration appeared: %v\n%s", err, ytDiagnose(page))
	}
	t.Logf("video ready: duration = %.1fs", dur)

	player := ytRect(t, page, ".html5-video-player, #movie_player, video")
	playerCX := player.X + player.Width/2
	playerCY := player.Y + player.Height/2

	// Make sure it's PLAYING (a click is a user gesture; we resume only while
	// paused, so a playing video is never toggled off).
	_ = page.Click(short, playerCX, playerCY, 1)
	mustEval(t, page, "(()=>{document.querySelector('video').muted=true;return true})()")
	_ = page.Hover(short, playerCX, player.Y+player.Height-24)

	bar := ytRect(t, page, ".ytp-progress-bar-container, .ytp-progress-bar")
	posBefore := scrubberStart(t, page, bar, dur).X
	tBefore := mustFloat(t, page, "document.querySelector('video').currentTime")

	// Let it run until the scrubber has clearly traveled to a new position.
	var posMoved, tMoved float64
	moved := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if evalBool(t, page, "document.querySelector('video').paused") {
			_ = page.Click(short, playerCX, playerCY, 1)
		}
		time.Sleep(500 * time.Millisecond)
		_ = page.Hover(short, playerCX, player.Y+player.Height-24)
		posMoved = scrubberStart(t, page, bar, dur).X
		tMoved = mustFloat(t, page, "document.querySelector('video').currentTime")
		if posMoved-posBefore > minTravelPx {
			moved = true
			break
		}
	}
	if !moved {
		t.Fatalf("scrubber did not visibly travel while playing (Δ=%.0fpx, %.1fs -> %.1fs)", posMoved-posBefore, tBefore, tMoved)
	}
	t.Logf("scrubber traveled %.0fpx (%.1fs -> %.1fs) while playing, before the grab", posMoved-posBefore, tBefore, tMoved)

	// Grab the dot at its LIVE (still-moving) position and slide it to 75%.
	grab := scrubberStart(t, page, bar, dur)
	const frac = 0.75
	toX := bar.X + frac*bar.Width
	toY := bar.Y + bar.Height/2
	if err := page.DragAndDrop(short, grab.X, grab.Y, toX, toY, 20); err != nil {
		t.Fatalf("drag scrubber: %v", err)
	}

	want := frac * dur
	got, err := waitForFloat(page,
		fmt.Sprintf("document.querySelector('video').currentTime > %.1f ? document.querySelector('video').currentTime : 0", want-3),
		10*time.Second)
	if err != nil {
		t.Fatalf("currentTime did not reach ~%.0fs after drag (was %.1f): %v", want, tMoved, err)
	}
	reportSeek(t, "grab moving scrubber -> 75%", tMoved, got, want, seekTolSec)
}

// scrubberStart returns the point to press for a drag: the live center of the
// .ytp-scrubber-button if it has size, else the current-time position computed
// on the rail (currentTime/duration), so the grab works even if the dot is tiny
// or momentarily unmeasurable.
func scrubberStart(t *testing.T, page *browser.Page, bar browser.ScreenshotClip, dur float64) browser.ScreenshotClip {
	t.Helper()
	expr := `(() => {
		const e = document.querySelector('.ytp-scrubber-button, .ytp-scrubber-container');
		if (e) { const r = e.getBoundingClientRect(); if (r.width > 0 && r.height > 0) return {x:r.x + r.width/2, y:r.y + r.height/2}; }
		return null;
	})()`
	var p struct{ X, Y float64 }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := page.Evaluate(ctx, expr, &p); err == nil && (p.X != 0 || p.Y != 0) {
		t.Logf("grab point: live scrubber button @ (%.0f, %.0f)", p.X, p.Y)
		return browser.ScreenshotClip{X: p.X, Y: p.Y}
	}
	cur := mustFloat(t, page, "document.querySelector('video').currentTime")
	frac := 0.0
	if dur > 0 {
		frac = cur / dur
	}
	x := bar.X + frac*bar.Width
	y := bar.Y + bar.Height/2
	t.Logf("grab point: computed from currentTime %.0fs -> (%.0f, %.0f)", cur, x, y)
	return browser.ScreenshotClip{X: x, Y: y}
}

// reportSeek logs a before/after/target line and fails if the landing is outside
// tol (generous, since YouTube snaps to keyframes and keeps playing).
func reportSeek(t *testing.T, label string, before, got, want, tol float64) {
	t.Helper()
	off := got - want
	if off < 0 {
		off = -off
	}
	if off > tol {
		t.Errorf("%s: currentTime %.1fs -> %.1fs, want ~%.1fs ± %.1fs (off %.1fs)", label, before, got, want, tol, off)
		return
	}
	t.Logf("%s: currentTime %.1fs -> %.1fs (target %.1fs ± %.1fs) ✓", label, before, got, want, tol)
}

// ytEnsurePlayJS forces the video into a playing state: it mutes, and *only when
// paused* calls play() and clicks the app-level .ytp-play-button (YouTube's
// controller re-pauses a bare element.play(), so the button click is what
// actually sticks). Guarding on v.paused means we never toggle a playing video
// off. Returns true if the video is now playing.
const ytEnsurePlayJS = "(()=>{const v=document.querySelector('video');if(!v)return false;v.muted=true;if(v.paused){v.play();const b=document.querySelector('.ytp-play-button');if(b)b.click();}return !v.paused;})()"

// dismissYouTubeConsent best-effort clicks a "Reject all"/"Accept all" button on
// the EU consent interstitial, retrying briefly while it renders.
func dismissYouTubeConsent(page *browser.Page) {
	expr := `(() => {
		const cand = [...document.querySelectorAll('button, [role=button], a')];
		const b = cand.find(el => {
			const t = (el.textContent || '').trim();
			const a = el.getAttribute('aria-label') || '';
			return /^(reject all|accept all|reject the use|i agree|agree to)/i.test(t) || /reject all|accept all/i.test(a);
		});
		if (b) { b.click(); return (b.textContent || b.getAttribute('aria-label') || '').trim().slice(0, 40); }
		return '';
	})()`
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var clicked string
		err := page.Evaluate(ctx, expr, &clicked)
		cancel()
		if err == nil && clicked != "" {
			return
		}
		// Stop early once a <video> exists (no consent gate in the way).
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		var hasVideo bool
		_ = page.Evaluate(ctx2, `!!document.querySelector('video')`, &hasVideo)
		cancel2()
		if hasVideo {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ---- small live-page helpers (non-fatal polling) ----

func waitForFloat(page *browser.Page, expr string, timeout time.Duration) (float64, error) {
	deadline := time.Now().Add(timeout)
	var last float64
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var out float64
		err := page.Evaluate(ctx, expr, &out)
		cancel()
		if err == nil && out > 0 {
			return out, nil
		}
		last = out
		time.Sleep(150 * time.Millisecond)
	}
	return last, fmt.Errorf("expression never became > 0 within %s", timeout)
}

func mustFloat(t *testing.T, page *browser.Page, expr string) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out float64
	if err := page.Evaluate(ctx, expr, &out); err != nil {
		t.Fatalf("evaluate %q: %v", expr, err)
	}
	return out
}

// ytRect resolves the bounding rect of the first matching selector via JS (so it
// works even when the element is normally hidden until hover).
func ytRect(t *testing.T, page *browser.Page, selectorList string) browser.ScreenshotClip {
	t.Helper()
	expr := fmt.Sprintf(`(() => {
		const sels = %q.split(',').map(s => s.trim());
		for (const s of sels) {
			const e = document.querySelector(s);
			if (e) { const r = e.getBoundingClientRect(); if (r.width > 0 && r.height > 0) return {x:r.x,y:r.y,w:r.width,h:r.height}; }
		}
		return null;
	})()`, selectorList)
	var r struct{ X, Y, W, H float64 }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := page.Evaluate(ctx, expr, &r); err != nil {
		t.Fatalf("rect for %q: %v", selectorList, err)
	}
	if r.W == 0 && r.H == 0 {
		t.Fatalf("no visible element for %q", selectorList)
	}
	return browser.ScreenshotClip{X: r.X, Y: r.Y, Width: r.W, Height: r.H, Scale: 1}
}

// ytDiagnose returns a short description of the current page for failure logs.
func ytDiagnose(page *browser.Page) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var s string
	_ = page.Evaluate(ctx, `(() => {
		const t = document.title || '';
		const h = (document.body ? document.body.innerText : '').slice(0, 300).replace(/\s+/g, ' ');
		const hasVideo = !!document.querySelector('video');
		const player = !!document.querySelector('.html5-video-player, #movie_player');
		return 'title=' + JSON.stringify(t) + ' hasVideo=' + hasVideo + ' player=' + player + ' bodyText=' + JSON.stringify(h);
	})()`, &s)
	return "  page: " + s
}
