package ai

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAssistantStreamDeliversTerminalEventAndResult(t *testing.T) {
	t.Parallel()

	stream := NewAssistantStream()
	message := AssistantMessage{Role: RoleAssistant, StopReason: StopComplete, Model: "test"}
	stream.Push(AssistantEvent{Type: EventStart, Partial: &message})
	stream.Push(AssistantEvent{Type: EventDone, Reason: StopComplete, Message: &message})

	first, ok, err := stream.Next(context.Background())
	if err != nil || !ok || first.Type != EventStart {
		t.Fatalf("first event = (%q, %v, %v), want start", first.Type, ok, err)
	}
	last, ok, err := stream.Next(context.Background())
	if err != nil || !ok || last.Type != EventDone {
		t.Fatalf("terminal event = (%q, %v, %v), want done", last.Type, ok, err)
	}
	if _, ok, err := stream.Next(context.Background()); err != nil || ok {
		t.Fatalf("after terminal = (ok %v, err %v), want exhausted", ok, err)
	}

	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "test" || result.StopReason != StopComplete {
		t.Fatalf("result = %#v", result)
	}
}

func TestAssistantStreamErrorResolvesToErrorMessage(t *testing.T) {
	t.Parallel()

	stream := NewAssistantStream()
	message := AssistantMessage{Role: RoleAssistant, StopReason: StopError, ErrorMessage: "provider failed"}
	stream.Push(AssistantEvent{Type: EventError, Reason: StopError, Error: &message})

	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorMessage != "provider failed" || result.StopReason != StopError {
		t.Fatalf("result = %#v", result)
	}
}

func TestEventStreamIgnoresPushAfterTerminal(t *testing.T) {
	t.Parallel()

	stream := NewEventStream(func(value int) (int, bool) { return value, value == 2 })
	stream.Push(1)
	stream.Push(2)
	stream.Push(3)

	for _, want := range []int{1, 2} {
		got, ok, err := stream.Next(context.Background())
		if err != nil || !ok || got != want {
			t.Fatalf("Next() = (%d, %v, %v), want (%d, true, nil)", got, ok, err, want)
		}
	}
	if _, ok, err := stream.Next(context.Background()); err != nil || ok {
		t.Fatalf("Next() after terminal = (ok %v, err %v)", ok, err)
	}
}

func TestEventStreamEndDrainsQueue(t *testing.T) {
	t.Parallel()

	stream := NewEventStream(func(value int) (int, bool) { return 0, false })
	stream.Push(7)
	result := 42
	stream.End(&result)

	got, ok, err := stream.Next(context.Background())
	if err != nil || !ok || got != 7 {
		t.Fatalf("Next() = (%d, %v, %v)", got, ok, err)
	}
	if _, ok, err := stream.Next(context.Background()); err != nil || ok {
		t.Fatalf("Next() after drain = (ok %v, err %v)", ok, err)
	}
	if got, err := stream.Result(context.Background()); err != nil || got != result {
		t.Fatalf("Result() = (%d, %v)", got, err)
	}
}

func TestEventStreamEndWithoutResultLeavesResultPending(t *testing.T) {
	t.Parallel()

	stream := NewEventStream(func(value int) (int, bool) { return 0, false })
	stream.End(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := stream.Result(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Result() error = %v, want deadline exceeded", err)
	}
}

func TestEventStreamLaterEndCanSupplyResult(t *testing.T) {
	t.Parallel()

	stream := NewEventStream(func(value int) (int, bool) { return 0, false })
	stream.End(nil)
	result := 23
	stream.End(&result)

	got, err := stream.Result(context.Background())
	if err != nil || got != result {
		t.Fatalf("Result() = (%d, %v), want (%d, nil)", got, err, result)
	}
}

func TestEventStreamWakesWaitingConsumer(t *testing.T) {
	t.Parallel()

	stream := NewEventStream(func(value int) (int, bool) { return value, value == 9 })
	got := make(chan int, 1)
	go func() {
		value, ok, err := stream.Next(context.Background())
		if err == nil && ok {
			got <- value
		}
	}()

	stream.Push(9)
	select {
	case value := <-got:
		if value != 9 {
			t.Fatalf("value = %d, want 9", value)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting consumer was not woken")
	}
}
