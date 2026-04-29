package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewMemoryStorage(t *testing.T) {
	storage := NewMemoryStorage()
	if storage == nil {
		t.Fatal("expected non-nil storage")
	}
	if storage.IsPersistent() {
		t.Error("memory storage should not be persistent")
	}
}

func TestNewStorage_FileSystem(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "polymux-cache-test")
	defer os.RemoveAll(dir)

	storage, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	if !storage.IsPersistent() {
		t.Error("filesystem storage should be persistent")
	}
}

func TestMemoryStorage_CRUD(t *testing.T) {
	storage := NewMemoryStorage()

	entry := &Entry{
		Version:   Version,
		Key:       "test-key",
		URL:       "https://example.com",
		Actions:   []Action{{Type: ActionTypeClick, Selector: "#btn"}},
		Timestamp: time.Now().UTC(),
	}

	if err := storage.Write(entry); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	read, err := storage.Read("test-key")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read == nil {
		t.Fatal("expected non-nil entry")
	}
	if read.Key != "test-key" {
		t.Errorf("expected key test-key, got %s", read.Key)
	}

	if err := storage.Delete("test-key"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	read, err = storage.Read("test-key")
	if err != nil {
		t.Fatalf("Read() after delete error = %v", err)
	}
	if read != nil {
		t.Error("expected nil entry after delete")
	}
}

func TestMemoryStorage_List(t *testing.T) {
	storage := NewMemoryStorage()

	entries := []*Entry{
		{Version: Version, Key: "key1", Timestamp: time.Now().UTC()},
		{Version: Version, Key: "key2", Timestamp: time.Now().UTC()},
		{Version: Version, Key: "key3", Timestamp: time.Now().UTC()},
	}

	for _, e := range entries {
		if err := storage.Write(e); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	keys, err := storage.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestMemoryStorage_Clear(t *testing.T) {
	storage := NewMemoryStorage()

	entries := []*Entry{
		{Version: Version, Key: "key1", Timestamp: time.Now().UTC()},
		{Version: Version, Key: "key2", Timestamp: time.Now().UTC()},
	}

	for _, e := range entries {
		if err := storage.Write(e); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	count, err := storage.Clear("key1")
	if err != nil {
		t.Fatalf("Clear(key1) error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 cleared, got %d", count)
	}

	count, err = storage.Clear("")
	if err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 cleared, got %d", count)
	}
}

func TestCache_StoreAndLookup(t *testing.T) {
	storage := NewMemoryStorage()
	cache := New(storage)

	actions := []Action{
		{Type: ActionTypeClick, Selector: "#btn"},
		{Type: ActionTypeType, Selector: "#input", Value: "%username%"},
	}

	ctx := context.Background()
	result, err := cache.Store(ctx, "login", actions, []string{"username"}, nil, "https://example.com/login")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if !result.Success {
		t.Error("expected successful store")
	}

	lookup, err := cache.Lookup(ctx, "login", "https://example.com/login")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !lookup.Found {
		t.Error("expected to find entry")
	}
	if !lookup.URLMatch {
		t.Error("expected URL match")
	}
	if len(lookup.Entry.Actions) != 2 {
		t.Errorf("expected 2 actions, got %d", len(lookup.Entry.Actions))
	}
}

func TestCache_Lookup_URLMismatch(t *testing.T) {
	storage := NewMemoryStorage()
	cache := New(storage)

	actions := []Action{{Type: ActionTypeClick, Selector: "#btn"}}

	ctx := context.Background()
	_, err := cache.Store(ctx, "test", actions, nil, nil, "https://example1.com")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	lookup, err := cache.Lookup(ctx, "test", "https://example2.com")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if !lookup.Found {
		t.Error("expected to find entry")
	}
	if lookup.URLMatch {
		t.Error("expected URL mismatch")
	}
}

func TestCache_InterpolateVariables(t *testing.T) {
	storage := NewMemoryStorage()
	cache := New(storage)

	entry := &Entry{
		Version: Version,
		Key:     "test",
		Actions: []Action{
			{Type: ActionTypeType, Selector: "#user", Value: "%username%"},
			{Type: ActionTypeType, Selector: "#pass", Value: "%password%"},
		},
		Variables: []string{"username", "password"},
	}

	variables := map[string]string{
		"username": "john",
		"password": "secret",
	}

	actions := cache.InterpolateVariables(entry, variables)

	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	if actions[0].Value != "john" {
		t.Errorf("expected 'john', got %s", actions[0].Value)
	}
	if actions[1].Value != "secret" {
		t.Errorf("expected 'secret', got %s", actions[1].Value)
	}
}

func TestCache_ValidateVariables(t *testing.T) {
	storage := NewMemoryStorage()
	cache := New(storage)

	entry := &Entry{
		Version:   Version,
		Key:       "test",
		Variables: []string{"username", "password"},
	}

	tests := []struct {
		name      string
		variables map[string]string
		expected  int
	}{
		{"all provided", map[string]string{"username": "a", "password": "b"}, 0},
		{"partial", map[string]string{"username": "a"}, 1},
		{"none provided", nil, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing := cache.ValidateVariables(entry, tt.variables)
			if len(missing) != tt.expected {
				t.Errorf("expected %d missing, got %d", tt.expected, len(missing))
			}
		})
	}
}
