package tui

import (
	"bytes"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// CancelFunc cancels a scheduled callback when it has not fired yet.
type CancelFunc func()

// Scheduler abstracts next-turn and delayed work for deterministic TUI tests.
type Scheduler interface {
	Now() time.Duration
	Defer(callback func())
	AfterFunc(delay time.Duration, callback func()) CancelFunc
}

// loopScheduler runs every callback on one goroutine. Component state is
// single-threaded by contract, but in Go terminal input arrives on the terminal
// reader goroutine while frames and timers run on scheduler goroutines. A loop
// restores the contract: input is posted to it, and renders and timers run on it,
// so an edit and a frame never touch the same component at once. Without it the
// race detector reports concurrent reads and writes of editor and overlay state,
// and a stale layout can panic a slice.
type loopScheduler struct {
	started time.Time

	mu      sync.Mutex
	queue   []func()
	wake    chan struct{}
	stopped bool
	idle    chan struct{}
	running atomic.Bool
	// owner is the loop goroutine's ID, so a shutdown requested from inside a
	// callback does not wait for itself.
	owner atomic.Uint64
}

func newLoopScheduler() *loopScheduler {
	scheduler := &loopScheduler{started: time.Now(), wake: make(chan struct{}, 1), idle: make(chan struct{})}
	go scheduler.run()
	return scheduler
}

func (s *loopScheduler) Now() time.Duration { return time.Since(s.started) }

// Defer queues work for the loop. It never blocks, so it is safe to call from a
// callback that is already running on the loop.
func (s *loopScheduler) Defer(callback func()) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, callback)
	s.mu.Unlock()
	s.signal()
}

// AfterFunc queues callback after delay. Cancelling a fired timer is harmless.
func (s *loopScheduler) AfterFunc(delay time.Duration, callback func()) CancelFunc {
	timer := time.AfterFunc(delay, func() { s.Defer(callback) })
	return func() { timer.Stop() }
}

// StopLoop ends the loop and drops queued work. It waits for a callback that is
// already running, so a caller on another goroutine can tear down state that
// callback touches; a caller that is itself a callback must not wait for the loop
// it is blocking, so running callbacks return immediately.
func (s *loopScheduler) StopLoop() {
	s.mu.Lock()
	s.stopped = true
	s.queue = nil
	s.mu.Unlock()
	s.signal()
	if s.running.Load() && s.owner.Load() == goroutineID() {
		// Called from the loop's own callback: the teardown is already on the right
		// goroutine and waiting for the loop would deadlock.
		return
	}
	select {
	case <-s.idle:
	case <-time.After(2 * time.Second):
	}
}

// goroutineID identifies the calling goroutine, which the loop needs to tell a
// shutdown from inside its own callback from one requested elsewhere.
func goroutineID() uint64 {
	var buffer [64]byte
	n := runtime.Stack(buffer[:], false)
	fields := bytes.Fields(buffer[:n])
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func (s *loopScheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *loopScheduler) run() {
	defer close(s.idle)
	s.owner.Store(goroutineID())
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			select {
			case <-s.wake:
			case <-time.After(50 * time.Millisecond):
				// A stop can arrive while the loop is parked on an empty queue; the
				// wake channel covers that, and the poll is a backstop.
			}
			continue
		}
		callback := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.running.Store(true)
		callback()
		s.running.Store(false)
	}
}

// inputPoster is implemented by schedulers that run everything on one goroutine.
// Terminal input is posted to those, so an edit and a frame never touch the same
// component at once. A test scheduler is deliberately not one of them: its tests
// drive input synchronously and expect that.
type inputPoster interface {
	PostInput(callback func())
}

// PostInput queues terminal input for the loop.
func (s *loopScheduler) PostInput(callback func()) { s.Defer(callback) }

// stopLoop stops a scheduler that owns a goroutine, if it has one.
func stopLoop(scheduler Scheduler) {
	if loop, ok := scheduler.(interface{ StopLoop() }); ok {
		loop.StopLoop()
	}
}
