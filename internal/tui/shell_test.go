package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalShellRunsInRequestedDirectoryAndCombinesOutput(t *testing.T) {
	cwd := t.TempDir()
	var output strings.Builder
	exitCode, cancelled, err := runLocalShell(context.Background(), cwd, "pwd; printf stdout; printf stderr >&2", func(chunk string) {
		output.WriteString(chunk)
	})
	if err != nil || cancelled || exitCode != 0 {
		t.Fatalf("shell result = exit %d cancelled=%v err=%v", exitCode, cancelled, err)
	}
	got := output.String()
	if !strings.Contains(got, cwd) || !strings.Contains(got, "stdout") || !strings.Contains(got, "stderr") {
		t.Fatalf("combined output = %q", got)
	}
}

func TestCDTargetRecognisesOnlyPlainDirectoryChanges(t *testing.T) {
	cwd := "/work/project"
	cases := []struct {
		command string
		want    string
		ok      bool
	}{
		{command: "cd sub", want: "/work/project/sub", ok: true},
		{command: "cd ../other", want: "/work/other", ok: true},
		{command: "cd /tmp", want: "/tmp", ok: true},
		{command: "cd \"quoted dir\"", want: "/work/project/quoted dir", ok: true},
		{command: "cd", ok: false}, // home is resolved separately, never empty
		{command: "cd sub && make", ok: false},
		{command: "cd sub; ls", ok: false},
		{command: "cd $HOME", ok: false},
		{command: "cd sub | tee log", ok: false},
		{command: "cd ..", want: "/work", ok: true},
		{command: "ls", ok: false},
	}
	for _, test := range cases {
		target, ok := cdTarget(cwd, test.command)
		if test.command == "cd" {
			if ok && target == "" {
				t.Fatalf("cd resolved to an empty path")
			}
			continue
		}
		if ok != test.ok || (test.ok && target != test.want) {
			t.Fatalf("cdTarget(%q) = %q, %v; want %q, %v", test.command, target, ok, test.want, test.ok)
		}
	}
}

// TestShellCDMovesTheWorkingDirectory goes through the real shell entry point: a
// cd used to deadlock there because the row was finished while holding the lock
// that finishing takes.
func TestShellCDMovesTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	asked := make(chan string, 4)
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test", CWD: root,
		ShellRunner: func(context.Context, string, string, func(string)) (int, bool, error) {
			return 0, false, nil
		},
		OnDirectoryChange: func(path string) error {
			asked <- path
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		chat.startShell("cd sub", false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a cd deadlocked the shell row")
	}
	select {
	case path := <-asked:
		if path != filepath.Join(root, "sub") {
			t.Fatalf("hook saw %q", path)
		}
	default:
		t.Fatal("the hook was never called")
	}
	if chat.cwd != filepath.Join(root, "sub") {
		t.Fatalf("cwd = %q", chat.cwd)
	}
	// The row that started it reports the move, rather than a second empty row.
	chat.mu.Lock()
	last := chat.entries[len(chat.entries)-1].shell
	chat.mu.Unlock()
	if last == nil || last.Status != "complete" || !strings.Contains(last.Output, "sub") {
		t.Fatalf("cd row = %#v", last)
	}
}

func TestChangeDirectoryMovesOnlyWhenTheHookAccepts(t *testing.T) {
	var asked string
	chat, err := NewChat(ChatOptions{
		Backend: &fakeChatBackend{}, Provider: "fake", Model: "test", CWD: "/work/project",
		OnDirectoryChange: func(path string) error {
			asked = path
			if path == "/work/project/missing" {
				return fmt.Errorf("no such directory: %s", path)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.startShell("cd /work/project/src", false)
	if asked != "/work/project/src" {
		t.Fatalf("hook saw %q", asked)
	}
	if chat.cwd != "/work/project/src" {
		t.Fatalf("cwd = %q", chat.cwd)
	}
	// A rejected change leaves the working directory alone and reports why.
	chat.startShell("cd /work/project/missing", false)
	if chat.cwd != "/work/project/src" {
		t.Fatalf("cwd after a failed change = %q", chat.cwd)
	}
	last := chat.entries[len(chat.entries)-1]
	if last.shell == nil || last.shell.Status != "error" || !strings.Contains(last.shell.Output, "no such directory") {
		t.Fatalf("failure entry = %#v", last.shell)
	}
	// Without a hook nothing moves either.
	bare, err := NewChat(ChatOptions{Backend: &fakeChatBackend{}, Provider: "fake", Model: "test", CWD: "/work/project"})
	if err != nil {
		t.Fatal(err)
	}
	bare.startShell("cd /work/project/src", false)
	if bare.cwd != "/work/project" {
		t.Fatalf("cwd without a hook = %q", bare.cwd)
	}
}
