package ai

import (
	"context"
	"errors"
	"testing"
)

type poolTestProvider struct {
	calls    []string
	status   map[string]int
	modelHit bool
}

func (p *poolTestProvider) ID() string { return "pool-test" }
func (p *poolTestProvider) Models(context.Context) ([]Model, error) {
	p.modelHit = true
	return []Model{{ID: "test"}}, nil
}
func (p *poolTestProvider) Stream(_ context.Context, _ Model, _ Context, options StreamOptions) (*AssistantStream, error) {
	p.calls = append(p.calls, options.APIKey)
	if status := p.status[options.APIKey]; status != 0 {
		return nil, &HTTPError{Provider: p.ID(), Status: status, Message: "rejected"}
	}
	return NewAssistantStream(), nil
}

func TestAPIKeyPoolRotatesAndKeepsHealthyKeyActive(t *testing.T) {
	base := &poolTestProvider{status: map[string]int{"first": 429}}
	provider := NewAPIKeyPoolProvider(base, []string{"first", "second"}, "first")
	if _, err := provider.Stream(context.Background(), Model{}, Context{}, StreamOptions{APIKey: "first", MaxRetries: 4}); err != nil {
		t.Fatal(err)
	}
	if len(base.calls) != 2 || base.calls[0] != "first" || base.calls[1] != "second" {
		t.Fatalf("calls = %#v", base.calls)
	}
	base.calls = nil
	if _, err := provider.Stream(context.Background(), Model{}, Context{}, StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(base.calls) != 1 || base.calls[0] != "second" {
		t.Fatalf("active calls = %#v", base.calls)
	}
}

func TestAPIKeyPoolMarksAuthenticationFailuresForThisProcess(t *testing.T) {
	base := &poolTestProvider{status: map[string]int{"first": 401, "second": 403}}
	provider := NewAPIKeyPoolProvider(base, []string{"first", "second"}, "first")
	if _, err := provider.Stream(context.Background(), Model{}, Context{}, StreamOptions{}); err == nil {
		t.Fatal("all-invalid pool succeeded")
	}
	base.calls = nil
	if _, err := provider.Stream(context.Background(), Model{}, Context{}, StreamOptions{}); err == nil {
		t.Fatal("unavailable pool succeeded")
	}
	if len(base.calls) != 0 {
		t.Fatalf("invalid keys retried: %#v", base.calls)
	}
}

func TestAPIKeyPoolDoesNotRotateUnrelatedFailures(t *testing.T) {
	base := &poolTestProvider{status: map[string]int{"first": 500}}
	provider := NewAPIKeyPoolProvider(base, []string{"first", "second"}, "first")
	_, err := provider.Stream(context.Background(), Model{}, Context{}, StreamOptions{})
	var response *HTTPError
	if !errors.As(err, &response) || response.Status != 500 {
		t.Fatalf("error = %v", err)
	}
	if len(base.calls) != 1 || base.calls[0] != "first" {
		t.Fatalf("calls = %#v", base.calls)
	}
	if _, err := provider.Models(context.Background()); err != nil || !base.modelHit {
		t.Fatalf("models = %v, hit=%v", err, base.modelHit)
	}
}
