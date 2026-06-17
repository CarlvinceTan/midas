package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// CLI (cmd/midas) spawn-binary tests: build the binary once, then drive it as a
// subprocess the way a user would, asserting on its stdout. The CLI launches
// its own detached Chromium (separate from the harness browser) but reaches the
// harness fixture server over loopback.

var (
	cliBinOnce sync.Once
	cliBinPath string
	cliBinErr  error
	cliBinDir  string
)

// midasBinary builds cmd/midas once per test binary and returns its path.
func midasBinary(t *testing.T) string {
	t.Helper()
	cliBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "midas-cli-bin")
		if err != nil {
			cliBinErr = err
			return
		}
		cliBinDir = dir
		bin := filepath.Join(dir, "midas")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/PolymuxOrg/midas/cmd/midas")
		if out, err := cmd.CombinedOutput(); err != nil {
			cliBinErr = err
			t.Logf("go build output:\n%s", out)
			return
		}
		cliBinPath = bin
	})
	if cliBinErr != nil {
		t.Fatalf("build midas CLI: %v", cliBinErr)
	}
	return cliBinPath
}

// cliEnv builds the env for a CLI invocation: an isolated session dir and the
// resolved Chromium path.
func cliEnv(sessionsDir string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"MIDAS_SESSIONS_DIR="+sessionsDir,
		"MIDAS_CHROME_PATH="+resolveChromePath(),
	)
	return env
}

// runCLI runs the midas binary with the given args and isolated session dir,
// returning combined output.
func runCLI(t *testing.T, sessionsDir string, args ...string) (string, error) {
	t.Helper()
	bin := midasBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = cliEnv(sessionsDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCLIHelp(t *testing.T) {
	if resolveChromePath() == "" {
		t.Skip("no Chromium binary; skipping CLI tests")
	}
	out, err := runCLI(t, t.TempDir(), "--help")
	if err != nil {
		t.Fatalf("--help exited non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Usage:") || !strings.Contains(out, "snapshot") {
		t.Errorf("--help output missing expected text:\n%s", out)
	}
}

func TestCLIListEmpty(t *testing.T) {
	if resolveChromePath() == "" {
		t.Skip("no Chromium binary; skipping CLI tests")
	}
	out, err := runCLI(t, t.TempDir(), "list")
	if err != nil {
		t.Fatalf("list exited non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(no sessions)") {
		t.Errorf("empty list output = %q, want '(no sessions)'", out)
	}
}

func TestCLIErrorOnMissingSession(t *testing.T) {
	if resolveChromePath() == "" {
		t.Skip("no Chromium binary; skipping CLI tests")
	}
	out, err := runCLI(t, t.TempDir(), "-s=does-not-exist", "goto", "data:text/html,x")
	if err == nil {
		t.Fatalf("goto on a missing session should exit non-zero; output:\n%s", out)
	}
}

func TestCLILifecycle(t *testing.T) {
	h := requireHarness(t)
	sessionsDir := t.TempDir()
	const sess = "clilife"

	// Always reap the detached browser, even on failure.
	t.Cleanup(func() {
		_, _ = runCLI(t, sessionsDir, "-s="+sess, "close")
	})

	out, err := runCLI(t, sessionsDir, "-s="+sess, "open")
	if err != nil {
		t.Fatalf("open: %v\n%s", err, out)
	}
	if !strings.Contains(out, "opened session") {
		t.Fatalf("open output = %q", out)
	}

	// goto a real fixture over loopback — should report HTTP status.
	out, err = runCLI(t, sessionsDir, "-s="+sess, "goto", h.server.URL+"/form.html")
	if err != nil {
		t.Fatalf("goto: %v\n%s", err, out)
	}
	if !strings.Contains(out, "status=200") {
		t.Errorf("goto output = %q, want status=200", out)
	}

	// eval reads the page title back as JSON.
	out, err = runCLI(t, sessionsDir, "-s="+sess, "eval", "document.title")
	if err != nil {
		t.Fatalf("eval: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Form test") {
		t.Errorf("eval title output = %q, want it to contain 'Form test'", out)
	}

	// fill then read the value back.
	if out, err = runCLI(t, sessionsDir, "-s="+sess, "fill", "#username", "cli-user"); err != nil {
		t.Fatalf("fill: %v\n%s", err, out)
	}
	out, err = runCLI(t, sessionsDir, "-s="+sess, "eval", "document.getElementById('username').value")
	if err != nil {
		t.Fatalf("eval value: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cli-user") {
		t.Errorf("filled value output = %q, want 'cli-user'", out)
	}

	// snapshot prints the a11y tree.
	out, err = runCLI(t, sessionsDir, "-s="+sess, "snapshot")
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Sign up") {
		t.Errorf("snapshot output missing button text:\n%s", out)
	}

	// list shows the live session.
	out, err = runCLI(t, sessionsDir, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, sess) {
		t.Errorf("list output = %q, want it to mention %q", out, sess)
	}

	// close removes it.
	out, err = runCLI(t, sessionsDir, "-s="+sess, "close")
	if err != nil {
		t.Fatalf("close: %v\n%s", err, out)
	}
	if !strings.Contains(out, "closed session") {
		t.Errorf("close output = %q", out)
	}
}
