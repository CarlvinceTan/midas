package voice

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	outR     *io.PipeReader
	outW     *io.PipeWriter
	mu       sync.Mutex
	input    bytes.Buffer
	exited   chan struct{}
	exitOnce sync.Once
	killed   bool
}

func newFakeProcess() *fakeProcess {
	reader, writer := io.Pipe()
	return &fakeProcess{outR: reader, outW: writer, exited: make(chan struct{})}
}
func (p *fakeProcess) Stdout() io.Reader { return p.outR }
func (p *fakeProcess) Stdin() io.Writer  { return lockedWriter{p} }
func (p *fakeProcess) PID() int          { return 123 }
func (p *fakeProcess) Wait() error       { <-p.exited; return nil }
func (p *fakeProcess) KillTree() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.exit()
	return nil
}
func (p *fakeProcess) exit()            { p.exitOnce.Do(func() { _ = p.outW.Close(); close(p.exited) }) }
func (p *fakeProcess) emit(line string) { _, _ = io.WriteString(p.outW, line+"\n") }
func (p *fakeProcess) commands() string { p.mu.Lock(); defer p.mu.Unlock(); return p.input.String() }

type lockedWriter struct{ process *fakeProcess }

func (w lockedWriter) Write(data []byte) (int, error) {
	w.process.mu.Lock()
	defer w.process.mu.Unlock()
	return w.process.input.Write(data)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}

func TestParseLineAndComposeText(t *testing.T) {
	for _, line := range []string{"", "junk", "null", `{"type":"unknown"}`, `{"type":2}`} {
		if _, ok := ParseLine(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
	event, ok := ParseLine(` {"type":"partial","text":"hello","extra":1} `)
	if !ok || event.Type != "partial" || event.Text != "hello" {
		t.Fatalf("event = %#v, %v", event, ok)
	}
	if got := ComposeText("base ", "done ", "now"); got != "base done now" {
		t.Fatalf("compose = %q", got)
	}
	if got := ComposeText("base", "done", "now"); got != "base done now" {
		t.Fatalf("compose without spaces = %q", got)
	}
}

func TestControllerStartsOnDemandAndKeepsTextInSync(t *testing.T) {
	process := newFakeProcess()
	var mu sync.Mutex
	var texts [][2]string
	var ready, stops, spawns int
	var errorsSeen []string
	controller := New(Options{Command: "fake", Spawn: func(string, []string) (Process, error) {
		mu.Lock()
		spawns++
		mu.Unlock()
		return process, nil
	}, OnText: func(committed, partial string) {
		mu.Lock()
		texts = append(texts, [2]string{committed, partial})
		mu.Unlock()
	}, OnReady: func() { mu.Lock(); ready++; mu.Unlock() }, OnStop: func() { mu.Lock(); stops++; mu.Unlock() }, OnError: func(message string) { mu.Lock(); errorsSeen = append(errorsSeen, message); mu.Unlock() }})
	// Nothing loads until dictation starts: no spawn, no helper commands.
	if spawned := func() int { mu.Lock(); defer mu.Unlock(); return spawns }(); spawned != 0 || process.commands() != "" {
		t.Fatalf("idle controller spawned=%d commands=%q", spawned, process.commands())
	}
	controller.Listen()
	if spawned := func() int { mu.Lock(); defer mu.Unlock(); return spawns }(); spawned != 1 {
		t.Fatalf("listen spawned=%d, want 1", spawned)
	}
	waitFor(t, func() bool { return bytes.Contains([]byte(process.commands()), []byte(`{"type":"listen"}`)) })
	process.emit(`{"type":"ready"}`)
	process.emit(`{"type":"ready"}`)
	waitFor(t, controller.Ready)
	process.emit(`{"type":"partial","text":"hel"}`)
	process.emit(`{"type":"partial","text":""}`)
	process.emit(`{"type":"final","text":"hello "}`)
	process.emit(`{"type":"partial","text":"world"}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(texts) >= 3 })
	mu.Lock()
	last := texts[len(texts)-1]
	readyCount := ready
	mu.Unlock()
	if last != [2]string{"hello", "world"} || readyCount != 1 {
		t.Fatalf("last=%#v ready=%d", last, readyCount)
	}
	controller.Pause()
	before := len(texts)
	process.emit(`{"type":"final","text":"ignored"}`)
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	after := len(texts)
	mu.Unlock()
	if before != after {
		t.Fatalf("paused final emitted text")
	}
	controller.Listen()
	process.emit(`{"type":"partial","text":"new"}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(texts) > after })
	mu.Lock()
	last = texts[len(texts)-1]
	mu.Unlock()
	if last != [2]string{"", "new"} {
		t.Fatalf("relisten text = %#v", last)
	}
	process.emit(`{"type":"error","message":"failed"}`)
	waitFor(t, func() bool { process.mu.Lock(); defer process.mu.Unlock(); return process.killed })
	mu.Lock()
	defer mu.Unlock()
	if len(errorsSeen) != 1 || errorsSeen[0] != "failed" || stops != 0 {
		t.Fatalf("errors=%#v stops=%d", errorsSeen, stops)
	}
}

func TestControllerUnexpectedExitCanRestart(t *testing.T) {
	first, second := newFakeProcess(), newFakeProcess()
	spawned := 0
	stopped := make(chan struct{}, 1)
	controller := New(Options{Command: "fake", Spawn: func(string, []string) (Process, error) {
		spawned++
		if spawned == 1 {
			return first, nil
		}
		return second, nil
	}, OnText: func(string, string) {}, OnError: func(string) {}, OnStop: func() { stopped <- struct{}{} }})
	controller.Listen()
	first.emit(`{"type":"partial","text":"kept"}`)
	first.exit()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("missing stop callback")
	}
	controller.Listen()
	if spawned != 2 {
		t.Fatalf("spawned = %d", spawned)
	}
	controller.Stop()
}

func TestControllerPauseAndWaitCommitsFinalText(t *testing.T) {
	process := newFakeProcess()
	var mu sync.Mutex
	var last [2]string
	controller := New(Options{Command: "fake", Spawn: func(string, []string) (Process, error) { return process, nil }, OnText: func(committed, partial string) {
		mu.Lock()
		last = [2]string{committed, partial}
		mu.Unlock()
	}, OnError: func(string) {}})
	controller.Listen()
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if bytes.Contains([]byte(process.commands()), []byte(`{"type":"pause"}`)) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		process.emit(`{"type":"final","text":"short phrase"}`)
		process.emit(`{"type":"paused"}`)
	}()
	controller.PauseAndWait(time.Second)
	mu.Lock()
	got := last
	mu.Unlock()
	if got != [2]string{"short phrase", ""} {
		t.Fatalf("final text = %#v", got)
	}
	controller.Stop()
}

func TestControllerStopReleasesTheHelperForTheNextSession(t *testing.T) {
	first, second := newFakeProcess(), newFakeProcess()
	spawned := 0
	controller := New(Options{Command: "fake", Spawn: func(string, []string) (Process, error) {
		spawned++
		if spawned == 1 {
			return first, nil
		}
		return second, nil
	}, OnText: func(string, string) {}, OnError: func(string) {}, OnStop: func() {}})
	controller.Listen()
	first.emit(`{"type":"ready"}`)
	waitFor(t, controller.Ready)

	// Ending dictation releases the helper so the speech model stops occupying
	// memory; the controller is cold until the next session asks for it.
	controller.Stop()
	if controller.Ready() {
		t.Fatal("stop left the controller ready")
	}
	first.mu.Lock()
	killed := first.killed
	first.mu.Unlock()
	if !killed {
		t.Fatal("stop did not release the helper")
	}

	controller.Listen()
	if spawned != 2 {
		t.Fatalf("spawned = %d after stop, want a fresh helper", spawned)
	}
	controller.Stop()
}
