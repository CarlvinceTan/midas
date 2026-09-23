package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func pointer[T any](value T) *T { return &value }

func decodeJSONFile(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func jsonValue(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestStoreMatchesReferenceFixture(t *testing.T) {
	directory := t.TempDir()
	now := int64(1000)
	store := newWithClock(directory, func() time.Time { return time.UnixMilli(now) })
	store.UpsertSession(SessionUpdate{ID: "s1", CWD: "/tmp/one", Title: pointer("Real title")})
	now = 900
	store.UpsertSession(SessionUpdate{ID: "s1", Title: pointer("New session")})
	now = 1100
	store.UpsertSession(SessionUpdate{ID: "s1", Title: pointer("")})
	now = 1200
	store.UpsertSession(SessionUpdate{ID: "s2", CWD: "/tmp/two", Title: pointer("Second"), CreatedAt: pointer(int64(5)), UpdatedAt: pointer(int64(5000))})

	now = 2000
	store.WriteDraft("s1", Draft{Text: "draft one", Attachments: []ImageChip{{Marker: "[Image: a.png]", Path: "/tmp/a.png"}}, Files: []FileChip{{Marker: "[File: a.txt]", Path: "/tmp/a.txt", ID: "f1", Name: "a.txt"}}})
	now = 1900
	store.WriteDraft("s2", Draft{Text: "draft two"})
	store.WriteDraft("empty", Draft{})

	now = 3000
	exitCode := 0
	store.WriteSessionState("s1", SessionState{
		CWD:  "/work",
		Bash: []BashEntry{{Command: "! pwd", Output: "/work\n", Status: "complete", ExitCode: &exitCode, At: 12}},
		Queue: []QueuedPrompt{
			{Text: "empty attachments", Attachments: []Attachment{}},
			{
				Text: "files",
				Files: []FileChip{
					{Marker: "[File: kept.txt]", Path: "/tmp/kept.txt", ID: "kept", Name: "kept.txt"},
					{Marker: "[File: missing.txt]", Path: "/tmp/missing.txt", ID: "missing", Name: "missing.txt"},
				},
				FrozenFiles:   []FrozenFile{{ID: "kept", Marker: "[File: kept.txt]", Path: "/tmp/kept.txt", Name: "kept.txt", Kind: "text", Content: "body"}},
				NeedsReattach: []FileChip{{Marker: "[File: prior.txt]", Path: "/tmp/prior.txt", ID: "prior", Name: "prior.txt"}},
			},
		},
		QueueHold: true,
	})

	draft1, _ := store.ReadDraft("s1")
	draft2, _ := store.ReadDraft("s2")
	_, missingDraft := store.ReadDraft("missing")
	state, _ := store.ReadSessionState("s1")
	actual := map[string]any{
		"sessions": map[string]any{"read": store.ReadSessions(), "file": decodeJSONFile(t, store.SessionsPath())},
		"drafts": map[string]any{"s1": draft1, "s2": draft2, "missing": func() any {
			if missingDraft {
				return Draft{}
			}
			return nil
		}(), "file": decodeJSONFile(t, store.DraftsPath())},
		"state": map[string]any{"read": state, "file": decodeJSONFile(t, store.SessionStatePath())},
	}
	expected := decodeJSONFile(t, filepath.Join("testdata", "reference.json"))
	if normalized := jsonValue(t, actual); !reflect.DeepEqual(normalized, expected) {
		got, _ := json.MarshalIndent(normalized, "", "  ")
		want, _ := json.MarshalIndent(expected, "", "  ")
		t.Fatalf("session store mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestStoreBoundsAndDeletion(t *testing.T) {
	now := int64(1)
	store := newWithClock(t.TempDir(), func() time.Time { value := now; now++; return time.UnixMilli(value) })
	for index := range 130 {
		store.WriteDraft("draft-"+string(rune(0x100+index)), Draft{Text: "saved"})
	}
	if _, ok := store.ReadDraft("draft-" + string(rune(0x100))); ok {
		t.Fatal("oldest draft was not pruned")
	}
	store.WriteDraft("delete", Draft{Text: "saved"})
	store.WriteDraft("delete", Draft{})
	if _, ok := store.ReadDraft("delete"); ok {
		t.Fatal("empty draft was retained")
	}

	bash := make([]BashEntry, 250)
	for index := range bash {
		bash[index] = BashEntry{Command: "command", Output: "out", Status: "complete", At: int64(index)}
	}
	bash[len(bash)-1].Output = strings.Repeat("x", maxBashOutput+1)
	queue := make([]QueuedPrompt, 80)
	for index := range queue {
		queue[index] = QueuedPrompt{Text: "queued", Attachments: []Attachment{}}
	}
	store.WriteSessionState("bounded", SessionState{Bash: bash, Queue: queue})
	state, ok := store.ReadSessionState("bounded")
	if !ok || len(state.Bash) != maxBashEntries || len(state.Queue) != maxQueuedPrompts {
		t.Fatalf("bounded state sizes = %d bash, %d queue", len(state.Bash), len(state.Queue))
	}
	if len(state.Bash[len(state.Bash)-1].Output) != maxBashOutput {
		t.Fatal("bash output was not tail-truncated")
	}
	store.WriteSessionState("bounded", SessionState{})
	if _, ok := store.ReadSessionState("bounded"); ok {
		t.Fatal("empty session state was retained")
	}
}

func TestStoreDropsOversizedPayloadPerFile(t *testing.T) {
	store := New(t.TempDir())
	imageChip := FileChip{Marker: "[File: shot.png]", Path: "/tmp/shot.png", ID: "image", Name: "shot.png"}
	textChip := FileChip{Marker: "[File: notes.md]", Path: "/tmp/notes.md", ID: "text", Name: "notes.md"}
	store.WriteSessionState("mixed", SessionState{Queue: []QueuedPrompt{{
		Text: "files", Files: []FileChip{imageChip, textChip},
		FrozenFiles: []FrozenFile{
			{ID: "image", Marker: imageChip.Marker, Path: imageChip.Path, Name: imageChip.Name, Kind: "image", Attachment: &Attachment{URL: strings.Repeat("x", maxAttachmentURL+1)}},
			{ID: "text", Marker: textChip.Marker, Path: textChip.Path, Name: textChip.Name, Kind: "text", Content: "kept"},
		},
	}}})
	state, _ := store.ReadSessionState("mixed")
	prompt := state.Queue[0]
	if len(prompt.FrozenFiles) != 1 || prompt.FrozenFiles[0].ID != "text" {
		t.Fatalf("frozen files = %#v", prompt.FrozenFiles)
	}
	if len(prompt.UnfrozenFiles) != 1 || prompt.UnfrozenFiles[0].ID != "image" {
		t.Fatalf("unfrozen files = %#v", prompt.UnfrozenFiles)
	}
}

func TestStoreToleratesMalformedAndUnwritableFiles(t *testing.T) {
	directory := t.TempDir()
	store := New(directory)
	if err := os.WriteFile(store.SessionsPath(), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sessions := store.ReadSessions(); len(sessions) != 0 {
		t.Fatalf("malformed sessions = %#v", sessions)
	}
	blockingPath := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(blockingPath, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	unwritable := New(blockingPath)
	unwritable.WriteDraft("session", Draft{Text: "ignored"})
}

func TestReadResumableSessionsHidesLegacyRowsWithoutNativeState(t *testing.T) {
	directory := t.TempDir()
	store := newWithClock(directory, func() time.Time { return time.UnixMilli(1_700_000_000_000) })
	legacyTitle := "Legacy OpenCode session"
	store.UpsertSession(SessionUpdate{ID: "ses_f47983c00ffeEIK22nsatsQHip", CWD: "/code/midas", Title: &legacyTitle})
	store.UpsertSession(SessionUpdate{ID: "ses_111111111111111111111111", CWD: "/code/midas"})
	nativeTitle := "Named native session"
	store.UpsertSession(SessionUpdate{ID: "ses_222222222222222222222222", CWD: "/code/midas", Title: &nativeTitle})
	store.UpsertSession(SessionUpdate{ID: "legacy-with-transcript", CWD: "/code/midas"})
	store.UpsertSession(SessionUpdate{ID: "ses_333333333333333333333333", CWD: "/code/midas"})
	store.UpsertSession(SessionUpdate{ID: "ses_444444444444444444444444", CWD: "/code/midas"})
	store.UpsertSession(SessionUpdate{ID: "legacy-with-draft", CWD: "/code/midas"})
	store.UpsertSession(SessionUpdate{ID: "current", CWD: "/code/midas"})

	if err := store.WriteTranscript("legacy-with-transcript", []ai.Message{ai.NewUserMessage("hello", time.Unix(1, 0))}); err != nil {
		t.Fatal(err)
	}
	store.WriteDraft("ses_333333333333333333333333", Draft{Text: "unfinished"})
	store.WriteSessionState("ses_444444444444444444444444", SessionState{Queue: []QueuedPrompt{{Text: "queued"}}})
	store.WriteDraft("legacy-with-draft", Draft{Text: "legacy unfinished"})

	listed := store.ReadResumableSessions("current")
	ids := make(map[string]bool, len(listed))
	for _, session := range listed {
		ids[session.ID] = true
	}
	for _, want := range []string{"current", "ses_222222222222222222222222", "ses_333333333333333333333333", "ses_444444444444444444444444", "legacy-with-transcript"} {
		if !ids[want] {
			t.Errorf("missing resumable session %q", want)
		}
	}
	for _, unwanted := range []string{"ses_f47983c00ffeEIK22nsatsQHip", "ses_111111111111111111111111", "legacy-with-draft"} {
		if ids[unwanted] {
			t.Errorf("included empty or legacy session %q", unwanted)
		}
	}
}
