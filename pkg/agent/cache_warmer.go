package agent

import (
	"context"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

const (
	cacheWarmingMaximumAge      = time.Hour
	cacheWarmingIdleMaximumAge  = 30 * time.Minute
	cacheWarmingMinimumSavings  = 0.05
	cacheWarmingIdleProbability = 0.15
)

type cacheWarmRequest struct {
	model        ai.Model
	input        ai.Context
	options      ai.StreamOptions
	promptTokens int
}

// CacheWarmer keeps only the latest provider prefix alive. A Chat owns one so
// idle warming can safely span agent runs while a model switch, new session,
// or new real request cancels the previous refresh.
type CacheWarmer struct {
	mu       sync.Mutex
	provider ai.Streamer
	onWarmed func(ai.Usage)
	timer    *time.Timer
	cancel   context.CancelFunc
	closed   bool
	serial   uint64
	phase    CacheWarmingMode
	started  time.Time
	request  cacheWarmRequest
}

func NewCacheWarmer(provider ai.Streamer, onWarmed func(ai.Usage)) *CacheWarmer {
	return &CacheWarmer{provider: provider, onWarmed: onWarmed}
}

func promptCacheTTL(model ai.Model, retention ai.CacheRetention) time.Duration {
	seconds := 0
	switch retention {
	case ai.CacheNone:
		return 0
	case ai.CacheLong:
		seconds = model.PromptCache.Long
	default:
		seconds = model.PromptCache.Short
	}
	return time.Duration(seconds) * time.Second
}

func cacheWarmingDelay(ttl time.Duration) time.Duration {
	if ttl <= 10*time.Second {
		return 0
	}
	delay := ttl * 9 / 10
	if latest := ttl - 10*time.Second; delay > latest {
		delay = latest
	}
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func cacheWarmingEconomics(model ai.Model, promptTokens int, continuationProbability float64) (warmCost, missCost, expectedSavings float64, worthwhile bool) {
	if promptTokens <= 0 {
		return 0, 0, 0, false
	}
	warmUsage := ai.Usage{CacheRead: promptTokens, Output: 1}
	warmCost = ai.CalculateCost(model, &warmUsage).Total
	hitUsage := ai.Usage{CacheRead: promptTokens}
	hitCost := ai.CalculateCost(model, &hitUsage).Total
	missUsage := ai.Usage{Input: promptTokens}
	if model.Cost.CacheWrite > 0 {
		missUsage = ai.Usage{CacheWrite: promptTokens}
	}
	missCost = max(0, ai.CalculateCost(model, &missUsage).Total-hitCost)
	expectedSavings = continuationProbability*missCost - warmCost
	economicsAvailable := model.Cost.Input > 0 || model.Cost.CacheRead > 0 || model.Cost.CacheWrite > 0
	return warmCost, missCost, expectedSavings, economicsAvailable && expectedSavings >= cacheWarmingMinimumSavings
}

func (w *CacheWarmer) start(request cacheWarmRequest, mode CacheWarmingMode) {
	if w == nil || w.provider == nil {
		return
	}
	if mode == CacheWarmingOff {
		// Warming can be turned off for a run without rebuilding the session's
		// warmer, so the mode decides whether a refresh is scheduled at all.
		w.Stop()
		return
	}
	if request.options.Reasoning != ai.ThinkingOff && request.model.API == "anthropic-messages" && !request.model.AdaptiveThinking {
		w.Stop()
		return
	}
	ttl := promptCacheTTL(request.model, request.options.CacheRetention)
	delay := cacheWarmingDelay(ttl)
	_, _, _, worthwhile := cacheWarmingEconomics(request.model, request.promptTokens, 1)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
	if w.closed || delay == 0 || !worthwhile {
		return
	}
	w.serial++
	w.phase = CacheWarmingStreaming
	w.started = time.Now()
	w.request = request
	w.scheduleLocked(delay)
}

func (w *CacheWarmer) scheduleLocked(delay time.Duration) {
	deadline := w.started.Add(cacheWarmingMaximumAge)
	if w.phase == CacheWarmingIdle {
		deadline = w.started.Add(cacheWarmingIdleMaximumAge)
	}
	if time.Now().Add(delay).After(deadline) {
		w.stopLocked()
		return
	}
	serial := w.serial
	w.timer = time.AfterFunc(delay, func() { w.refresh(serial, delay) })
}

func (w *CacheWarmer) refresh(serial uint64, delay time.Duration) {
	w.mu.Lock()
	if w.closed || serial != w.serial {
		w.mu.Unlock()
		return
	}
	probability := 1.0
	if w.phase == CacheWarmingIdle {
		probability = cacheWarmingIdleProbability
	}
	request := w.request
	_, _, _, worthwhile := cacheWarmingEconomics(request.model, request.promptTokens, probability)
	if !worthwhile {
		w.stopLocked()
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.timer = nil
	w.mu.Unlock()

	options := request.options
	options.MaxTokens = 1
	options.MaxRetries = 0
	stream, err := w.provider.Stream(ctx, request.model, request.input, options)
	if err == nil {
		if message, resultErr := stream.Result(ctx); resultErr == nil && message.StopReason != ai.StopError && message.StopReason != ai.StopAborted && w.onWarmed != nil {
			w.onWarmed(message.Usage)
		}
	}
	cancel()

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || serial != w.serial {
		return
	}
	w.cancel = nil
	w.scheduleLocked(delay)
}

func (w *CacheWarmer) settle(mode CacheWarmingMode) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if mode != CacheWarmingIdle || w.closed || w.timer == nil {
		w.serial++
		w.stopLocked()
		return
	}
	w.phase = CacheWarmingIdle
	_, _, _, worthwhile := cacheWarmingEconomics(w.request.model, w.request.promptTokens, cacheWarmingIdleProbability)
	delay := cacheWarmingDelay(promptCacheTTL(w.request.model, w.request.options.CacheRetention))
	if !worthwhile || delay == 0 || time.Now().Add(delay).After(w.started.Add(cacheWarmingIdleMaximumAge)) {
		w.serial++
		w.stopLocked()
	}
}

func (w *CacheWarmer) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.serial++
	w.stopLocked()
}

func (w *CacheWarmer) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.serial++
	w.stopLocked()
}

func (w *CacheWarmer) stopLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}
