package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndUpdatesPreserveExistingSettings(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(path, []byte(`{"agentModels":{"main":"provider/model"},"compactHeader":true,"titleMaxWords":99,"statusMaxWords":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(directory)
	values := store.Load()
	if values.AutoModelRotation != true || values.AutoProviderRotation != false || values.TerminalTitle != true || !values.CompactHeader || values.TitleMaxWords != 20 || values.StatusMaxWords != 12 || values.RemotePassword != "" {
		t.Fatalf("values = %#v", values)
	}
	if err := store.Set(RemotePassword, "secret phrase"); err != nil {
		t.Fatal(err)
	}
	if got := store.Load().RemotePassword; got != "secret phrase" {
		t.Fatalf("remote password = %q", got)
	}
	if err := store.Set(AutoProviderRotation, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["agentModels"] == nil || string(raw[AutoProviderRotation]) != "true" {
		t.Fatalf("settings were not merged: %s", data)
	}
}

func TestStringMapsRoundTripAndClearEntries(t *testing.T) {
	directory := t.TempDir()
	store := New(directory)
	if err := store.SetStringMapEntry(AgentModels, "task", "openai/gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStringMapEntry(AgentLastUsed, " main ", " openai/gpt-6 "); err != nil {
		t.Fatal(err)
	}
	if got := store.StringMap(AgentModels); len(got) != 1 || got["task"] != "openai/gpt-5.6-sol" {
		t.Fatalf("agent models = %#v", got)
	}
	if got := store.StringMap(AgentLastUsed); len(got) != 1 || got["main"] != "openai/gpt-6" {
		t.Fatalf("agent last used = %#v", got)
	}
	// A cleared entry disappears so the agent returns to its default instead of
	// pinning an empty model.
	if err := store.SetStringMapEntry(AgentModels, "task", ""); err != nil {
		t.Fatal(err)
	}
	if got := store.StringMap(AgentModels); len(got) != 0 {
		t.Fatalf("cleared agent models = %#v", got)
	}
	// Unrelated keys survive a per-agent write.
	if err := store.Set(CompactHeader, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStringMapEntry(ModelThinkingLevels, "openai/gpt-6", "high"); err != nil {
		t.Fatal(err)
	}
	if !store.Load().CompactHeader || store.StringMap(ModelThinkingLevels)["openai/gpt-6"] != "high" {
		t.Fatal("per-agent writes did not merge with existing settings")
	}
}

func TestStringMapIgnoresMalformedEntries(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(path, []byte(`{"agentModels":{"main":""," ":"provider/model","task":"openai/gpt-5.6-sol"},"agentLastUsed":"nonsense"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(directory)
	models := store.StringMap(AgentModels)
	if len(models) != 1 || models["task"] != "openai/gpt-5.6-sol" {
		t.Fatalf("agent models = %#v", models)
	}
	if got := store.StringMap(AgentLastUsed); len(got) != 0 {
		t.Fatalf("agent last used = %#v", got)
	}
}

func TestPaddingDefaultsToOneAndClamps(t *testing.T) {
	directory := t.TempDir()
	store := New(directory)
	if got := store.Load().Padding; got != 1 {
		t.Fatalf("default padding = %d, want 1", got)
	}
	for _, test := range []struct {
		written int
		want    int
	}{
		{written: 0, want: 0},
		{written: 3, want: 3},
		{written: 9, want: 4},
		{written: -2, want: 0},
	} {
		if err := store.Set(Padding, test.written); err != nil {
			t.Fatal(err)
		}
		if got := store.Load().Padding; got != test.want {
			t.Fatalf("padding %d loaded as %d, want %d", test.written, got, test.want)
		}
	}
}

func TestCompactionSettingsDefaultAndRoundTrip(t *testing.T) {
	store := New(t.TempDir())
	if got := store.Load().Compaction; got != DefaultCompactionSettings() {
		t.Fatalf("default compaction = %#v", got)
	}
	if err := store.SetCompaction(CompactionSettings{Enabled: false, ReserveTokens: 4096, KeepRecentTokens: 9000}); err != nil {
		t.Fatal(err)
	}
	stored := store.Load().Compaction
	if stored.Enabled || stored.ReserveTokens != 4096 || stored.KeepRecentTokens != 9000 {
		t.Fatalf("stored compaction = %#v", stored)
	}
	// Out-of-range budgets fall back to the defaults instead of breaking startup.
	if err := store.Set(Compaction, map[string]any{"enabled": true, "reserveTokens": 10, "keepRecentTokens": 5}); err != nil {
		t.Fatal(err)
	}
	stored = store.Load().Compaction
	if !stored.Enabled || stored.ReserveTokens != DefaultCompactionSettings().ReserveTokens || stored.KeepRecentTokens != DefaultCompactionSettings().KeepRecentTokens {
		t.Fatalf("clamped compaction = %#v", stored)
	}
}
