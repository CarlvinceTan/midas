package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

// TestRecordSliderDrag emits a frame sequence of midas grabbing the moving
// playhead and sliding it to a target, for assembly into a video with ffmpeg.
// It drives a *slow, held-button* drag by hand (mousePressed → many mouseMoved →
// mouseReleased via SendCDP), screenshotting between steps so the slide is
// visible rather than instantaneous.
//
//	MIDAS_E2E_VIDEO=1 MIDAS_E2E_VIDEO_DIR=/tmp/midas-frames \
//	  go test ./e2e -run TestRecordSliderDrag -v -count=1
func TestRecordSliderDrag(t *testing.T) {
	if os.Getenv("MIDAS_E2E_VIDEO") == "" {
		t.Skip("set MIDAS_E2E_VIDEO=1 to record the slider drag frames")
	}
	dir := os.Getenv("MIDAS_E2E_VIDEO_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "midas-frames")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir frames: %v", err)
	}

	page := newPage(t)
	short, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := page.SetViewportSize(short, 900, 560, 1); err != nil {
		t.Fatalf("viewport: %v", err)
	}
	gotoPath(t, page, "/slider.html")

	rail := mustBox(t, page, "#video-rail")
	railY := rail.Y + rail.Height/2

	idx := 0
	frame := func() {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		data, err := page.Screenshot(ctx, browser.ScreenshotOptions{Format: "png"})
		if err != nil {
			t.Fatalf("screenshot frame %d: %v", idx, err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("frame_%04d.png", idx)), data, 0o644); err != nil {
			t.Fatalf("write frame %d: %v", idx, err)
		}
		idx++
	}
	mouse := func(typ string, x, y float64, extra map[string]any) {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		params := map[string]any{"type": typ, "x": x, "y": y}
		for k, v := range extra {
			params[k] = v
		}
		if err := page.SendCDP(ctx, "Input.dispatchMouseEvent", params, nil); err != nil {
			t.Fatalf("mouse %s: %v", typ, err)
		}
	}

	thumbCenterX := func() float64 {
		return evalFloat(t, page, "(()=>{const r=document.getElementById('video-thumb').getBoundingClientRect();return r.left+r.width/2;})()")
	}

	// Phase 1 — the timeline is PLAYING. Let it run for a few seconds so the thumb
	// travels to a genuinely new position before we touch it. We never pause.
	posBefore := thumbCenterX()
	for i := 0; i < 26; i++ { // ~3s of real playback
		frame()
		time.Sleep(110 * time.Millisecond)
	}

	// Phase 2 — while the thumb is STILL MOVING, grab it wherever it currently is.
	// We read its live position at this instant and press exactly there, so the
	// slide continues smoothly from the live spot (no jump = grabbed the live
	// thumb, not a fixed point).
	grabX := thumbCenterX()
	grabTime := evalFloat(t, page, "window.player.currentTime")
	startY := railY
	targetX := rail.X + 0.80*rail.Width
	t.Logf("playhead moved %.0fpx (%.1fs) on its own, then grabbed live at %.0fpx / %.1fs",
		grabX-posBefore, grabTime, grabX, grabTime)

	mouse("mouseMoved", grabX, startY, nil)
	mouse("mousePressed", grabX, startY, map[string]any{"button": "left", "buttons": 1, "clickCount": 1})
	frame()

	const steps = 32
	for i := 1; i <= steps; i++ {
		tt := float64(i) / float64(steps)
		x := grabX + (targetX-grabX)*tt
		mouse("mouseMoved", x, startY, map[string]any{"button": "left", "buttons": 1})
		frame()
		time.Sleep(45 * time.Millisecond)
	}
	mouse("mouseReleased", targetX, startY, map[string]any{"button": "left", "buttons": 0, "clickCount": 1})
	landed := evalFloat(t, page, "window.player.currentTime")

	// Phase 3 — it keeps playing from the drop point, proving it was a live
	// playhead the whole time.
	for i := 0; i < 12; i++ {
		frame()
		time.Sleep(90 * time.Millisecond)
	}

	t.Logf("wrote %d frames to %s; grabbed mid-motion, landed at %.1fs on release (target %.1fs)", idx, dir, landed, 0.80*120)
}

// TestRecordYouTubeDrag records a slow grab-and-slide of the REAL YouTube
// scrubber into a frame sequence. Network + live YouTube.
//
//	MIDAS_E2E_VIDEO=1 MIDAS_E2E_YOUTUBE=1 MIDAS_E2E_VIDEO_DIR=/tmp/yt-frames \
//	  go test ./e2e -run TestRecordYouTubeDrag -v -count=1
func TestRecordYouTubeDrag(t *testing.T) {
	if os.Getenv("MIDAS_E2E_VIDEO") == "" || os.Getenv("MIDAS_E2E_YOUTUBE") == "" {
		t.Skip("set MIDAS_E2E_VIDEO=1 and MIDAS_E2E_YOUTUBE=1 to record the YouTube drag")
	}
	dir := os.Getenv("MIDAS_E2E_VIDEO_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "yt-frames")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir frames: %v", err)
	}

	page := newPage(t)
	short, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := page.SetViewportSize(short, 1280, 720, 1); err != nil {
		t.Fatalf("viewport: %v", err)
	}

	// A short clip ("Me at the zoo", ~19s): at natural 1x speed the scrubber races
	// across the bar in a couple of seconds, so its motion is unmistakable — and
	// 1x playback never stalls the way a forced high playbackRate does.
	const videoID = "jNQXAC9IVRw"
	navCtx, cancelNav := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelNav()
	if _, err := page.Goto(navCtx, "https://www.youtube.com/watch?v="+videoID+"&hl=en"); err != nil {
		t.Fatalf("goto youtube: %v", err)
	}
	dismissYouTubeConsent(page)
	dur, err := waitForFloat(page, `(() => { const v = document.querySelector('video'); return v && isFinite(v.duration) ? v.duration : 0; })()`, 30*time.Second)
	if err != nil || dur <= 0 {
		t.Fatalf("no playable video: %v", err)
	}

	player := ytRect(t, page, ".html5-video-player, #movie_player, video")
	playerCX := player.X + player.Width/2
	playerCY := player.Y + player.Height/2

	idx := 0
	frame := func() {
		ctx, c := context.WithTimeout(context.Background(), 6*time.Second)
		defer c()
		data, err := page.Screenshot(ctx, browser.ScreenshotOptions{Format: "png"})
		if err != nil {
			t.Fatalf("screenshot %d: %v", idx, err)
		}
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("frame_%04d.png", idx)), data, 0o644)
		idx++
	}
	mouse := func(typ string, x, y float64, extra map[string]any) {
		ctx, c := context.WithTimeout(context.Background(), 6*time.Second)
		defer c()
		params := map[string]any{"type": typ, "x": x, "y": y}
		for k, v := range extra {
			params[k] = v
		}
		if err := page.SendCDP(ctx, "Input.dispatchMouseEvent", params, nil); err != nil {
			t.Fatalf("mouse %s: %v", typ, err)
		}
	}

	// Make sure the clip is actually PLAYING before we record: a user-gesture click
	// to satisfy autoplay, then retry YouTube's playVideo() until the playhead
	// advances, so we never record a stalled/paused player.
	_ = page.Click(short, playerCX, playerCY, 1)
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		mustEval(t, page, ytEnsurePlayJS)
		t0 := mustFloat(t, page, "document.querySelector('video').currentTime")
		time.Sleep(500 * time.Millisecond)
		if mustFloat(t, page, "document.querySelector('video').currentTime") > t0 {
			break
		}
	}

	// Reveal controls and keep them up.
	_ = page.Hover(short, playerCX, player.Y+player.Height-24)
	time.Sleep(400 * time.Millisecond)

	// Phase 1 — the video is PLAYING. Let it run a couple of seconds so the
	// scrubber travels to a genuinely new position before we touch it. Never pause.
	bar0 := ytRect(t, page, ".ytp-progress-bar-container, .ytp-progress-bar")
	posBefore := scrubberStart(t, page, bar0, dur).X
	tBefore := mustFloat(t, page, "document.querySelector('video').currentTime")
	for i := 0; i < 20; i++ { // ~2.5s of real time
		_ = page.Hover(short, playerCX, player.Y+player.Height-24)
		frame()
		time.Sleep(120 * time.Millisecond)
	}

	// Phase 2 — while it is STILL PLAYING/MOVING, grab the scrubber wherever it
	// currently is and slide it to 70%.
	bar := ytRect(t, page, ".ytp-progress-bar-container, .ytp-progress-bar")
	grab := scrubberStart(t, page, bar, dur)
	tGrab := mustFloat(t, page, "document.querySelector('video').currentTime")
	startY := bar.Y + bar.Height/2
	targetX := bar.X + 0.70*bar.Width
	t.Logf("scrubber moved %.0fpx (%.1fs->%.1fs) while playing, then grabbed live at %.0fpx",
		grab.X-posBefore, tBefore, tGrab, grab.X)

	mouse("mouseMoved", grab.X, startY, nil)
	mouse("mousePressed", grab.X, startY, map[string]any{"button": "left", "buttons": 1, "clickCount": 1})
	frame()
	const steps = 30
	for i := 1; i <= steps; i++ {
		tt := float64(i) / float64(steps)
		x := grab.X + (targetX-grab.X)*tt
		mouse("mouseMoved", x, startY, map[string]any{"button": "left", "buttons": 1})
		frame()
		time.Sleep(50 * time.Millisecond)
	}
	mouse("mouseReleased", targetX, startY, map[string]any{"button": "left", "buttons": 0, "clickCount": 1})
	time.Sleep(250 * time.Millisecond)
	landed := mustFloat(t, page, "document.querySelector('video').currentTime")

	// A few trailing frames: it resumes playing from the drop point.
	for i := 0; i < 8; i++ {
		frame()
		time.Sleep(90 * time.Millisecond)
	}
	t.Logf("wrote %d frames to %s; grabbed mid-motion, released at 70%% -> seeked to %.1fs of %.1fs (%.0f%%)",
		idx, dir, landed, dur, 100*landed/dur)
}

// mustEval runs a JS expression for its side effect.
func mustEval(t *testing.T, page *browser.Page, expr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ignored any
	if err := page.Evaluate(ctx, expr, &ignored); err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
}
