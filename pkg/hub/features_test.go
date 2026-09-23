package hub

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openTestStore is a store in a temporary directory, which is what every feature
// test starts from.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestEditsReplaceTextAndSurviveARestart: a corrected message keeps its identity
// and its place in history, and the correction is what a reload sees.
func TestEditsReplaceTextAndSurviveARestart(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	store, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Message{ID: "m1", Account: "personal", Thread: "dm:alice", Text: "deploy is gren", Kind: KindText}); err != nil {
		t.Fatal(err)
	}
	updated, ok := store.Edit("personal", "m1", "deploy is green", 1234)
	if !ok {
		t.Fatal("the message was not found for editing")
	}
	if updated.Text != "deploy is green" || !updated.Edited || updated.Timestamp != 1234 {
		t.Fatalf("edited = %#v", updated)
	}
	if _, ok := store.Edit("personal", "missing", "x", 0); ok {
		t.Fatal("editing an unknown message succeeded")
	}

	reopened, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	messages := reopened.Messages("personal", "dm:alice", 10)
	if len(messages) != 1 || messages[0].Text != "deploy is green" || !messages[0].Edited {
		t.Fatalf("messages after restart = %#v", messages)
	}
}

// TestDeletesClearContentAndKeepTheRecord: a deleted message leaves a tombstone so
// a client's history does not shift, but nothing of what it said remains.
func TestDeletesClearContentAndKeepTheRecord(t *testing.T) {
	store := openTestStore(t)
	if err := store.Append(Message{
		ID: "m1", Account: "personal", Thread: "dm:alice", Text: "the password is hunter2",
		Kind: KindImage, Media: []Media{{Kind: KindImage, Path: "/tmp/photo.png", Size: 12}},
	}); err != nil {
		t.Fatal(err)
	}
	deleted, ok := store.Delete("personal", "m1")
	if !ok {
		t.Fatal("the message was not found for deletion")
	}
	if !deleted.Deleted || deleted.Text != "" || len(deleted.Media) != 0 {
		t.Fatalf("deleted = %#v", deleted)
	}
	messages := store.Messages("personal", "dm:alice", 10)
	if len(messages) != 1 || messages[0].Text != "" || !messages[0].Deleted {
		t.Fatalf("messages = %#v", messages)
	}
}

// TestReactionsAreOnePerSender: the second reaction from the same person replaces
// the first, a removal takes it away, and other people's reactions stay.
func TestReactionsAreOnePerSender(t *testing.T) {
	store := openTestStore(t)
	if err := store.Append(Message{ID: "m1", Account: "personal", Thread: "dm:alice", Text: "ship it"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.React("personal", "m1", Reaction{Emoji: "👍", Sender: "alice"}); !ok {
		t.Fatal("reacting failed")
	}
	if _, ok := store.React("personal", "m1", Reaction{Emoji: "🎉", Sender: "bob"}); !ok {
		t.Fatal("reacting failed")
	}
	updated, _ := store.React("personal", "m1", Reaction{Emoji: "❤️", Sender: "alice"})
	bySender := map[string]string{}
	for _, reaction := range updated.Reactions {
		bySender[reaction.Sender] = reaction.Emoji
	}
	if len(updated.Reactions) != 2 || bySender["alice"] != "❤️" || bySender["bob"] != "🎉" {
		t.Fatalf("reactions = %#v", updated.Reactions)
	}
	removed, _ := store.React("personal", "m1", Reaction{Sender: "alice", Removed: true})
	if len(removed.Reactions) != 1 || removed.Reactions[0].Sender != "bob" {
		t.Fatalf("reactions after removal = %#v", removed.Reactions)
	}
	if _, ok := store.React("personal", "missing", Reaction{Emoji: "👍"}); ok {
		t.Fatal("reacting to an unknown message succeeded")
	}
}

// TestThreadDescriptionsCarryCommunitiesAndEmptyChannels: what the bridge says
// about a conversation is what a client lists, including a channel that has never
// carried a message.
func TestThreadDescriptionsCarryCommunitiesAndEmptyChannels(t *testing.T) {
	store := openTestStore(t)
	space := Thread{
		ID: "space:acme", Account: "personal", Title: "Acme", Kind: ThreadCommunity,
		Participants: []string{"me", "alice"},
	}
	muted := true
	channel := Thread{
		ID: "space:acme/general", Account: "personal", Title: "#general", Kind: ThreadChannel,
		Parent: "space:acme", Participants: []string{"alice"}, Muted: &muted,
	}
	if err := store.SetThread(space); err != nil {
		t.Fatal(err)
	}
	if err := store.SetThread(channel); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Message{ID: "m1", Account: "personal", Thread: "space:acme/general", Text: "hi", Incoming: true, Timestamp: 99}); err != nil {
		t.Fatal(err)
	}
	threads := store.Threads("personal")
	byID := map[string]Thread{}
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	if got := byID["space:acme"]; got.Kind != ThreadCommunity || got.LastMessage != 0 {
		t.Fatalf("community = %#v", got)
	}
	if got := byID["space:acme/general"]; got.Kind != ThreadChannel || got.Parent != "space:acme" || got.Muted == nil || !*got.Muted || got.LastMessage != 99 || got.Unread != 1 {
		t.Fatalf("channel = %#v", got)
	}
	// A later report that says nothing about the flags leaves them alone, and one
	// that says false clears them: "not reported" is not "false".
	if err := store.SetThread(Thread{ID: "space:acme/general", Account: "personal", Title: "#general"}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range store.Threads("personal") {
		if thread.ID == "space:acme/general" && (thread.Muted == nil || !*thread.Muted) {
			t.Fatalf("a report without flags cleared them: %#v", thread)
		}
	}
	unmuted := false
	if err := store.SetThread(Thread{ID: "space:acme/general", Account: "personal", Muted: &unmuted}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range store.Threads("personal") {
		if thread.ID == "space:acme/general" && (thread.Muted == nil || *thread.Muted) {
			t.Fatalf("an explicit false did not clear the flag: %#v", thread)
		}
	}
	// A description survives a restart, and a later message does not erase it.
	reopened, err := OpenStore(filepath.Dir(store.threadsPath))
	if err != nil {
		t.Fatal(err)
	}
	restored := map[string]Thread{}
	for _, thread := range reopened.Threads("personal") {
		restored[thread.ID] = thread
	}
	if got := restored["space:acme/general"]; got.Parent != "space:acme" || got.Kind != ThreadChannel {
		t.Fatalf("channel after restart = %#v", got)
	}
}

// TestAStreamThatCannotBeReadIsReportedAndStopped: a bridge whose stdout fails, or
// that writes a line the framing cannot hold, is not a bridge that exited cleanly.
// The reason has to reach the caller, and the process behind the stream has to go.
func TestAStreamThatCannotBeReadIsReportedAndStopped(t *testing.T) {
	killed := make(chan struct{}, 1)
	reader := io.MultiReader(strings.NewReader(""), errorReader{err: errors.New("device error")})
	process := newBridgeProcess("broken", nopWriteCloser{io.Discard}, reader, func() {
		select {
		case killed <- struct{}{}:
		default:
		}
	})
	select {
	case <-process.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process never noticed its stream had ended")
	}
	failure := process.Failure()
	if failure == nil || !strings.Contains(failure.Error(), "device error") {
		t.Fatalf("failure = %v", failure)
	}
	if err := process.Call(context.Background(), MethodHello, nil, nil); err == nil || !strings.Contains(err.Error(), "device error") {
		t.Fatalf("a call after the failure = %v", err)
	}
	select {
	case <-killed:
	case <-time.After(time.Second):
		t.Fatal("a process whose stream cannot be read was left running")
	}

	// A line longer than the framing allows is the same kind of failure.
	huge := newBridgeProcess("flood", nopWriteCloser{io.Discard}, strings.NewReader(strings.Repeat("x", 5<<20)+"\n"), nil)
	select {
	case <-huge.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the process never noticed the oversized line")
	}
	if failure := huge.Failure(); failure == nil || !strings.Contains(failure.Error(), "read from bridge flood") {
		t.Fatalf("oversized line failure = %v", failure)
	}
}

// errorReader fails every read, which is what a broken pipe looks like here.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

// nopWriteCloser is a writer with a trivial Close.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
