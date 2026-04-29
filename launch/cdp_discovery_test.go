package launch

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWaitForWebSocketDebuggerURLAtUsesHostHeader(t *testing.T) {
	t.Parallel()

	const expectedHost = "kernel.local"
	server := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		if r.Host != expectedHost {
			t.Fatalf("expected host %q, got %q", expectedHost, r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/test"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL, err := waitForWebSocketDebuggerURLAt(ctx, cdpEndpointOptions{
		BaseURL: server.URL,
		Headers: map[string]string{"Host": expectedHost},
	}, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatalf("waitForWebSocketDebuggerURLAt returned error: %v", err)
	}
	if wsURL != "ws://127.0.0.1/devtools/browser/test" {
		t.Fatalf("unexpected websocket URL: %s", wsURL)
	}
}

func TestWaitForWebSocketReadySucceeds(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	server := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := waitForWebSocketReady(ctx, wsURL, time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("waitForWebSocketReady returned error: %v", err)
	}
}
