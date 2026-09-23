package tui

import (
	"math"
	"testing"
	"time"
)

func TestRateDisplayFillsInFromZeroAndSettles(t *testing.T) {
	var display RateDisplay
	if _, ok := display.Value(); ok {
		t.Fatal("a fresh display has no reading")
	}
	// First tick slides up from zero rather than snapping to the target.
	if !display.Step(100, true) {
		t.Fatal("the first tick should request a render")
	}
	value, _ := display.Value()
	if value != 1 {
		t.Fatalf("first value = %v, want 1", value)
	}
	sawIntermediate := false
	for index := 0; index < 200; index++ {
		display.Step(100, true)
		value, _ = display.Value()
		if value > 100 {
			t.Fatalf("display overshot the target: %v", value)
		}
		if value > 1 && value < 100 {
			sawIntermediate = true
		}
	}
	if !sawIntermediate {
		t.Fatal("display never passed through an intermediate value")
	}
	if math.Round(value) != 100 {
		t.Fatalf("settled value = %v, want 100", value)
	}
	if display.Step(100, true) {
		t.Fatal("a settled display should stop requesting renders")
	}
}

func TestRateDisplayEasesBackDown(t *testing.T) {
	var display RateDisplay
	display.Step(80, true)
	for display.Step(80, true) {
	}
	value, _ := display.Value()
	if math.Round(value) != 80 {
		t.Fatalf("settled value = %v, want 80", value)
	}
	if !display.Step(20, true) {
		t.Fatal("a lower target should request a render")
	}
	value, _ = display.Value()
	if value <= 20 || value >= 80 {
		t.Fatalf("eased value = %v, want between 20 and 80", value)
	}
}

func TestRateDisplayIgnoresMissingTarget(t *testing.T) {
	var display RateDisplay
	if display.Step(0, false) {
		t.Fatal("an invalid target should not request a render")
	}
	if _, ok := display.Value(); ok {
		t.Fatal("an invalid target should not create a reading")
	}
}

func TestGenerationRateSamplesWindowsAndHoldsOnIdle(t *testing.T) {
	var rate GenerationRate
	start := time.Unix(0, 0)
	rate.Start("test/model", start)
	// 60 chars over 500ms at the default 3.8 chars/token is about 31.6 t/s.
	rate.Add(30, start.Add(100*time.Millisecond))
	rate.Add(30, start.Add(500*time.Millisecond))
	value, ok := rate.Rate()
	if !ok {
		t.Fatal("a live window should produce a reading")
	}
	if value < 20 || value > 45 {
		t.Fatalf("reading = %v, want roughly 31", value)
	}
	// A gap longer than the idle threshold holds the previous reading.
	rate.Add(200, start.Add(3*time.Second))
	held, _ := rate.Rate()
	if held != value {
		t.Fatalf("idle gap changed the reading: %v -> %v", value, held)
	}
}

func TestGenerationRateFallsBackToSpanThroughput(t *testing.T) {
	var rate GenerationRate
	start := time.Unix(0, 0)
	rate.Start("test/model", start)
	// No streamed characters at all: the span reading is the only signal.
	rate.Finish(50, false, start.Add(2*time.Second))
	value, ok := rate.Rate()
	if !ok {
		t.Fatal("a finished generation should produce a reading")
	}
	if math.Abs(value-25) > 0.001 {
		t.Fatalf("reading = %v, want 25", value)
	}
}

func TestGenerationRateCalibratesCharsPerToken(t *testing.T) {
	var rate GenerationRate
	start := time.Unix(0, 0)
	rate.Start("test/model", start)
	rate.Add(200, start.Add(300*time.Millisecond))
	rate.Finish(40, true, start.Add(2*time.Second))
	if ratio := rate.ratio(); math.Abs(ratio-5) > 0.001 {
		t.Fatalf("calibrated ratio = %v, want 5", ratio)
	}
	// A later generation with the same key reuses the learned ratio.
	rate.Start("test/model", start.Add(3*time.Second))
	if ratio := rate.ratio(); math.Abs(ratio-5) > 0.001 {
		t.Fatalf("ratio = %v, want the learned 5", ratio)
	}
}
