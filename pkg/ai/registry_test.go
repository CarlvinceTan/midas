package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type registryProvider struct {
	id     string
	models []Model
	err    error
}

func (p registryProvider) ID() string { return p.id }
func (p registryProvider) Models(context.Context) ([]Model, error) {
	return append([]Model(nil), p.models...), p.err
}
func (p registryProvider) Stream(context.Context, Model, Context, StreamOptions) (*AssistantStream, error) {
	return nil, p.err
}

func TestRegistryListsProvidersAndModelsDeterministically(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry(
		registryProvider{id: "z", models: []Model{{ID: "z-model"}}},
		registryProvider{id: "a", models: []Model{{ID: "a-model"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(registry.ProviderIDs()); got != "[a z]" {
		t.Fatalf("provider IDs = %s", got)
	}
	models, err := registry.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint([]string{models[0].ID, models[1].ID}); got != "[a-model z-model]" {
		t.Fatalf("models = %s", got)
	}
}

func TestRegistryReplacesProviderAndRoutesStream(t *testing.T) {
	t.Parallel()

	want := errors.New("replacement")
	registry, err := NewRegistry(registryProvider{id: "provider", err: errors.New("old")})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(registryProvider{id: "provider", err: want}); err != nil {
		t.Fatal(err)
	}
	_, got := registry.Stream(context.Background(), Model{Provider: "provider"}, Context{}, StreamOptions{})
	if !errors.Is(got, want) {
		t.Fatalf("stream error = %v", got)
	}
}

func TestRegistryRejectsMissingProvider(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Stream(context.Background(), Model{Provider: "missing"}, Context{}, StreamOptions{}); err == nil {
		t.Fatal("expected missing provider error")
	}
	if err := registry.Register(nil); err == nil {
		t.Fatal("expected nil provider error")
	}
}
