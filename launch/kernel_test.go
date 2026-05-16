package launch

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLaunchKernelWithDirectWSURL(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := LaunchKernel(ctx, LaunchKernelOptions{
		WSURL:            "ws" + server.URL[len("http"):],
		ConnectTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("LaunchKernel returned error: %v", err)
	}
	defer func() {
		_ = result.Resource.Close()
	}()

	if result.WS == "" {
		t.Fatal("expected websocket URL")
	}
	if result.Kernel == nil {
		t.Fatal("expected kernel resource")
	}
}

func TestLaunchKernelLaunchesAndClosesManagedProcess(t *testing.T) {
	t.Parallel()

	var wsURL string
	server := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"` + wsURL + `"}`))
		default:
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
		}
	}))
	defer server.Close()
	wsURL = "ws" + server.URL[len("http"):]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := LaunchKernel(ctx, LaunchKernelOptions{
		Command:          helperProcessCommandSpec(),
		Env:              map[string]string{"MIDAS_BROWSER_TEST_HELPER": "1"},
		BaseURL:          server.URL,
		ConnectTimeoutMs: 2000,
	})
	if err != nil {
		t.Fatalf("LaunchKernel returned error: %v", err)
	}

	kernel := result.Kernel
	if kernel == nil || kernel.Cmd == nil || kernel.Cmd.Process == nil {
		t.Fatal("expected managed kernel process")
	}

	if err := result.Resource.Close(); err != nil {
		t.Fatalf("kernel close returned error: %v", err)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		return !processAlive(kernel.Cmd.Process.Pid)
	})
}

func TestLaunchedKernelCloseRunsShutdownCommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "shutdown.marker")
	scriptPath := filepath.Join(tempDir, "shutdown.sh")
	script := "#!/bin/sh\n" +
		"touch \"" + markerPath + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write shutdown script: %v", err)
	}

	kernel := &LaunchedKernel{
		ShutdownCommand: []string{scriptPath},
	}

	if err := kernel.Close(); err != nil {
		t.Fatalf("kernel close returned error: %v", err)
	}

	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected shutdown marker to exist, got err=%v", err)
	}
}

func helperProcessCommandSpec() []string {
	return []string{os.Args[0], "-test.run=TestHelperProcess"}
}
