package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestTranscriptRoundTripAndMissing(t *testing.T) {
	store := New(t.TempDir())
	if messages, err := store.ReadTranscript("ses_missing"); err != nil || messages != nil {
		t.Fatalf("missing = %#v, %v", messages, err)
	}
	messages := []ai.Message{ai.NewUserMessage("hello", time.Unix(1, 0)), ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("hi")}, StopReason: ai.StopComplete}}
	if err := store.WriteTranscript("ses_test", messages); err != nil {
		t.Fatal(err)
	}
	restored, err := store.ReadTranscript("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, messages) {
		t.Fatalf("restored = %#v", restored)
	}
	if _, err := store.TranscriptPath("../escape"); err == nil {
		t.Fatal("unsafe id accepted")
	}
}
