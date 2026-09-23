package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBroker(t *testing.T) *Broker {
	t.Helper()
	broker, err := NewBroker(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	return broker
}

func TestSendWakesAParkedRecipient(t *testing.T) {
	broker := testBroker(t)
	if err := broker.Register("alice"); err != nil {
		t.Fatal(err)
	}
	got := make(chan Delivery, 1)
	go func() {
		delivery, err := broker.take(context.Background(), "alice")
		if err != nil {
			return
		}
		got <- delivery
	}()
	// Give the reader a moment to park, then send: delivery must be event-driven,
	// not a poll that eventually notices.
	time.Sleep(20 * time.Millisecond)
	if _, err := broker.Send("bob", "alice", "team", "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case delivery := <-got:
		if delivery.Text != "hello" || delivery.From != "bob" || delivery.Kind != KindTell {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("a parked recipient was not woken by a send")
	}
}

func TestAskWaitsForTheReplyAndTellDoesNot(t *testing.T) {
	broker := testBroker(t)
	_ = broker.Register("alice")
	_ = broker.Register("bob")

	answers := make(chan string, 1)
	go func() {
		delivery, err := broker.take(context.Background(), "bob")
		if err != nil {
			return
		}
		_ = delivery.Reply("from bob")
		answers <- delivery.Text
	}()
	reply, err := broker.Ask(context.Background(), "alice", "bob", "team", "what is the plan?")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "from bob" {
		t.Fatalf("reply = %q", reply)
	}
	if question := <-answers; question != "what is the plan?" {
		t.Fatalf("bob saw %q", question)
	}
	// An ask that is never answered ends with the caller's context, not a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := broker.Ask(ctx, "alice", "bob", "team", "anyone home?"); err == nil {
		t.Fatal("an unanswered ask returned without an error")
	}
	// A tell has no reply channel at all.
	delivery := Delivery{}
	if err := delivery.Reply("nobody asked"); err == nil {
		t.Fatal("a tell accepted a reply")
	}
}

func TestInboxesArePersistentAndBounded(t *testing.T) {
	dir := t.TempDir()
	broker, err := NewBroker(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = broker.Register("worker")
	for index := 0; index < 5; index++ {
		if _, err := broker.Send("user", "worker", "", "job"); err != nil {
			t.Fatal(err)
		}
	}
	broker.Close()

	// A restart loads what was still queued, so a long-lived agent loses no work.
	reopened, err := NewBroker(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Register("worker"); err != nil {
		t.Fatal(err)
	}
	queued, err := reopened.History("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("ungrouped history = %#v", queued)
	}
	delivery, err := reopened.take(context.Background(), "worker")
	if err != nil || delivery.Text != "job" {
		t.Fatalf("reopened = %#v, %v", delivery, err)
	}

	// Overflow drops the oldest rather than growing without bound.
	saturate := testBroker(t)
	_ = saturate.Register("slow")
	for index := 0; index < inboxCapacity+10; index++ {
		if _, err := saturate.Send("user", "slow", "", "job"); err != nil {
			t.Fatal(err)
		}
	}
	saturate.mu.Lock()
	box := saturate.boxByID["slow"]
	saturate.mu.Unlock()
	if queued := len(box.snapshot()); queued != inboxCapacity {
		t.Fatalf("queue = %d, want the %d cap", queued, inboxCapacity)
	}
}

func TestGroupHistoryAndBroadcast(t *testing.T) {
	broker := testBroker(t)
	for _, address := range []string{"alice", "bob", "carol"} {
		_ = broker.Register(address)
	}
	if _, err := broker.Broadcast("alice", "standup", "starting"); err != nil {
		t.Fatal(err)
	}
	// Everyone but the sender: alice, bob, carol and the user link.
	history, err := broker.History("standup", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("broadcast reached %d addresses", len(history))
	}
	if history[0].To == "alice" {
		t.Fatal("the sender received its own broadcast")
	}
	// Dropping a queue entry keeps the envelope out of the file too, so a restart
	// does not replay work that was already handled.
	delivery, err := broker.take(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Text != "starting" {
		t.Fatalf("delivery = %#v", delivery)
	}
	again, err := broker.History("standup", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 3 {
		t.Fatalf("history changed when a queue entry was taken: %#v", again)
	}
	if !strings.Contains(again[0].Text, "starting") {
		t.Fatalf("history = %#v", again)
	}
}

// TestHistoryReportsAStreamItCannotFinish: a group file with a line longer than
// the framing holds is an error, not a short history that reads as a complete one.
func TestHistoryReportsAStreamItCannotFinish(t *testing.T) {
	directory := t.TempDir()
	broker, err := NewBroker(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()

	// The scanner's ceiling is four megabytes, so a five megabyte line cannot be
	// read: what matters is that the caller is told rather than handed a page that
	// looks complete.
	groups := filepath.Join(directory, "groups")
	if err := os.MkdirAll(groups, 0o700); err != nil {
		t.Fatal(err)
	}
	huge := `{"from":"a","to":"b","text":"` + strings.Repeat("x", 5<<20) + `"}`
	if err := os.WriteFile(filepath.Join(groups, "big.jsonl"), []byte(huge+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.History("big", 10); err == nil {
		t.Fatal("an unreadable group file returned a history")
	}
	// A group that does not exist is still an empty history: not every absence is an
	// error.
	if messages, err := broker.History("never-used", 10); err != nil || len(messages) != 0 {
		t.Fatalf("a missing group = %#v, %v", messages, err)
	}
}
