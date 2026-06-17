// Package humanize injects realistic timing, trajectory, and cadence into
// otherwise jump-cut CDP input events. Algorithms are a faithful port of the
// cloakbrowser-human Python package — Bezier mouse curves, per-character
// keyboard pacing, and accel/cruise/decel mouse-wheel scrolling — adapted to
// Go interfaces so the same wiring can sit on top of any CDP client.
package humanize

import (
	"context"
	"math/rand/v2"
	"time"
)

// Range is a closed interval [min, max] used for jittered durations and
// magnitudes throughout the package.
type Range [2]float64

// Config is the full set of tunable parameters. Construct via DefaultConfig
// and then mutate fields if you need a custom profile.
type Config struct {
	// Keyboard timing.
	TypingDelay       float64
	TypingDelaySpread float64
	TypingPauseChance float64
	TypingPauseRange  Range
	ShiftDownDelay    Range
	ShiftUpDelay      Range
	KeyHold           Range
	FieldSwitchDelay  Range

	// Mouse trajectory.
	MouseStepsDivisor    float64
	MouseMinSteps        int
	MouseMaxSteps        int
	MouseWobbleMax       float64
	MouseOvershootChance float64
	MouseOvershootPx     Range
	MouseBurstSize       Range
	MouseBurstPause      Range

	// Mouse click cadence.
	ClickAimDelayInput  Range
	ClickAimDelayButton Range
	ClickHoldInput      Range
	ClickHoldButton     Range
	ClickInputXRange    Range

	// Mouse idle drift.
	IdleDriftPx    float64
	IdlePauseRange Range

	// Scroll cadence.
	ScrollDeltaBase       Range
	ScrollDeltaVariance   float64
	ScrollPauseFast       Range
	ScrollPauseSlow       Range
	ScrollAccelSteps      Range
	ScrollDecelSteps      Range
	ScrollOvershootChance float64
	ScrollOvershootPx     Range
	ScrollSettleDelay     Range
	ScrollTargetZone      Range
	ScrollPreMoveDelay    Range

	// Where to seed the cursor on first humanized action — biased toward the
	// address-bar region so the first move feels like it began at the URL.
	InitialCursorX Range
	InitialCursorY Range

	// PatchCoalesced enables PointerEvent.getCoalescedEvents synthesis.
	PatchCoalesced bool
}

// DefaultConfig returns the one humanize profile: as fast as possible while
// staying behaviourally indistinguishable from a real (fast) human. There is a
// single profile by design — humanize is either on (this) or off.
//
// Tuning rule: minimise latency, but never below a human floor on any signal a
// behavioural scorer reads. Concretely —
//   - typing lands ~100ms keydown→keydown (a fast typist) with real variance and
//     the occasional pause, never a flat superhuman stream;
//   - key dwell (down→up) sits in the human 45–90ms band, so dwell-tracking
//     detectors see a genuine hold rather than a near-instant tap;
//   - clicks keep a human-range press-hold (a sub-30ms down→up is a robot tell);
//   - the mouse traces the fewest steps that still form a curved, wobbled,
//     occasionally-overshooting path — never a single teleport segment;
//   - deliberation sleeps that don't aid realism (field-switch delay, frequent
//     thinking pauses, long settle/burst pauses) are cut hard.
//
// "Too fast / too uniform" is as detectable as "too instant": the scorer keys on
// the *shape* of the timing distribution, not merely whether a delay is non-zero.
func DefaultConfig() Config {
	return Config{
		// Typing: ~100ms keydown→keydown (KeyHold + inter-char), human dwell,
		// real variance, a rare short pause.
		TypingDelay:       35,
		TypingDelaySpread: 25,
		TypingPauseChance: 0.05,
		TypingPauseRange:  Range{250, 600},
		ShiftDownDelay:    Range{30, 70},
		ShiftUpDelay:      Range{20, 50},
		KeyHold:           Range{45, 90}, // dwell in the human band (was 15–35)
		FieldSwitchDelay:  Range{150, 400},

		// Mouse: few steps but a genuinely curved, wobbled, occasionally
		// overshooting path.
		MouseStepsDivisor:    14,
		MouseMinSteps:        10,
		MouseMaxSteps:        40,
		MouseWobbleMax:       1.5,
		MouseOvershootChance: 0.15,
		MouseOvershootPx:     Range{3, 6},
		MouseBurstSize:       Range{3, 5},
		MouseBurstPause:      Range{4, 10},

		// Clicks: short aim, human-range press-hold.
		ClickAimDelayInput:  Range{30, 80},
		ClickAimDelayButton: Range{40, 100},
		ClickHoldInput:      Range{50, 100},
		ClickHoldButton:     Range{60, 120},
		ClickInputXRange:    Range{0.05, 0.30},

		IdleDriftPx:    3,
		IdlePauseRange: Range{300, 1000},

		ScrollDeltaBase:       Range{80, 130},
		ScrollDeltaVariance:   0.2,
		ScrollPauseFast:       Range{40, 90},
		ScrollPauseSlow:       Range{100, 250},
		ScrollAccelSteps:      Range{2, 3},
		ScrollDecelSteps:      Range{2, 3},
		ScrollOvershootChance: 0.1,
		ScrollOvershootPx:     Range{50, 150},
		ScrollSettleDelay:     Range{150, 350},
		ScrollTargetZone:      Range{0.20, 0.80},
		ScrollPreMoveDelay:    Range{60, 150},

		InitialCursorX: Range{400, 700},
		InitialCursorY: Range{45, 60},

		PatchCoalesced: true,
	}
}

// Rand returns a uniform random float64 in [lo, hi). When hi <= lo, returns lo.
func Rand(lo, hi float64) float64 {
	if hi <= lo {
		return lo
	}
	return lo + rand.Float64()*(hi-lo)
}

// RandRange is Rand with the bounds packed into a Range.
func RandRange(r Range) float64 {
	return Rand(r[0], r[1])
}

// RandIntRange returns a uniform random int in the inclusive interval r.
func RandIntRange(r Range) int {
	lo := int(r[0])
	hi := int(r[1])
	if hi <= lo {
		return lo
	}
	return lo + rand.IntN(hi-lo+1)
}

// SleepMs sleeps for ms milliseconds, honoring ctx cancellation. Non-positive
// durations return immediately.
func SleepMs(ctx context.Context, ms float64) error {
	if ms <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(ms * float64(time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
