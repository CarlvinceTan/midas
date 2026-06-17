package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The daemon holds one CDP connection for a session and serves commands over a
// unix socket: faster (no per-command re-attach) and — uniquely for the CLI —
// it persists state across separate command invocations.

func TestCLIDaemonServesCommands(t *testing.T) {
	if resolveChromePath() == "" {
		t.Skip("no Chromium binary; skipping daemon test")
	}
	bin := midasBinary(t)
	sessionsDir := t.TempDir()
	const sess = "dmn"

	// open a browser with a focusable input.
	out, err := runCLI(t, sessionsDir, "-s="+sess, "open",
		`data:text/html,<title>D</title><input id=q value="select me">`)
	if err != nil {
		t.Fatalf("open: %v\n%s", err, out)
	}

	// start the daemon in the background.
	daemon := exec.Command(bin, "-s="+sess, "daemon")
	daemon.Env = cliEnv(sessionsDir)
	if err := daemon.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runCLI(t, sessionsDir, "-s="+sess, "close") // stops daemon + browser
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	})

	// wait for the daemon socket to appear.
	sock := filepath.Join(sessionsDir, sess+".sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("daemon socket never appeared at %s", sock)
	}

	// A command now routes to the daemon.
	out, err = runCLI(t, sessionsDir, "-s="+sess, "eval", "document.title")
	if err != nil {
		t.Fatalf("eval via daemon: %v\n%s", err, out)
	}
	if !strings.Contains(out, "D") {
		t.Errorf("daemon eval output = %q", out)
	}

	// Cross-command state: focus, then hold Control across separate processes,
	// press a, release — the daemon's live page keeps the modifier held, so
	// Control+A selects the whole field. A stateless CLI loses the modifier
	// between invocations and would select nothing.
	if _, err := runCLI(t, sessionsDir, "-s="+sess, "eval",
		`(()=>{document.getElementById('q').focus();return 'ok'})()`); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if _, err := runCLI(t, sessionsDir, "-s="+sess, "keydown", "Control"); err != nil {
		t.Fatalf("keydown: %v", err)
	}
	if _, err := runCLI(t, sessionsDir, "-s="+sess, "press", "a"); err != nil {
		t.Fatalf("press: %v", err)
	}
	if _, err := runCLI(t, sessionsDir, "-s="+sess, "keyup", "Control"); err != nil {
		t.Fatalf("keyup: %v", err)
	}
	out, err = runCLI(t, sessionsDir, "-s="+sess, "eval",
		`(()=>{const e=document.getElementById('q');return e.selectionEnd-e.selectionStart})()`)
	if err != nil {
		t.Fatalf("eval selection: %v\n%s", err, out)
	}
	if !strings.Contains(out, "9") {
		t.Errorf("cross-command modifier state lost: selection length = %q, want 9", strings.TrimSpace(out))
	}
}

func TestCLIDaemonStoppedByClose(t *testing.T) {
	if resolveChromePath() == "" {
		t.Skip("no Chromium binary; skipping daemon test")
	}
	bin := midasBinary(t)
	sessionsDir := t.TempDir()
	const sess = "dmn2"

	if out, err := runCLI(t, sessionsDir, "-s="+sess, "open", "data:text/html,<title>x</title>"); err != nil {
		t.Fatalf("open: %v\n%s", err, out)
	}
	daemon := exec.Command(bin, "-s="+sess, "daemon")
	daemon.Env = cliEnv(sessionsDir)
	if err := daemon.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer func() { _ = daemon.Process.Kill(); _ = daemon.Wait() }()

	sock := filepath.Join(sessionsDir, sess+".sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// close should stop the daemon and remove its socket.
	if out, err := runCLI(t, sessionsDir, "-s="+sess, "close"); err != nil {
		t.Fatalf("close: %v\n%s", err, out)
	}
	// give the daemon a moment to exit and clean up its socket.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			return // socket gone — daemon shut down cleanly
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("daemon socket %s still present after close", sock)
}
