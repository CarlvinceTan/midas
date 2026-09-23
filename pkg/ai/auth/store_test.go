package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripsAndDeletesProviderCredentials(t *testing.T) {
	directory := t.TempDir()
	store := New(directory)
	credential := Credential{Kind: KindAPIKey, Name: "OpenAI", APIKey: "secret"}
	if err := store.Set("openai", credential); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("openai")
	if !ok || got.Name != "OpenAI" || got.APIKey != "secret" || len(got.APIKeys) != 1 || got.APIKeys[0] != "secret" || store.LastProvider() != "openai" {
		t.Fatalf("credential = %#v, ok = %v, last = %q", got, ok, store.LastProvider())
	}
	info, err := os.Stat(filepath.Join(directory, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	if err := store.Delete("openai"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("openai"); ok || store.LastProvider() != "" {
		t.Fatal("provider was not deleted")
	}
}

func TestStoreAddsSelectsAndRemovesMultipleAPIKeys(t *testing.T) {
	store := New(t.TempDir())
	first := Credential{Kind: KindAPIKey, Name: "OpenAI", APIKey: "first", BaseURL: "https://first.example/v1"}
	if _, err := store.AddAPIKey("openai", first); err != nil {
		t.Fatal(err)
	}
	second := Credential{Kind: KindAPIKey, Name: "OpenAI", APIKey: "second", BaseURL: "https://second.example/v1"}
	got, err := store.AddAPIKey("openai", second)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "second" || len(got.APIKeys) != 2 || got.APIKeys[0] != "first" || got.APIKeys[1] != "second" || got.BaseURL != second.BaseURL {
		t.Fatalf("merged credential = %#v", got)
	}
	got, err = store.SetActiveAPIKey("openai", "first")
	if err != nil || got.APIKey != "first" || len(got.APIKeys) != 2 {
		t.Fatalf("active credential = %#v, %v", got, err)
	}
	got, deleted, err := store.DeleteAPIKey("openai", "first")
	if err != nil || deleted || got.APIKey != "second" || len(got.APIKeys) != 1 {
		t.Fatalf("remaining credential = %#v, deleted=%v, %v", got, deleted, err)
	}
	_, deleted, err = store.DeleteAPIKey("openai", "second")
	if err != nil || !deleted {
		t.Fatalf("last key deleted=%v, %v", deleted, err)
	}
	if _, ok := store.Get("openai"); ok || store.LastProvider() != "" {
		t.Fatal("provider remained after its last key was removed")
	}
}

func TestStoreDeduplicatesAPIKeysAndMigratesLegacyKey(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Set("openai", Credential{Kind: KindAPIKey, Name: "OpenAI", APIKey: "same", APIKeys: []string{"same", "same"}}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("openai")
	if !ok || len(got.APIKeys) != 1 || got.APIKeys[0] != "same" {
		t.Fatalf("credential = %#v", got)
	}
}
