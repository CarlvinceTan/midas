package cache

import (
	"testing"
	"time"
)

func TestNewEntry(t *testing.T) {
	actions := []Action{
		{Type: ActionTypeClick, Selector: "#submit"},
		{Type: ActionTypeType, Selector: "#input", Value: "hello"},
	}

	entry := NewEntry("test-key", actions, "https://example.com")

	if entry.Version != Version {
		t.Errorf("expected version %d, got %d", Version, entry.Version)
	}
	if entry.Key != "test-key" {
		t.Errorf("expected key test-key, got %s", entry.Key)
	}
	if entry.URL != "https://example.com" {
		t.Errorf("expected URL https://example.com, got %s", entry.URL)
	}
	if len(entry.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(entry.Actions))
	}
	if !entry.Timestamp.Before(time.Now().Add(time.Second)) {
		t.Error("expected timestamp to be recent")
	}
}

func TestEntryHasVariables(t *testing.T) {
	tests := []struct {
		name      string
		variables []string
		expected  bool
	}{
		{"no variables", nil, false},
		{"empty variables", []string{}, false},
		{"with variables", []string{"username", "password"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &Entry{Variables: tt.variables}
			if got := entry.HasVariables(); got != tt.expected {
				t.Errorf("HasVariables() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEntryIsSelectorBased(t *testing.T) {
	tests := []struct {
		name     string
		actions  []Action
		expected bool
	}{
		{"no actions", nil, false},
		{"goto without selector", []Action{{Type: ActionTypeGoto, Value: "https://example.com"}}, false},
		{"click with selector", []Action{{Type: ActionTypeClick, Selector: "#btn"}}, true},
		{"mixed actions", []Action{
			{Type: ActionTypeGoto, Value: "https://example.com"},
			{Type: ActionTypeClick, Selector: "#btn"},
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &Entry{Actions: tt.actions}
			if got := entry.IsSelectorBased(); got != tt.expected {
				t.Errorf("IsSelectorBased() = %v, want %v", got, tt.expected)
			}
		})
	}
}
