package ai

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	apiKeyRateLimitCooldown = time.Minute
	// apiKeyInvalidCooldown is how long a rejected key is set aside. A key is not
	// banned for the life of the process: it can be replaced, and a gateway can
	// reject a request for reasons other than the key itself.
	apiKeyInvalidCooldown = 5 * time.Minute
)

type apiKeyHealth struct {
	invalidUntil time.Time
	limitedUntil time.Time
	lastError    error
}

// APIKeyPoolProvider retries an immediate authentication or rate-limit failure
// with another saved key. Health is deliberately process-local: saved keys are
// all eligible again after Midas restarts.
type APIKeyPoolProvider struct {
	provider Provider
	keys     []string

	mu     sync.Mutex
	active string
	health map[string]apiKeyHealth
}

func NewAPIKeyPoolProvider(provider Provider, keys []string, active string) Provider {
	unique := uniqueAPIKeys(append([]string{active}, keys...))
	if provider == nil || len(unique) < 2 {
		return provider
	}
	return &APIKeyPoolProvider{provider: provider, keys: unique, active: unique[0], health: make(map[string]apiKeyHealth)}
}

func (p *APIKeyPoolProvider) ID() string { return p.provider.ID() }

func (p *APIKeyPoolProvider) Models(ctx context.Context) ([]Model, error) {
	return p.provider.Models(ctx)
}

func (p *APIKeyPoolProvider) Stream(ctx context.Context, model Model, input Context, options StreamOptions) (*AssistantStream, error) {
	candidates, unavailable := p.candidates(options.APIKey, time.Now())
	if len(candidates) == 0 {
		if unavailable != nil {
			return nil, fmt.Errorf("%s: all saved API keys are unavailable: %w", p.ID(), unavailable)
		}
		return nil, fmt.Errorf("%s: no saved API keys are available", p.ID())
	}
	var lastErr error
	for _, apiKey := range candidates {
		attempt := options
		attempt.APIKey = apiKey
		stream, err := p.provider.Stream(ctx, model, input, attempt)
		if err == nil {
			p.markHealthy(apiKey)
			return stream, nil
		}
		lastErr = err
		if !p.markFailure(apiKey, err, time.Now()) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%s: all saved API keys failed: %w", p.ID(), lastErr)
}

func (p *APIKeyPoolProvider) candidates(preferred string, now time.Time) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ordered := uniqueAPIKeys(append([]string{preferred, p.active}, p.keys...))
	result := make([]string, 0, len(ordered))
	var unavailable error
	for _, apiKey := range ordered {
		health := p.health[apiKey]
		if now.Before(health.invalidUntil) || now.Before(health.limitedUntil) {
			if unavailable == nil {
				unavailable = health.lastError
			}
			continue
		}
		result = append(result, apiKey)
	}
	return result, unavailable
}

func (p *APIKeyPoolProvider) markHealthy(apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = apiKey
	delete(p.health, apiKey)
}

func (p *APIKeyPoolProvider) markFailure(apiKey string, err error, now time.Time) bool {
	var response *HTTPError
	if !errors.As(err, &response) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	health := p.health[apiKey]
	health.lastError = err
	switch response.Status {
	case 401:
		health.invalidUntil = now.Add(apiKeyInvalidCooldown)
	case 403, 429:
		// A 403 is usually about what the key may do rather than whether it is
		// valid, so it is set aside like a rate limit instead of being banned.
		health.limitedUntil = now.Add(apiKeyRateLimitCooldown)
	default:
		return false
	}
	p.health[apiKey] = health
	return true
}

func uniqueAPIKeys(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		seen := false
		for _, existing := range result {
			if existing == value {
				seen = true
				break
			}
		}
		if !seen {
			result = append(result, value)
		}
	}
	return result
}
