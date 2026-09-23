package tui

import (
	"math"
	"time"
)

// GenerationRate measures live generation speed in tokens/second.
//
// Unlike cumulative throughput (output/total span), it measures the rate over
// each recent sampling window so prefill and tool waits do not drag the reading
// down. When a window has no new output (idle/prefill) the last reading is held
// instead of averaged toward zero. Streamed characters are scaled by a learned
// chars/token ratio that provider usage calibrates over time.
//
// This is a direct port of the pre-port Midas `GenerationRate`; the footer
// numbers are expected to match that implementation.
const (
	rateSampleInterval = 250 * time.Millisecond
	// Windows longer than this are treated as idle and skipped, not averaged.
	rateMaxGap           = 1500 * time.Millisecond
	rateDefaultRatio     = 3.8
	rateMaximum          = 2000.0
	rateMinimumTokens    = 2.0
	rateSmoothing        = 0.3
	rateCalibrationChars = 128
	rateCalibrationMin   = 32
)

type GenerationRate struct {
	rate float64
	has  bool

	key             string
	ratios          map[string]float64
	startedAt       time.Time
	lastSampleAt    time.Time
	lastSampleChars int
	chars           int
}

// Start begins a generation for key, resetting the reading when the model changes.
func (g *GenerationRate) Start(key string, now time.Time) {
	if key != g.key {
		g.rate = 0
		g.has = false
	}
	if g.ratios == nil {
		g.ratios = make(map[string]float64)
	}
	g.key = key
	g.startedAt = now
	g.lastSampleAt = now
	g.lastSampleChars = 0
	g.chars = 0
}

// Add feeds streamed character deltas; sampling happens at most every sample interval.
func (g *GenerationRate) Add(count int, now time.Time) {
	if count <= 0 {
		return
	}
	g.chars += count
	dt := now.Sub(g.lastSampleAt)
	if dt < rateSampleInterval {
		return
	}
	windowChars := g.chars - g.lastSampleChars
	g.lastSampleAt = now
	g.lastSampleChars = g.chars
	// A long gap means idle (prefill, tools): hold the last reading.
	if dt > rateMaxGap || windowChars <= 0 {
		return
	}
	tokens := float64(windowChars) / g.ratio()
	if tokens < rateMinimumTokens {
		return
	}
	instant := tokens / dt.Seconds()
	if math.IsNaN(instant) || math.IsInf(instant, 0) || instant <= 0 || instant >= rateMaximum {
		return
	}
	if !g.has {
		g.rate = instant
		g.has = true
		return
	}
	g.rate = g.rate*(1-rateSmoothing) + instant*rateSmoothing
}

// Finish settles the reading using provider usage. It learns a chars/token
// ratio only when the streamed content represents the billed output; hidden
// reasoning would otherwise skew later live estimates.
func (g *GenerationRate) Finish(output int, calibrate bool, now time.Time) {
	seconds := now.Sub(g.startedAt).Seconds()
	if calibrate && output >= rateCalibrationMin && g.chars >= rateCalibrationChars && seconds >= 1 {
		ratio := float64(g.chars) / float64(output)
		if ratio >= 1 && ratio <= 12 {
			if previous, ok := g.ratios[g.key]; ok {
				g.ratios[g.key] = previous*0.75 + ratio*0.25
			} else {
				g.ratios[g.key] = ratio
			}
		}
	}
	// Keep the live active-generation reading; only fall back to span throughput
	// when no live sample was possible (e.g. a model that streams no text).
	if g.has {
		return
	}
	if output <= 0 || seconds <= 0 {
		return
	}
	measured := float64(output) / seconds
	if !math.IsNaN(measured) && !math.IsInf(measured, 0) && measured > 0 && measured < rateMaximum {
		g.rate = measured
		g.has = true
	}
}

// Rate returns the current reading and whether one exists.
func (g *GenerationRate) Rate() (float64, bool) { return g.rate, g.has }

func (g *GenerationRate) ratio() float64 {
	if g.ratios != nil {
		if ratio, ok := g.ratios[g.key]; ok && ratio > 0 {
			return ratio
		}
	}
	return rateDefaultRatio
}

// RateDisplay eases a measured reading toward the target so the footer fills in
// between provider samples instead of jumping.
type RateDisplay struct {
	value float64
	has   bool
}

// Reset clears the eased value.
func (d *RateDisplay) Reset() { d.value = 0; d.has = false }

// Value returns the eased reading and whether one is shown.
func (d *RateDisplay) Value() (float64, bool) {
	if !d.has {
		return 0, false
	}
	return d.value, true
}

// Step eases toward target and reports whether the rendered value changed.
func (d *RateDisplay) Step(target float64, valid bool) bool {
	if !valid || math.IsNaN(target) || math.IsInf(target, 0) {
		return false
	}
	goal := math.Max(0, target)
	if !d.has {
		// Fill in from zero so the first reading slides up instead of snapping.
		d.value = math.Min(1, goal)
		d.has = true
		return true
	}
	gap := goal - d.value
	if math.Abs(gap) < 0.05 {
		if d.value == goal {
			return false
		}
		d.value = goal
		return true
	}
	// Continuous exponential approach: the number visibly fills between
	// readings but still settles on the measured value (no fake jitter).
	d.value += gap * 0.18
	return true
}
