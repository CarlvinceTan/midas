package launch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("MIDAS_BROWSER_TEST_HELPER") != "1" {
		return
	}

	select {}
}

func startHelperProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "MIDAS_BROWSER_TEST_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition was not satisfied within %s", timeout)
}

func waitForCommandExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-time.After(timeout):
		t.Fatalf("command did not exit within %s", timeout)
	case err := <-done:
		if err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				return
			}
			t.Fatalf("command exited with error: %v", err)
		}
	}
}

func newHTTPTestServerOrSkip(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	var server *httptest.Server
	defer func() {
		if r := recover(); r != nil {
			msg := strings.ToLower(toString(r))
			if strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "failed to listen on a port") {
				t.Skipf("skipping network listener test in sandbox: %v", r)
			}
			panic(r)
		}
	}()

	server = httptest.NewServer(handler)
	return server
}

func toString(v any) string {
	return fmt.Sprint(v)
}
