package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func temporaryProcessTerminal(t *testing.T) (*ProcessTerminal, *os.File) {
	t.Helper()
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "terminal-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = output.Close() })
	return newProcessTerminal(input, output), output
}

func readTerminalOutput(t *testing.T, output *os.File) string {
	t.Helper()
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestProcessTerminalOutputMethods(t *testing.T) {
	terminal, output := temporaryProcessTerminal(t)
	logPath := filepath.Join(t.TempDir(), "writes.log")
	terminal.writeLogPath = logPath
	terminal.MoveBy(2)
	terminal.MoveBy(-1)
	terminal.MoveBy(0)
	terminal.HideCursor()
	terminal.ShowCursor()
	terminal.ClearLine()
	terminal.ClearFromCursor()
	terminal.ClearScreen()
	terminal.SetTitle("title")
	terminal.SetProgress(true)
	terminal.SetProgress(false)
	terminal.Write("one")
	terminal.Write("two")
	want := "\x1b[2B\x1b[1A\x1b[?25l\x1b[?25h\x1b[K\x1b[J\x1b[2J\x1b[H\x1b]0;title\x07" + progressActiveSequence + progressClearSequence + "onetwo"
	if got := readTerminalOutput(t, output); got != want {
		t.Fatalf("output mismatch:\n got %q\nwant %q", got, want)
	}
	if logged, err := os.ReadFile(logPath); err != nil || string(logged) != "onetwo" {
		t.Fatalf("write log = %q, %v", logged, err)
	}
}

func TestProcessTerminalKeyboardNegotiation(t *testing.T) {
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	terminal, output := temporaryProcessTerminal(t)
	var mu sync.Mutex
	var input strings.Builder
	terminal.onInput = func(value string) { mu.Lock(); defer mu.Unlock(); input.WriteString(value) }
	terminal.processInputSequence("\x1b[?1;2c")
	terminal.processInputSequence("a")
	terminal.processInputSequence("\x1b[")
	terminal.processInputSequence("?7u")
	if !terminal.KittyProtocolActive() {
		t.Fatal("kitty keyboard protocol was not activated")
	}
	if got := readTerminalOutput(t, output); got != modifyOtherKeysEnable+modifyOtherKeysDisable {
		t.Fatalf("negotiation output = %q", got)
	}
	mu.Lock()
	gotInput := input.String()
	mu.Unlock()
	if gotInput != "a" {
		t.Fatalf("forwarded input = %q, want %q", gotInput, "a")
	}
}

func TestProcessTerminalNegotiationPrefixTimesOut(t *testing.T) {
	terminal, _ := temporaryProcessTerminal(t)
	input := make(chan string, 1)
	terminal.onInput = func(value string) { input <- value }
	terminal.processInputSequence("\x1b[")
	select {
	case got := <-input:
		if got != "\x1b[" {
			t.Fatalf("forwarded input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("negotiation prefix was not flushed")
	}
}

func TestProcessTerminalStartInputAndStop(t *testing.T) {
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "terminal-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputRead.Close(); _ = inputWrite.Close(); _ = output.Close() })
	terminal := newProcessTerminal(inputRead, output)
	input := make(chan string, 2)
	terminal.Start(func(value string) { input <- value }, func() {})
	if _, err := inputWrite.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-input:
		if got != "x" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for terminal input")
	}
	if _, err := inputWrite.Write([]byte("\x1b[?7u")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !terminal.KittyProtocolActive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !terminal.KittyProtocolActive() {
		t.Fatal("kitty response was not processed")
	}
	terminal.Stop()
	want := "\x1b[?2004h" + kittyKeyboardProtocolQuery + "\x1b[?2004l" + kittyKeyboardProtocolPop
	if got := readTerminalOutput(t, output); got != want {
		t.Fatalf("lifecycle output mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestProcessTerminalDrainInput(t *testing.T) {
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "terminal-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputRead.Close(); _ = inputWrite.Close(); _ = output.Close() })
	terminal := newProcessTerminal(inputRead, output)
	input := make(chan string, 2)
	terminal.Start(func(value string) { input <- value }, func() {})
	if _, err := inputWrite.Write([]byte("\x1b[?1;2c")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		terminal.stateMu.Lock()
		active := terminal.modifyOtherKeysActive
		terminal.stateMu.Unlock()
		if active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	drained := make(chan error, 1)
	go func() { drained <- terminal.DrainInput(500*time.Millisecond, 30*time.Millisecond) }()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		terminal.stateMu.Lock()
		draining := terminal.onInput == nil
		terminal.stateMu.Unlock()
		if draining {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := inputWrite.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain input did not finish")
	}
	select {
	case got := <-input:
		t.Fatalf("drained input was forwarded: %q", got)
	default:
	}
	if _, err := inputWrite.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-input:
		if got != "y" {
			t.Fatalf("input after drain = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("input handler was not restored after drain")
	}
	terminal.Stop()
	want := "\x1b[?2004h" + kittyKeyboardProtocolQuery + modifyOtherKeysEnable + kittyKeyboardProtocolPop + modifyOtherKeysDisable + "\x1b[?2004l"
	if got := readTerminalOutput(t, output); got != want {
		t.Fatalf("drain lifecycle output mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestProcessTerminalDrainInputFromCallback(t *testing.T) {
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "terminal-output-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputRead.Close(); _ = inputWrite.Close(); _ = output.Close() })
	terminal := newProcessTerminal(inputRead, output)
	drainStarted := make(chan struct{})
	drained := make(chan error, 1)
	input := make(chan string, 1)
	terminal.Start(func(value string) {
		if value == "q" {
			close(drainStarted)
			drained <- terminal.DrainInput(500*time.Millisecond, 30*time.Millisecond)
			return
		}
		input <- value
	}, func() {})
	if _, err := inputWrite.Write([]byte("qrelease")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("drain callback did not start")
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain called from input callback deadlocked")
	}
	select {
	case got := <-input:
		t.Fatalf("drained callback input was forwarded: %q", got)
	default:
	}
	if _, err := inputWrite.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-input:
		if got != "z" {
			t.Fatalf("input after drain = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("input did not resume after callback drain")
	}
	terminal.Stop()
}

func TestProcessTerminalIgnoresStaleSessionEvents(t *testing.T) {
	terminal, output := temporaryProcessTerminal(t)
	called := false
	currentSession := &terminalInputSession{generation: 2}
	staleSession := &terminalInputSession{generation: 1}
	currentSession.active.Store(true)
	staleSession.active.Store(true)
	terminal.stateMu.Lock()
	terminal.started = true
	terminal.generation = 2
	terminal.session = currentSession
	terminal.onInput = func(string) { called = true }
	terminal.stateMu.Unlock()
	terminal.processInputSequenceForSession(staleSession, "\x1b[?7u")
	terminal.forwardInputForSession(staleSession, "a")
	terminal.handleResize(staleSession)
	if called {
		t.Fatal("stale session invoked a callback")
	}
	if terminal.KittyProtocolActive() {
		t.Fatal("stale session activated Kitty protocol")
	}
	if got := readTerminalOutput(t, output); got != "" {
		t.Fatalf("stale session wrote %q", got)
	}
}

func TestProcessTerminalStopFromInputCallbackInvalidatesRemainingSequence(t *testing.T) {
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	terminal, output := temporaryProcessTerminal(t)
	session := &terminalInputSession{generation: 1}
	session.active.Store(true)
	terminal.stateMu.Lock()
	terminal.started = true
	terminal.generation = 1
	terminal.session = session
	terminal.onInput = func(string) { terminal.Stop() }
	terminal.stateMu.Unlock()
	terminal.processInputSequenceForSession(session, "\x1b[")
	terminal.processInputSequenceForSession(session, "\x1b[?1c")
	terminal.stateMu.Lock()
	modifyOtherKeys := terminal.modifyOtherKeysActive
	terminal.stateMu.Unlock()
	if modifyOtherKeys {
		t.Fatal("protocol fallback was re-enabled after Stop")
	}
	if terminal.KittyProtocolActive() {
		t.Fatal("Kitty protocol was re-enabled after Stop")
	}
	if got := readTerminalOutput(t, output); got != "\x1b[?2004l" {
		t.Fatalf("stop output = %q", got)
	}
}

func TestProcessTerminalRestartDoesNotWaitForOldCallback(t *testing.T) {
	terminal, _ := temporaryProcessTerminal(t)
	oldSession := &terminalInputSession{generation: 1}
	oldSession.active.Store(true)
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	oldDone := make(chan struct{})
	terminal.stateMu.Lock()
	terminal.started, terminal.generation, terminal.session = true, 1, oldSession
	terminal.onInput = func(string) { close(callbackStarted); <-releaseCallback }
	terminal.stateMu.Unlock()
	go func() { terminal.processInputSequenceForSession(oldSession, "a"); close(oldDone) }()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("old callback did not start")
	}
	terminal.Stop()
	newInput := make(chan string, 1)
	terminal.Start(func(value string) { newInput <- value }, func() {})
	terminal.stateMu.Lock()
	newSession := terminal.session
	terminal.stateMu.Unlock()
	terminal.processInputSequenceForSession(newSession, "b")
	select {
	case got := <-newInput:
		if got != "b" {
			t.Fatalf("new input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("new session was blocked by old callback")
	}
	close(releaseCallback)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old callback did not finish")
	}
	terminal.Stop()
}

func TestProcessTerminalConcurrentLifecycle(t *testing.T) {
	terminal, _ := temporaryProcessTerminal(t)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(2)
		go func() { defer wait.Done(); terminal.Start(func(string) {}, func() {}) }()
		go func() { defer wait.Done(); terminal.Stop() }()
	}
	wait.Wait()
	terminal.Stop()
	terminal.stateMu.Lock()
	defer terminal.stateMu.Unlock()
	if terminal.started || terminal.session != nil || terminal.stdinBuffer != nil || terminal.stopReader != nil || terminal.stopResize != nil || terminal.oldState != nil {
		t.Fatal("terminal retained lifecycle state after concurrent Stop")
	}
}
