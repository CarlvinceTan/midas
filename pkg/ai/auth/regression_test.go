package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritePathsRefuseToClobberAnUnreadableStore: a corrupt or unreadable
// auth.json used to look like an empty store, so the next write silently replaced
// every saved credential.
func TestWritePathsRefuseToClobberAnUnreadableStore(t *testing.T) {
	directory := t.TempDir()
	store := New(directory)
	broken := []byte("{ not json")
	if err := os.WriteFile(store.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("openai", Credential{Kind: KindAPIKey, Name: "openai", APIKey: "sk-test"}); err == nil {
		t.Fatal("Set overwrote an unreadable store")
	}
	if _, err := store.AddAPIKey("openai", Credential{Kind: KindAPIKey, Name: "openai", APIKey: "sk-test"}); err == nil {
		t.Fatal("AddAPIKey overwrote an unreadable store")
	}
	if err := store.Delete("openai"); err == nil {
		t.Fatal("Delete overwrote an unreadable store")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(broken) {
		t.Fatalf("the unreadable file was rewritten as %q", string(after))
	}
}

// TestMissingStoreStillWrites is the other half: a store that simply does not
// exist yet is not an error.
func TestMissingStoreStillWrites(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Set("openai", Credential{Kind: KindAPIKey, Name: "openai", APIKey: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Providers["openai"].APIKey != "sk-test" {
		t.Fatalf("stored credential = %#v", data.Providers["openai"])
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("auth.json mode = %o", mode)
	}
	if _, err := os.Stat(filepath.Join(store.Path(), "..")); err != nil && strings.Contains(err.Error(), "permission") {
		t.Log(err)
	}
}
