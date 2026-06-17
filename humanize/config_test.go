package humanize

import (
	"context"
	"testing"
	"time"
)

// TestDefaultConfigHumanFloors pins the human-floor guarantees of the single
// profile: dwell and click-hold must stay in the human band so dwell-tracking
// detectors see a genuine hold, and the mouse must trace multiple steps rather
// than teleport. These are the constraints that keep "as fast as possible" from
// drifting into "suspiciously fast".
func TestDefaultConfigHumanFloors(t *testing.T) {
	c := DefaultConfig()
	if c.KeyHold[0] < 40 {
		t.Errorf("KeyHold lower bound %v < 40ms — dwell too short to read as human", c.KeyHold[0])
	}
	if c.ClickHoldButton[0] < 40 || c.ClickHoldInput[0] < 40 {
		t.Errorf("click-hold floors too low: button %v input %v (want ≥40ms)", c.ClickHoldButton[0], c.ClickHoldInput[0])
	}
	if c.MouseMinSteps < 5 {
		t.Errorf("MouseMinSteps %d < 5 — too few points to form a human curve", c.MouseMinSteps)
	}
	if !c.PatchCoalesced {
		t.Error("PatchCoalesced should stay on")
	}
}

func TestRandRespectsBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := Rand(10, 20)
		if v < 10 || v > 20 {
			t.Fatalf("Rand(10,20) returned %v; out of bounds", v)
		}
	}
	if got := Rand(5, 5); got != 5 {
		t.Errorf("Rand(5,5) = %v, want 5", got)
	}
	if got := Rand(10, 5); got != 10 {
		t.Errorf("Rand(10,5) (inverted) = %v, want lo=10", got)
	}
}

func TestRandIntRangeInclusive(t *testing.T) {
	hits := map[int]bool{}
	for i := 0; i < 1000; i++ {
		v := RandIntRange(Range{2, 5})
		if v < 2 || v > 5 {
			t.Fatalf("RandIntRange{2,5} returned %d; out of bounds", v)
		}
		hits[v] = true
	}
	for want := 2; want <= 5; want++ {
		if !hits[want] {
			t.Errorf("RandIntRange{2,5} never hit %d in 1000 draws", want)
		}
	}
}

func TestSleepMsHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := SleepMs(ctx, 500); err == nil {
		t.Errorf("SleepMs with canceled ctx should return error")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("SleepMs returned too slowly under cancel: %v", elapsed)
	}
}

func TestSleepMsZeroIsNoop(t *testing.T) {
	start := time.Now()
	if err := SleepMs(context.Background(), 0); err != nil {
		t.Errorf("SleepMs(0) returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Errorf("SleepMs(0) blocked: %v", elapsed)
	}
}
