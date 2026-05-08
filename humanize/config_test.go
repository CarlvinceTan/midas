package humanize

import (
	"context"
	"testing"
	"time"
)

func TestDefaultAndCarefulDiverge(t *testing.T) {
	d := DefaultConfig()
	c := CarefulConfig()

	if d.TypingDelay >= c.TypingDelay {
		t.Errorf("careful TypingDelay should be > default; got %v vs %v", c.TypingDelay, d.TypingDelay)
	}
	if d.MouseOvershootChance <= c.MouseOvershootChance {
		t.Errorf("careful MouseOvershootChance should be < default; got %v vs %v", c.MouseOvershootChance, d.MouseOvershootChance)
	}
	if d.ScrollPauseSlow[1] >= c.ScrollPauseSlow[1] {
		t.Errorf("careful ScrollPauseSlow upper bound should be > default; got %v vs %v", c.ScrollPauseSlow, d.ScrollPauseSlow)
	}
}

func TestResolveConfigPicksPreset(t *testing.T) {
	if got := ResolveConfig(PresetCareful).TypingDelay; got != CarefulConfig().TypingDelay {
		t.Errorf("ResolveConfig(careful) TypingDelay = %v, want %v", got, CarefulConfig().TypingDelay)
	}
	if got := ResolveConfig(PresetDefault).TypingDelay; got != DefaultConfig().TypingDelay {
		t.Errorf("ResolveConfig(default) TypingDelay = %v, want %v", got, DefaultConfig().TypingDelay)
	}
	// Unknown presets fall back to default.
	if got := ResolveConfig(Preset("unknown")).TypingDelay; got != DefaultConfig().TypingDelay {
		t.Errorf("ResolveConfig(unknown) should fall back to default; got %v", got)
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
