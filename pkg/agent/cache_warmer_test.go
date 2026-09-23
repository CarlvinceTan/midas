package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

type warmingProvider struct {
	mu      sync.Mutex
	options []ai.StreamOptions
}

func (p *warmingProvider) Stream(_ context.Context, model ai.Model, _ ai.Context, options ai.StreamOptions) (*ai.AssistantStream, error) {
	p.mu.Lock()
	p.options = append(p.options, options)
	p.mu.Unlock()
	message := ai.AssistantMessage{Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID, StopReason: ai.StopComplete, Usage: ai.Usage{CacheRead: 10, Output: 1}}
	stream := ai.NewAssistantStream()
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Message: &message})
	return stream, nil
}

func TestCacheWarmingDelayUsesNinetyPercentAndMargin(t *testing.T) {
	if got := cacheWarmingDelay(5 * time.Minute); got != 270*time.Second {
		t.Fatalf("five-minute delay = %s", got)
	}
	if got := cacheWarmingDelay(15 * time.Second); got != 5*time.Second {
		t.Fatalf("fifteen-second delay = %s", got)
	}
	if got := cacheWarmingDelay(10 * time.Second); got != 0 {
		t.Fatalf("short TTL delay = %s", got)
	}
}

func TestCacheWarmingRequiresMeaningfulExpectedSavings(t *testing.T) {
	model := ai.Model{Cost: ai.ModelCost{Input: 10, Output: 10, CacheRead: 1, CacheWrite: 12}}
	warm, miss, savings, worthwhile := cacheWarmingEconomics(model, 10_000, 1)
	if !worthwhile || warm <= 0 || miss <= warm || savings < cacheWarmingMinimumSavings {
		t.Fatalf("economics = warm %.4f miss %.4f savings %.4f worthwhile %v", warm, miss, savings, worthwhile)
	}
	if _, _, _, worthwhile := cacheWarmingEconomics(model, 100, 1); worthwhile {
		t.Fatal("small prompt unexpectedly qualified for a paid refresh")
	}
}

func TestCacheWarmingIdleDiscountsContinuationAndLongTTLStillSchedules(t *testing.T) {
	model := ai.Model{Cost: ai.ModelCost{Input: 10, Output: 10, CacheRead: 1}}
	_, _, streamingSavings, streaming := cacheWarmingEconomics(model, 10_000, 1)
	_, _, idleSavings, idle := cacheWarmingEconomics(model, 10_000, cacheWarmingIdleProbability)
	if !streaming || idle || idleSavings >= streamingSavings {
		t.Fatalf("streaming=%v %.4f idle=%v %.4f", streaming, streamingSavings, idle, idleSavings)
	}

	provider := &warmingProvider{}
	warmer := NewCacheWarmer(provider, nil)
	warmer.start(cacheWarmRequest{
		model:        ai.Model{ID: "model", Cost: model.Cost, PromptCache: ai.ModelPromptCache{Long: 3600}},
		promptTokens: 100_000,
		options:      ai.StreamOptions{CacheRetention: ai.CacheLong},
	}, CacheWarmingStreaming)
	warmer.mu.Lock()
	scheduled := warmer.timer != nil
	warmer.mu.Unlock()
	warmer.Close()
	if !scheduled {
		t.Fatal("one-hour cache TTL did not schedule its first refresh")
	}
}

func TestCacheWarmRefreshCapsOutputAndDisablesRetries(t *testing.T) {
	provider := &warmingProvider{}
	warmed := make(chan ai.Usage, 1)
	warmer := NewCacheWarmer(provider, func(usage ai.Usage) { warmed <- usage })
	request := cacheWarmRequest{
		model:        ai.Model{ID: "model", Provider: "provider", Cost: ai.ModelCost{Input: 10, Output: 10, CacheRead: 1}},
		promptTokens: 10_000,
		options:      ai.StreamOptions{MaxTokens: 4096, MaxRetries: 3},
	}
	warmer.request = request
	warmer.phase = CacheWarmingStreaming
	warmer.started = time.Now()
	warmer.refresh(0, time.Minute)
	warmer.Close()
	provider.mu.Lock()
	options := append([]ai.StreamOptions(nil), provider.options...)
	provider.mu.Unlock()
	if len(options) != 1 || options[0].MaxTokens != 1 || options[0].MaxRetries != 0 {
		t.Fatalf("warm options = %#v", options)
	}
	select {
	case usage := <-warmed:
		if usage.CacheRead != 10 {
			t.Fatalf("warm usage = %#v", usage)
		}
	default:
		t.Fatal("successful warm usage was not reported")
	}
}
