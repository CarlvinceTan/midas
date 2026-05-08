package humanize

import (
	"context"
	"math"
	"math/rand/v2"
)

// Scroll dispatches a humanized sequence of mouseWheel events totaling
// approximately |targetDeltaY| pixels in the appropriate direction. The
// sequence has three phases — acceleration (small, slow deltas), cruise (full
// deltas, fast cadence), deceleration (small, slow deltas) — with optional
// overshoot+correction at the end, mirroring how a real wheel-spin settles.
//
// Returns the signed total scrolled (sum of dispatched deltas). The caller
// is responsible for any per-step element re-checks: keep this primitive
// purely about the wheel cadence so it can be reused for both viewport and
// in-element scrolls.
func Scroll(ctx context.Context, raw RawMouse, targetDeltaY float64, cfg Config) (float64, error) {
	if math.Abs(targetDeltaY) < 1 {
		return 0, nil
	}
	direction := 1.0
	if targetDeltaY < 0 {
		direction = -1
	}
	absDist := math.Abs(targetDeltaY)
	avgDelta := (cfg.ScrollDeltaBase[0] + cfg.ScrollDeltaBase[1]) / 2
	if avgDelta <= 0 {
		avgDelta = 100
	}
	totalClicks := int(math.Ceil(absDist / avgDelta))
	if totalClicks < 3 {
		totalClicks = 3
	}
	accelSteps := RandIntRange(cfg.ScrollAccelSteps)
	decelSteps := RandIntRange(cfg.ScrollDecelSteps)

	var scrolledMag float64
	var signedScrolled float64
	for i := 0; i < totalClicks; i++ {
		if err := ctx.Err(); err != nil {
			return signedScrolled, err
		}
		var delta, pause float64
		switch {
		case i < accelSteps:
			delta = Rand(80, 100)
			pause = RandRange(cfg.ScrollPauseSlow)
		case i >= totalClicks-decelSteps:
			delta = Rand(60, 90)
			pause = RandRange(cfg.ScrollPauseSlow)
		default:
			delta = RandRange(cfg.ScrollDeltaBase)
			pause = RandRange(cfg.ScrollPauseFast)
		}
		delta *= 1 + (rand.Float64()-0.5)*2*cfg.ScrollDeltaVariance
		stepDelta := math.Round(delta) * direction

		if err := raw.Wheel(ctx, 0, stepDelta); err != nil {
			return signedScrolled, err
		}
		scrolledMag += math.Abs(stepDelta)
		signedScrolled += stepDelta
		if err := SleepMs(ctx, pause); err != nil {
			return signedScrolled, err
		}
		if scrolledMag >= absDist*1.1 {
			break
		}
	}

	if rand.Float64() < cfg.ScrollOvershootChance {
		overshoot := math.Round(RandRange(cfg.ScrollOvershootPx)) * direction
		if err := raw.Wheel(ctx, 0, overshoot); err != nil {
			return signedScrolled, err
		}
		signedScrolled += overshoot
		if err := SleepMs(ctx, RandRange(cfg.ScrollSettleDelay)); err != nil {
			return signedScrolled, err
		}
		corrections := RandIntRange(Range{1, 2})
		for i := 0; i < corrections; i++ {
			corrDelta := math.Round(Rand(40, 80)) * -direction
			if err := raw.Wheel(ctx, 0, corrDelta); err != nil {
				return signedScrolled, err
			}
			signedScrolled += corrDelta
			if err := SleepMs(ctx, Rand(100, 250)); err != nil {
				return signedScrolled, err
			}
		}
	}

	if err := SleepMs(ctx, RandRange(cfg.ScrollSettleDelay)); err != nil {
		return signedScrolled, err
	}
	return signedScrolled, nil
}
