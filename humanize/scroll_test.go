package humanize

import (
	"context"
	"math"
	"strings"
	"testing"
)

func fastScrollConfig() Config {
	cfg := DefaultConfig()
	// Eliminate per-step sleeps so the test runs instantly.
	cfg.ScrollPauseFast = Range{0, 0}
	cfg.ScrollPauseSlow = Range{0, 0}
	cfg.ScrollSettleDelay = Range{0, 0}
	cfg.ScrollOvershootChance = 0 // deterministic — no overshoot tail
	return cfg
}

func TestScrollDownEmitsPositiveDeltas(t *testing.T) {
	fake := &fakeMouse{}
	cfg := fastScrollConfig()
	signed, err := Scroll(context.Background(), fake, 600, cfg)
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
	if len(fake.wheels) < 3 {
		t.Errorf("Scroll emitted %d wheel events; want >= 3", len(fake.wheels))
	}
	for i, w := range fake.wheels {
		if w.Y <= 0 {
			t.Errorf("wheel[%d].Y = %v; want positive (scroll down)", i, w.Y)
		}
		if w.X != 0 {
			t.Errorf("wheel[%d].X = %v; want 0 (vertical only)", i, w.X)
		}
	}
	if signed <= 0 {
		t.Errorf("signed total = %v; want positive", signed)
	}
}

func TestScrollUpEmitsNegativeDeltas(t *testing.T) {
	fake := &fakeMouse{}
	signed, err := Scroll(context.Background(), fake, -500, fastScrollConfig())
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
	for i, w := range fake.wheels {
		if w.Y >= 0 {
			t.Errorf("wheel[%d].Y = %v; want negative (scroll up)", i, w.Y)
		}
	}
	if signed >= 0 {
		t.Errorf("signed total = %v; want negative", signed)
	}
}

func TestScrollSkipsTrivialDelta(t *testing.T) {
	fake := &fakeMouse{}
	if signed, err := Scroll(context.Background(), fake, 0.4, DefaultConfig()); err != nil || signed != 0 {
		t.Errorf("Scroll(0.4) = (%v, %v); want (0, nil)", signed, err)
	}
	if len(fake.wheels) != 0 {
		t.Errorf("Scroll(0.4) emitted %d wheels; want 0", len(fake.wheels))
	}
}

func TestScrollMagnitudeApproximatesTarget(t *testing.T) {
	fake := &fakeMouse{}
	const target = 1200.0
	signed, err := Scroll(context.Background(), fake, target, fastScrollConfig())
	if err != nil {
		t.Fatalf("Scroll returned error: %v", err)
	}
	// Accel and decel phases use smaller deltas than cruise, so the actual
	// total typically lands ~10–20% short of the naive avgDelta × clicks
	// estimate. Loose bound here: just verify the order of magnitude.
	mag := math.Abs(signed)
	if mag < target*0.80 || mag > target*1.3 {
		t.Errorf("signed total %v not within ~target=%v", signed, target)
	}
}

func TestPointerCoalescedScriptIsIdempotent(t *testing.T) {
	got := PointerCoalescedScript()
	if !strings.Contains(got, "__cbHumanCoalesced") {
		t.Errorf("PointerCoalescedScript missing idempotency guard")
	}
	if !strings.Contains(got, "getCoalescedEvents") {
		t.Errorf("PointerCoalescedScript missing target API name")
	}
}
