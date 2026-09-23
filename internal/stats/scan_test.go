package stats

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestScanMidasReadsOnlyRegisteredMidasTranscripts(t *testing.T) {
	directory := t.TempDir()
	store := storage.New(directory)
	title := "Midas session"
	store.UpsertSession(storage.SessionUpdate{ID: "ses_midas", CWD: "/repo", Title: &title})
	assistant := ai.AssistantMessage{
		Role: ai.RoleAssistant, Usage: ai.Usage{
			Input: 10, Output: 5, CacheRead: 20, CacheWrite: 3,
			Cost: ai.UsageCost{Total: 0.42},
		},
	}
	if err := store.WriteTranscript("ses_midas", []ai.Message{ai.NewUserMessage("hello", time.Now()), assistant}); err != nil {
		t.Fatal(err)
	}
	// A transcript file not present in Midas's own registry is deliberately not
	// discovered or counted.
	if err := store.WriteTranscript("foreign", []ai.Message{assistant, assistant}); err != nil {
		t.Fatal(err)
	}
	scan := ScanMidas(store)
	summary, err := scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sessions != 1 || summary.Calls != 1 || summary.Cost != 0.42 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Tokens != (Tokens{Input: 10, Output: 5, CacheRead: 20, CacheWrite: 3}) {
		t.Fatalf("tokens = %#v", summary.Tokens)
	}
	if filepath.Base(store.SessionsPath()) != "sessions.json" {
		t.Fatal("unexpected session store")
	}
}

func TestScanMidasHonorsCancellation(t *testing.T) {
	store := storage.New(t.TempDir())
	title := "cancelled"
	store.UpsertSession(storage.SessionUpdate{ID: "ses_cancel", Title: &title})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanMidas(store)(ctx); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}
