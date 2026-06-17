package e2e

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/PolymuxOrg/midas/browser"
	"github.com/PolymuxOrg/midas/humanize"
)

// TestHumanizeSignal captures the *actual input signal* the humanize profile
// emits — as a detector's JavaScript would observe it — and checks that the
// single "on" profile stays inside human-plausible bounds while shedding the
// robotic jump-cut signature that "off" has.
//
// A real behavioural anti-bot scorer doesn't see our Config; it sees the DOM
// events. So we install capture listeners (mousemove/pointermove/key/mouse) and,
// for each of off/on, measure what reaches the page:
//   - typing: inter-key interval (mean + variance) and per-key dwell
//   - mouse: path point count + curvature (teleport vs traced)
//   - click: aim delay + press-hold
//
// The point: "off" is a robot (1-point teleport moves, ~0ms dwell, ~0ms hold,
// near-zero-variance keystrokes); the question is whether "on" keeps the *shape*
// (variance, dwell, curvature) rather than just shrinking the delays into a new,
// superhuman-but-still-uniform tell. The assertions below are the executable
// definition of "on is fast but still human-shaped".
//
//	go test ./e2e -run TestHumanizeSignal -v -count=1
func TestHumanizeSignal(t *testing.T) {
	page := newPage(t)
	if err := page.SetViewportSize(testCtx(t), 1100, 760, 1); err != nil {
		t.Fatalf("set viewport: %v", err)
	}
	gotoPath(t, page, "/form.html")
	installRecorder(t, page)

	const email = "casey.jordan@example.com"

	on := humanize.DefaultConfig()
	profiles := []struct {
		name string
		cfg  *humanize.Config
	}{
		{"off", nil},
		{"on", &on},
	}

	type profStats struct {
		ikiMean, ikiCV, dwellMean float64
		moveCount                 int
		moveCurve                 float64
		clickAim, clickHold       float64
		coalescedMax              int
	}
	stats := map[string]profStats{}

	for _, p := range profiles {
		page.EnableHumanize(p.cfg)

		// Seed the cursor somewhere known so the measured move has a real start.
		_ = page.Locator("#submit").Hover(testCtx(t))

		// --- MOVE: reset, then move across to a far element. ---
		recReset(t, page)
		if err := page.Locator("#username").Hover(testCtx(t)); err != nil {
			t.Fatalf("[%s] hover username: %v", p.name, err)
		}
		mv := recSnapshot(t, page)
		moveCount := len(mv.Moves)
		moveCurve := maxPerpDeviation(mv.Moves)
		coalMax := 0
		for _, pm := range mv.PMoves {
			if pm.C > coalMax {
				coalMax = pm.C
			}
		}

		// --- TYPE: clear field, reset, type the email. ---
		setEval(t, page, "document.getElementById('email').value=''")
		recReset(t, page)
		if err := page.Locator("#email").Type(testCtx(t), email, 0); err != nil {
			t.Fatalf("[%s] type email: %v", p.name, err)
		}
		ty := recSnapshot(t, page)
		ikiMean, ikiCV := interKeyStats(ty.Keydowns)
		dwellMean := dwellStats(ty.Keydowns, ty.Keyups)

		// --- CLICK: reset, click the button, read aim + hold. ---
		recReset(t, page)
		if err := page.Locator("#submit").Click(testCtx(t)); err != nil {
			t.Fatalf("[%s] click submit: %v", p.name, err)
		}
		ck := recSnapshot(t, page)
		aim, hold := clickStats(ck)

		stats[p.name] = profStats{
			ikiMean: ikiMean, ikiCV: ikiCV, dwellMean: dwellMean,
			moveCount: moveCount, moveCurve: moveCurve,
			clickAim: aim, clickHold: hold, coalescedMax: coalMax,
		}
		t.Logf("%-8s | type: IKI %.0fms (cv %.2f) dwell %.0fms | move: %d pts curve %.1fpx | click: aim %.0fms hold %.0fms | coalesced≤%d",
			p.name, ikiMean, ikiCV, dwellMean, moveCount, moveCurve, aim, hold, coalMax)
	}

	// Reference bands for genuine human input (rough, from HCI literature):
	//   inter-key interval: ~120–280ms typical; a fast typist floors ~90–100ms.
	//     Sustained <60ms mean is superhuman. Variance matters as much as the
	//     mean — a human stream has coefficient-of-variation well above ~0.2.
	//   key dwell (down→up): ~50–130ms; <25ms reads as synthetic.
	//   click hold (down→up): ~60–150ms; <30ms reads as synthetic.
	//   a real pointer move is many points along a curve, not a 1-point jump.
	f := stats["on"]
	off := stats["off"]

	// The single profile must stay human-shaped on every axis a behavioural
	// scorer reads.
	if f.ikiMean < 60 || f.ikiMean > 250 {
		t.Errorf("on IKI mean %.0fms outside human band [60,250] — too fast/slow", f.ikiMean)
	}
	if f.ikiCV < 0.15 {
		t.Errorf("on IKI variance too low (cv %.2f < 0.15) — uniform timing is itself a tell", f.ikiCV)
	}
	if f.dwellMean < 40 {
		t.Errorf("on key dwell %.0fms < 40ms — below the human band, visible to dwell-tracking detectors", f.dwellMean)
	}
	if f.clickHold < 40 {
		t.Errorf("on click hold %.0fms < 40ms — zero-hold click is a robotic signature", f.clickHold)
	}
	if f.moveCount < 5 {
		t.Errorf("on move emitted %d points — a human move is many points, not a teleport", f.moveCount)
	}
	if f.moveCurve < 1.5 {
		t.Errorf("on move curvature %.1fpx < 1.5 — a perfectly straight path is a tell", f.moveCurve)
	}

	// Sanity: "off" really is the robot we're contrasting against, so the test
	// is measuring a real difference and not a no-op.
	if off.moveCount > 2 {
		t.Errorf("expected off to teleport (≤2 move pts), got %d", off.moveCount)
	}
	if off.dwellMean >= f.dwellMean {
		t.Errorf("expected off dwell (%.0fms) << fast dwell (%.0fms)", off.dwellMean, f.dwellMean)
	}

	t.Logf("verdict: on keeps human shape (IKI %.0fms cv %.2f, dwell %.0fms, hold %.0fms, %d-pt curved move) while off is a teleport/zero-dwell robot",
		f.ikiMean, f.ikiCV, f.dwellMean, f.clickHold, f.moveCount)
}

// ---- recorder plumbing -----------------------------------------------------

type evPoint struct {
	T float64 `json:"t"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	C int     `json:"c"`
}
type evKey struct {
	T float64 `json:"t"`
	K string  `json:"k"`
}
type recData struct {
	Moves    []evPoint `json:"moves"`
	PMoves   []evPoint `json:"pmoves"`
	Keydowns []evKey   `json:"keydowns"`
	Keyups   []evKey   `json:"keyups"`
	Downs    []evPoint `json:"downs"`
	Ups      []evPoint `json:"ups"`
}

// installRecorder attaches capturing listeners that record every input event
// the page receives, with high-res timestamps — i.e. exactly what a detector's
// own listeners would see.
func installRecorder(t *testing.T, page *browser.Page) {
	t.Helper()
	const script = `(() => {
  window.__rec = { moves: [], pmoves: [], keydowns: [], keyups: [], downs: [], ups: [] };
  window.__recReset = () => { for (const k in window.__rec) if (Array.isArray(window.__rec[k])) window.__rec[k].length = 0; };
  const add = (type, arr, fn) => document.addEventListener(type, e => window.__rec[arr].push(fn(e)), true);
  add('mousemove', 'moves', e => ({ t: e.timeStamp, x: e.clientX, y: e.clientY }));
  add('pointermove', 'pmoves', e => { let c = 0; try { c = e.getCoalescedEvents ? e.getCoalescedEvents().length : 0; } catch (_) {} return { t: e.timeStamp, x: e.clientX, y: e.clientY, c }; });
  add('keydown', 'keydowns', e => ({ t: e.timeStamp, k: e.key }));
  add('keyup', 'keyups', e => ({ t: e.timeStamp, k: e.key }));
  add('mousedown', 'downs', e => ({ t: e.timeStamp, x: e.clientX, y: e.clientY }));
  add('mouseup', 'ups', e => ({ t: e.timeStamp, x: e.clientX, y: e.clientY }));
  return true;
})()`
	var ok bool
	if err := page.Evaluate(testCtx(t), script, &ok); err != nil {
		t.Fatalf("install recorder: %v", err)
	}
}

func recReset(t *testing.T, page *browser.Page) {
	t.Helper()
	var ignored any
	if err := page.Evaluate(testCtx(t), "window.__recReset()", &ignored); err != nil {
		t.Fatalf("reset recorder: %v", err)
	}
}

func setEval(t *testing.T, page *browser.Page, expr string) {
	t.Helper()
	var ignored any
	if err := page.Evaluate(testCtx(t), "(()=>{"+expr+";return true})()", &ignored); err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
}

func recSnapshot(t *testing.T, page *browser.Page) recData {
	t.Helper()
	raw := evalString(t, page, "JSON.stringify(window.__rec)")
	var d recData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("decode recorder snapshot: %v", err)
	}
	return d
}

// ---- statistics ------------------------------------------------------------

// interKeyStats returns the mean and coefficient-of-variation of the interval
// between consecutive (non-modifier) keydowns — the classic typing-cadence
// signal. CV (stdev/mean) captures whether the stream has human variance.
func interKeyStats(keys []evKey) (mean, cv float64) {
	var ts []float64
	for _, k := range keys {
		if k.K == "Shift" || k.K == "Control" || k.K == "Alt" || k.K == "Meta" {
			continue
		}
		ts = append(ts, k.T)
	}
	if len(ts) < 2 {
		return 0, 0
	}
	var deltas []float64
	for i := 1; i < len(ts); i++ {
		deltas = append(deltas, ts[i]-ts[i-1])
	}
	mean = meanOf(deltas)
	cv = 0
	if mean > 0 {
		cv = stdevOf(deltas, mean) / mean
	}
	return mean, cv
}

// dwellStats pairs each printable keydown with the next keyup of the same key
// and returns the mean hold time.
func dwellStats(downs, ups []evKey) float64 {
	var holds []float64
	used := make([]bool, len(ups))
	for _, d := range downs {
		if len(d.K) != 1 {
			continue
		}
		for j, u := range ups {
			if used[j] || u.K != d.K || u.T < d.T {
				continue
			}
			holds = append(holds, u.T-d.T)
			used[j] = true
			break
		}
	}
	if len(holds) == 0 {
		return 0
	}
	return meanOf(holds)
}

// clickStats returns the aim delay (last move before press → press) and the
// press-hold (press → release) for the first click in the snapshot.
func clickStats(d recData) (aim, hold float64) {
	if len(d.Downs) == 0 || len(d.Ups) == 0 {
		return 0, 0
	}
	down := d.Downs[0].T
	hold = d.Ups[0].T - down
	aim = 0
	for _, m := range d.Moves {
		if m.T <= down {
			aim = down - m.T // distance from the *last* pre-press move
		}
	}
	return aim, hold
}

// maxPerpDeviation measures how far the move path bows off the straight line
// between its first and last recorded point — 0 for a teleport/straight drag,
// >0 for a traced curve.
func maxPerpDeviation(pts []evPoint) float64 {
	if len(pts) < 3 {
		return 0
	}
	a, b := pts[0], pts[len(pts)-1]
	dx, dy := b.X-a.X, b.Y-a.Y
	length := math.Hypot(dx, dy)
	if length == 0 {
		return 0
	}
	var maxd float64
	for _, p := range pts[1 : len(pts)-1] {
		// Perpendicular distance from p to line a→b.
		d := math.Abs(dy*p.X-dx*p.Y+b.X*a.Y-b.Y*a.X) / length
		if d > maxd {
			maxd = d
		}
	}
	return maxd
}

func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stdevOf(xs []float64, mean float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var s float64
	for _, x := range xs {
		d := x - mean
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}
