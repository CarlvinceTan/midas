package ai

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Registry owns provider implementations and provides deterministic model
// discovery. It is safe for concurrent reads and provider replacement.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) (*Registry, error) {
	registry := &Registry{providers: make(map[string]Provider)}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("ai: cannot register a nil provider")
	}
	id := provider.ID()
	if id == "" {
		return fmt.Errorf("ai: provider ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	r.providers[id] = provider
	return nil
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, id)
}

func (r *Registry) Provider(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[id]
	return provider, ok
}

func (r *Registry) ProviderIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Models returns every provider's models in provider-ID order. One failing
// provider fails the operation so configuration problems are not hidden.
func (r *Registry) Models(ctx context.Context) ([]Model, error) {
	ids := r.ProviderIDs()
	models := make([]Model, 0)
	for _, id := range ids {
		provider, ok := r.Provider(id)
		if !ok {
			// The provider was removed between listing and lookup.
			continue
		}
		provided, err := provider.Models(ctx)
		if err != nil {
			return nil, fmt.Errorf("ai: list models for provider %s: %w", id, err)
		}
		models = append(models, provided...)
	}
	return models, nil
}

func (r *Registry) Stream(ctx context.Context, model Model, input Context, options StreamOptions) (*AssistantStream, error) {
	provider, ok := r.Provider(model.Provider)
	if !ok {
		return nil, fmt.Errorf("ai: provider %q is not registered", model.Provider)
	}
	return provider.Stream(ctx, model, input, options)
}
