//go:build darwin || linux

package tui

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestProcessTerminalPTYLifecycle(t *testing.T) {
	if os.Getenv("MIDAS_TEST_PROCESS_TERMINAL_PTY") != "1" {
		t.Skip("set MIDAS_TEST_PROCESS_TERMINAL_PTY=1 under a pseudo-terminal")
	}
	stdinFD, stdoutFD := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(stdinFD) || !term.IsTerminal(stdoutFD) {
		t.Fatal("test requires terminal stdin and stdout")
	}
	before, err := readTermiosForTest(stdinFD)
	if err != nil {
		t.Fatal(err)
	}
	terminal := NewProcessTerminal()
	terminal.Start(func(string) {}, func() {})
	during, err := readTermiosForTest(stdinFD)
	if err != nil {
		terminal.Stop()
		t.Fatal(err)
	}
	if normalizedTermiosForTest(*before) == normalizedTermiosForTest(*during) {
		terminal.Stop()
		t.Fatal("terminal did not enter raw mode")
	}
	terminal.Stop()
	after, err := readTermiosForTest(stdinFD)
	if err != nil {
		t.Fatal(err)
	}
	if normalizedTermiosForTest(*before) != normalizedTermiosForTest(*after) {
		t.Fatalf("terminal state was not restored:\n before: %#v\n  after: %#v", before, after)
	}
}

func normalizedTermiosForTest(state unix.Termios) unix.Termios {
	// PENDIN is kernel-maintained pending-input state, not a terminal mode.
	state.Lflag &^= unix.PENDIN
	return state
}
