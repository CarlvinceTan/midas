package ai

import (
	"context"
	"errors"
	"sync"
)

var ErrNilTerminalResult = errors.New("ai: terminal event has no result")

// EventStream is an unbounded, single-logical-consumer event queue. Terminal
// events remain observable through Next and also resolve Result. End without a
// result ends iteration but intentionally leaves Result unresolved.
type EventStream[T, R any] struct {
	mu sync.Mutex

	// queue is a ring-free FIFO: head advances on read and the slice is reused
	// once drained, so streaming thousands of deltas does not keep allocating.
	queue []T
	head  int
	done  bool

	changed chan struct{}

	resultReady   bool
	result        R
	resultChanged chan struct{}

	complete func(T) (R, bool)
}

func NewEventStream[T, R any](complete func(T) (R, bool)) *EventStream[T, R] {
	return &EventStream[T, R]{
		changed:       make(chan struct{}),
		resultChanged: make(chan struct{}),
		complete:      complete,
	}
}

// Push appends an event unless the stream has already ended. A terminal event
// ends the stream after being enqueued and resolves Result.
func (s *EventStream[T, R]) Push(event T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}

	s.queue = append(s.queue, event)
	if result, complete := s.complete(event); complete {
		s.done = true
		s.result = result
		s.resultReady = true
		s.signalResultLocked()
	}
	s.signalChangedLocked()
}

// End stops iteration. Passing a result also resolves Result. Passing nil keeps
// Result pending when End is called without a result.
func (s *EventStream[T, R]) End(result *R) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	if result != nil && !s.resultReady {
		s.result = *result
		s.resultReady = true
		s.signalResultLocked()
	}
	s.signalChangedLocked()
}

// Next returns the next queued event. ok is false after all queued events have
// been consumed and the stream has ended.
func (s *EventStream[T, R]) Next(ctx context.Context) (event T, ok bool, err error) {
	for {
		s.mu.Lock()
		if s.head < len(s.queue) {
			event = s.queue[s.head]
			var zero T
			s.queue[s.head] = zero
			s.head++
			if s.head == len(s.queue) {
				s.queue = s.queue[:0]
				s.head = 0
			}
			s.mu.Unlock()
			return event, true, nil
		}
		if s.done {
			s.mu.Unlock()
			return event, false, nil
		}
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		changed := s.changed
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return event, false, ctx.Err()
		case <-changed:
		}
	}
}

// Result waits for the terminal result. Provider failures are represented by
// the returned AssistantMessage's stop reason, not as a Go error; errors here
// are reserved for cancellation or malformed terminal events.
func (s *EventStream[T, R]) Result(ctx context.Context) (result R, err error) {
	for {
		s.mu.Lock()
		if s.resultReady {
			result = s.result
			s.mu.Unlock()
			return result, nil
		}
		if s.resultChanged == nil {
			s.resultChanged = make(chan struct{})
		}
		changed := s.resultChanged
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-changed:
		}
	}
}

// signalChangedLocked wakes a waiting consumer. The channel only exists while
// someone is waiting, so a producer that outruns its consumer allocates nothing
// to signal.
func (s *EventStream[T, R]) signalChangedLocked() {
	if s.changed == nil {
		return
	}
	close(s.changed)
	s.changed = nil
}

func (s *EventStream[T, R]) signalResultLocked() {
	if s.resultChanged == nil {
		return
	}
	close(s.resultChanged)
	s.resultChanged = nil
}

type AssistantStream struct {
	*EventStream[AssistantEvent, AssistantMessage]
}

func NewAssistantStream() *AssistantStream {
	return &AssistantStream{EventStream: NewEventStream(func(event AssistantEvent) (AssistantMessage, bool) {
		message, terminal := event.terminal()
		if !terminal {
			return AssistantMessage{}, false
		}
		if message == nil {
			return AssistantMessage{Role: RoleAssistant, StopReason: StopError, ErrorMessage: ErrNilTerminalResult.Error()}, true
		}
		return *message, true
	})}
}
