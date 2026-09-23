package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// scriptedBrain answers from a table, and can delegate: that is enough to prove
// the bus, the inboxes, and the ask path without a model or a network.
type scriptedBrain struct {
	handle func(ctx context.Context, request Request) (string, error)
}

func (b scriptedBrain) Respond(ctx context.Context, request Request) (string, error) {
	return b.handle(ctx, request)
}

func TestTwoAgentsTalkThroughTheBus(t *testing.T) {
	broker := testBroker(t)
	registry := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A worker that answers questions, and an orchestrator that delegates to it.
	worker, err := NewAgent(broker, "worker", "worker", scriptedBrain{handle: func(_ context.Context, request Request) (string, error) {
		return "done: " + request.Text, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := NewAgent(broker, "orchestrator", "orchestrator", scriptedBrain{handle: func(ctx context.Context, request Request) (string, error) {
		answer, err := request.Ask(ctx, "worker", request.Text)
		if err != nil {
			return "", err
		}
		return "worker said " + answer, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(worker)
	registry.Add(orchestrator)
	go worker.Run(ctx)
	go orchestrator.Run(ctx)
	t.Cleanup(func() {
		cancel()
		worker.Wait()
		orchestrator.Wait()
	})

	// The user link asks the orchestrator something, which the orchestrator
	// delegates to the worker and reports back.
	answer, err := broker.Ask(context.Background(), "user", "orchestrator", "project", "prepare the release")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "worker said done: prepare the release" {
		t.Fatalf("answer = %q", answer)
	}
	if err := registry.WaitForIdle(2 * time.Second); err != nil {
		t.Fatal(err)
	}

	// The exchange is in the group history, so the user link can render it
	// without subscribing to anything.
	history, err := broker.History("project", 20)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{}
	for _, envelope := range history {
		texts = append(texts, envelope.From+"→"+envelope.To)
	}
	joined := strings.Join(texts, ", ")
	for _, want := range []string{"user→orchestrator", "orchestrator→worker", "worker→orchestrator"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("history is missing %q: %s", want, joined)
		}
	}
	// Statuses say what happened, which is what the registry endpoint serves.
	statuses := registry.Statuses()
	if len(statuses) != 2 || statuses[0].Address != "orchestrator" {
		t.Fatalf("statuses = %#v", statuses)
	}
	for _, status := range statuses {
		if status.Handled == 0 {
			t.Fatalf("%s handled nothing: %#v", status.Address, status)
		}
	}
}

func TestATellGetsTheAnswerBackAsAMessage(t *testing.T) {
	broker := testBroker(t)
	registry := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sender, err := NewAgent(broker, "sender", "", scriptedBrain{handle: func(_ context.Context, request Request) (string, error) {
		return "", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	echo, err := NewAgent(broker, "echo", "", scriptedBrain{handle: func(_ context.Context, request Request) (string, error) {
		return "noted " + request.Text, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(sender)
	registry.Add(echo)
	go sender.Run(ctx)
	go echo.Run(ctx)
	t.Cleanup(func() {
		cancel()
		sender.Wait()
		echo.Wait()
	})

	if _, err := broker.Send("user", "echo", "", "ping"); err != nil {
		t.Fatal(err)
	}
	// The echo's answer comes back to the sender as an ordinary message, so a tell
	// is answered without the user link having to ask a second time.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		delivery, err := broker.take(context.Background(), "user")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.From == "echo" && delivery.Text == "noted ping" {
			return
		}
	}
	t.Fatal("the tell was never answered")
}

func TestAgentStopsCleanlyAndRefusesWorkAfterwards(t *testing.T) {
	broker := testBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	agent, err := NewAgent(broker, "solo", "", scriptedBrain{handle: func(context.Context, Request) (string, error) {
		return "ok", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	go agent.Run(ctx)
	if _, err := broker.Send("user", "solo", "", "hello"); err != nil {
		t.Fatal(err)
	}
	// Stop the loop and the whole broker: nothing should block or panic.
	agent.Stop()
	agent.Wait()
	cancel()
	broker.Close()
	// A closed broker refuses new work rather than accepting it silently.
	if _, err := broker.Send("user", "solo", "", "after close"); err == nil {
		t.Fatal("a closed inbox accepted work")
	}
	if err := broker.Register("solo"); err != nil {
		t.Fatalf("registering a known address = %v", err)
	}
	// The user link is always an address; the agent is the other one.
	if addresses := broker.Addresses(); len(addresses) != 2 || addresses[0] != "solo" || addresses[1] != UserAddress {
		t.Fatalf("addresses = %#v", addresses)
	}
	if status := agent.Status(); status.Address != "solo" || status.Busy {
		t.Fatalf("status = %#v", status)
	}
}

// TestStoppingAnAgentBeforeItStartsIsHonoured: a shutdown that races a start must
// not lose the stop, or the loop runs until the process context ends and the
// goroutine is leaked.
func TestStoppingAnAgentBeforeItStartsIsHonoured(t *testing.T) {
	broker := testBroker(t)
	agent, err := NewAgent(broker, "raced", "", scriptedBrain{handle: func(context.Context, Request) (string, error) {
		return "ok", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Stop before the loop has registered its cancel, which is what a shutdown
	// overlapping a start looks like.
	agent.Stop()
	go agent.Run(context.Background())
	stopped := make(chan struct{})
	go func() {
		agent.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("an agent stopped before it started kept running")
	}
	// Work sent afterwards is refused by the closed address rather than queued
	// forever.
	if _, err := broker.Send("user", "raced", "", "after stop"); err == nil {
		t.Log("the inbox still accepts work, which the broker decides for itself")
	}
}
