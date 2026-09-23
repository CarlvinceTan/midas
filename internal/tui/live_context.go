package tui

import (
	"math"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// LiveContext is the display-only context estimate shown as `ctx (%)` in the
// footer: it grows with streamed text/thinking/tool arguments and reconciles
// against provider usage once it arrives. It never changes accounting or
// compaction decisions. This is a direct port of the pre-port Midas `LiveContext`
// (itself ported from pi), so the footer numbers match that implementation.
type LiveContext struct {
	active bool
	base   *int
	chars  int
	input  *int
	output int
}

// Start begins a live estimate from the context size known before the turn.
func (l *LiveContext) Start(tokens int, known bool) {
	l.Clear()
	l.active = true
	if known {
		value := tokens
		l.base = &value
	}
}

// AddChars records streamed content (text, thinking, tool arguments).
func (l *LiveContext) AddChars(count int) {
	if !l.active || count <= 0 {
		return
	}
	l.chars += count
}

// UpdateUsage reconciles the estimate with provider usage: cached input is part
// of the context, not additional generated output, and a moving output total
// only ever grows within a turn.
func (l *LiveContext) UpdateUsage(usage ai.Usage) {
	if !l.active {
		return
	}
	if input := positiveUsage(usage.Input) + positiveUsage(usage.CacheRead) + positiveUsage(usage.CacheWrite); input > 0 {
		value := input
		l.input = &value
	}
	if output := positiveUsage(usage.Output); output > l.output {
		l.output = output
	}
}

// Read returns the estimated context size for the current model window.
func (l *LiveContext) Read(current int, contextWindow int, known bool) (int, bool) {
	if !l.active || contextWindow <= 0 || !known {
		return current, known
	}
	base := l.input
	if base == nil {
		base = l.base
	}
	if base == nil {
		// Unknown after compaction until provider input usage arrives.
		return current, known
	}
	// Use the baseline captured before streaming, never a moving total which might
	// already include the assistant. Deltas and provider output overlap.
	tokens := *base + max(l.output, int(math.Ceil(float64(l.chars)/4)))
	return tokens, true
}

// Clear drops the estimate.
func (l *LiveContext) Clear() {
	l.active = false
	l.base, l.input = nil, nil
	l.chars, l.output = 0, 0
}

func positiveUsage(value int) int {
	if value > 0 {
		return value
	}
	return 0
}

// ContextDisplay samples live totals so the footer does not flicker between
// frames: intermediate estimated values are capped at one sample per second,
// while final provider usage, tool results and unknown states apply immediately.
type ContextDisplay struct {
	sampled   int
	window    int
	sampledAt time.Time
	has       bool
}

// Read samples an estimated context reading. The second result reports whether a
// usable reading exists at all (a known window with a non-zero size).
func (d *ContextDisplay) Read(tokens, contextWindow int, estimated bool, now time.Time) (int, bool) {
	if !estimated {
		d.Clear()
		return tokens, contextWindow > 0 && tokens > 0
	}
	if !d.has || now.Sub(d.sampledAt) >= time.Second || now.Before(d.sampledAt) || contextWindow != d.window {
		d.sampled, d.window, d.sampledAt, d.has = tokens, contextWindow, now, true
	}
	return d.sampled, d.sampled > 0 && d.window > 0
}

// Clear drops the sampled reading.
func (d *ContextDisplay) Clear() {
	d.sampled, d.window, d.sampledAt, d.has = 0, 0, time.Time{}, false
}
