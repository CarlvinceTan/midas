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

// Preset names a built-in tuning profile.
type Preset string

const (
	PresetDefault Preset = "default"
	PresetCareful Preset = "careful"
)

// Config is the full set of tunable parameters. Construct via DefaultConfig
// or CarefulConfig and then mutate fields if you need a custom profile.
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

// DefaultConfig returns the "default" preset — normal human speeds.
func DefaultConfig() Config {
	return Config{
		TypingDelay:       70,
		TypingDelaySpread: 40,
		TypingPauseChance: 0.1,
		TypingPauseRange:  Range{400, 1000},
		ShiftDownDelay:    Range{30, 70},
		ShiftUpDelay:      Range{20, 50},
		KeyHold:           Range{15, 35},
		FieldSwitchDelay:  Range{800, 1500},

		MouseStepsDivisor:    8,
		MouseMinSteps:        25,
		MouseMaxSteps:        80,
		MouseWobbleMax:       1.5,
		MouseOvershootChance: 0.15,
		MouseOvershootPx:     Range{3, 6},
		MouseBurstSize:       Range{3, 5},
		MouseBurstPause:      Range{8, 18},

		ClickAimDelayInput:  Range{60, 140},
		ClickAimDelayButton: Range{80, 200},
		ClickHoldInput:      Range{40, 100},
		ClickHoldButton:     Range{60, 150},
		ClickInputXRange:    Range{0.05, 0.30},

		IdleDriftPx:    3,
		IdlePauseRange: Range{300, 1000},

		ScrollDeltaBase:       Range{80, 130},
		ScrollDeltaVariance:   0.2,
		ScrollPauseFast:       Range{60, 150},
		ScrollPauseSlow:       Range{150, 400},
		ScrollAccelSteps:      Range{2, 3},
		ScrollDecelSteps:      Range{2, 3},
		ScrollOvershootChance: 0.1,
		ScrollOvershootPx:     Range{50, 150},
		ScrollSettleDelay:     Range{300, 600},
		ScrollTargetZone:      Range{0.20, 0.80},
		ScrollPreMoveDelay:    Range{100, 300},

		InitialCursorX: Range{400, 700},
		InitialCursorY: Range{45, 60},

		PatchCoalesced: true,
	}
}

// CarefulConfig returns the "careful" preset — slower, more deliberate.
func CarefulConfig() Config {
	cfg := DefaultConfig()
	cfg.TypingDelay = 100
	cfg.TypingDelaySpread = 50
	cfg.TypingPauseChance = 0.15
	cfg.TypingPauseRange = Range{500, 1200}
	cfg.ShiftDownDelay = Range{40, 90}
	cfg.ShiftUpDelay = Range{30, 70}
	cfg.KeyHold = Range{20, 45}
	cfg.FieldSwitchDelay = Range{1000, 2000}
	cfg.MouseOvershootChance = 0.10
	cfg.MouseBurstPause = Range{12, 25}
	cfg.ClickAimDelayInput = Range{80, 180}
	cfg.ClickAimDelayButton = Range{120, 280}
	cfg.ClickHoldInput = Range{60, 140}
	cfg.ClickHoldButton = Range{80, 200}
	cfg.ScrollPauseFast = Range{100, 200}
	cfg.ScrollPauseSlow = Range{250, 600}
	cfg.ScrollSettleDelay = Range{400, 800}
	cfg.ScrollPreMoveDelay = Range{150, 400}
	return cfg
}

// ResolveConfig returns the named preset, falling back to default.
func ResolveConfig(preset Preset) Config {
	switch preset {
	case PresetCareful:
		return CarefulConfig()
	default:
		return DefaultConfig()
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
