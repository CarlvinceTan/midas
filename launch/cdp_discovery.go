package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/carlvincetan/polymux/internal/midas/debug"
)

type cdpEndpointOptions struct {
	BaseURL string
	Headers map[string]string
}

func waitForWebSocketDebuggerURL(ctx context.Context, port int, deadline time.Time) (string, error) {
	return waitForWebSocketDebuggerURLWithMessage(ctx, cdpEndpointOptions{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
	}, deadline, fmt.Sprintf("timed out waiting for /json/version on port %d", port))
}

func waitForWebSocketDebuggerURLAt(ctx context.Context, opts cdpEndpointOptions, deadline time.Time) (string, error) {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	timeoutMessage := fmt.Sprintf("timed out waiting for /json/version at %s", baseURL)
	if parsedURL, err := url.Parse(baseURL); err == nil && parsedURL.Host != "" {
		timeoutMessage = fmt.Sprintf("timed out waiting for /json/version at %s", parsedURL.Host)
	}
	return waitForWebSocketDebuggerURLWithMessage(ctx, opts, deadline, timeoutMessage)
}

func waitForWebSocketDebuggerURLWithMessage(ctx context.Context, opts cdpEndpointOptions, deadline time.Time, timeoutMessage string) (string, error) {
	type versionResponse struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}

	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		return "", fmt.Errorf("base CDP URL is required")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	var lastErrMsg string
	var lastProgressLog time.Time

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/version", nil)
		if err != nil {
			return "", err
		}
		for key, value := range opts.Headers {
			if strings.EqualFold(key, "Host") {
				req.Host = value
				continue
			}
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErrMsg = err.Error()
		} else {
			var body versionResponse
			func() {
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					lastErrMsg = resp.Status
					return
				}

				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
					lastErrMsg = err.Error()
					return
				}
			}()

			if body.WebSocketDebuggerURL != "" {
				return body.WebSocketDebuggerURL, nil
			}

			if lastErrMsg == "" {
				lastErrMsg = "missing webSocketDebuggerUrl"
			}
		}

		if debug.Enabled() && (lastProgressLog.IsZero() || time.Since(lastProgressLog) >= 5*time.Second) {
			debug.Printf("still waiting for /json/version at %s (last error: %s)", baseURL, lastErrMsg)
			lastProgressLog = time.Now()
		}

		if err := sleepContext(ctx, 250*time.Millisecond); err != nil {
			return "", err
		}
	}

	msg := timeoutMessage
	if lastErrMsg != "" {
		msg += fmt.Sprintf(" (last error: %s)", lastErrMsg)
	}
	return "", &ConnectionTimeoutError{Message: msg}
}

func waitForWebSocketReady(ctx context.Context, wsURL string, deadline time.Time) error {
	var lastErrMsg string
	var lastProgressLog time.Time

	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining < 200*time.Millisecond {
			remaining = 200 * time.Millisecond
		}

		timeout := 2 * time.Second
		if remaining < timeout {
			timeout = remaining
		}

		if err := probeWebSocket(ctx, wsURL, timeout); err == nil {
			return nil
		} else {
			lastErrMsg = err.Error()
		}

		if debug.Enabled() && (lastProgressLog.IsZero() || time.Since(lastProgressLog) >= 5*time.Second) {
			debug.Printf("still probing CDP WebSocket %s (last error: %s)", wsURL, lastErrMsg)
			lastProgressLog = time.Now()
		}

		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}

	msg := fmt.Sprintf("timed out waiting for CDP websocket to accept connections at %s", wsURL)
	if lastErrMsg != "" {
		msg += fmt.Sprintf(" (last error: %s)", lastErrMsg)
	}
	return &ConnectionTimeoutError{Message: msg}
}

func probeWebSocket(ctx context.Context, wsURL string, timeout time.Duration) error {
	dialer := websocket.Dialer{HandshakeTimeout: timeout}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, _, err := dialer.DialContext(dialCtx, wsURL, nil)
	if err != nil {
		return err
	}
	return conn.Close()
}
