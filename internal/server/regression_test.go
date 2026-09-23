package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSocketRefusesAnUnconfiguredToken: the socket authenticates itself instead
// of going through the bearer middleware, so with no token configured a request
// that omits the token must not be treated as authorised.
func TestSocketRefusesAnUnconfiguredToken(t *testing.T) {
	environment := testEnvironment(t)
	environment.Config.Token = ""
	for _, target := range []string{"/v1/socket", "/v1/socket?token=", "/v1/socket?token=anything"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		environment.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401", target, recorder.Code)
		}
	}
}

// TestSocketAcceptsTheConfiguredToken keeps the other direction honest.
func TestSocketAcceptsTheConfiguredToken(t *testing.T) {
	environment := testEnvironment(t)
	environment.Config.Token = "socket-token"
	request := httptest.NewRequest(http.MethodGet, "/v1/socket?token=socket-token", nil)
	if !environment.socketAuthorized(request) {
		t.Fatal("the configured token was refused")
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/socket?token=other", nil)
	if environment.socketAuthorized(request) {
		t.Fatal("a wrong token was accepted")
	}
}

// TestDecodeJSONRejectsAnOversizedBody protects the API from allocating without
// limit for a single request.
func TestDecodeJSONRejectsAnOversizedBody(t *testing.T) {
	huge := `{"name":"` + strings.Repeat("a", maxJSONBody+1024) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/teams", strings.NewReader(huge))
	recorder := httptest.NewRecorder()
	var body struct {
		Name string `json:"name"`
	}
	err := decodeJSON(recorder, request, &body)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized body = %v", err)
	}
}

// TestFeedPublishIsSafeDuringUnsubscribeAndClose is the regression test for a
// panic: publishing used to send on a subscriber channel after releasing the lock,
// so an unsubscribe or Close that closed the channel could race the send. Run
// under -race.
func TestFeedPublishIsSafeDuringUnsubscribeAndClose(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		feed := NewFeed()
		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					feed.Publish(Event{Kind: EventMessage, Action: "test"})
				}
			}
		}()
		for subscriber := 0; subscriber < 4; subscriber++ {
			events, unsubscribe := feed.Subscribe()
			go func() {
				for range events {
				}
			}()
			unsubscribe()
		}
		feed.Close()
		close(stop)
		wg.Wait()
	}
}

// TestFeedSinceReturnsChangesAfterATimestamp keeps the reconnect path honest.
func TestFeedSinceReturnsChangesAfterATimestamp(t *testing.T) {
	feed := NewFeed()
	feed.Publish(Event{Kind: EventMessage, Action: "first", At: 10})
	feed.Publish(Event{Kind: EventMessage, Action: "second", At: 20})
	events := feed.Since(10)
	if len(events) != 1 || events[0].Action != "second" {
		t.Fatalf("since(10) = %#v", events)
	}
	if events := feed.Since(0); events != nil {
		t.Fatalf("since(0) = %#v", events)
	}
}

// TestPoolSingleFlightConnectsOnce: concurrent callers wait for one connection
// attempt instead of starting their own, and every caller gets the same tools.
func TestPoolSingleFlightConnectsOnce(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".midas"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"mcpServers":{"slow":{"command":["/nonexistent/mcp-server"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".midas", "mcp.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(configDir, root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var wg sync.WaitGroup
	results := make([]int, 8)
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tools, err := pool.Tools(ctx)
			if err != nil {
				t.Errorf("tools: %v", err)
				return
			}
			results[index] = len(tools)
		}(index)
	}
	wg.Wait()
	for index, count := range results {
		if count != 0 {
			t.Fatalf("caller %d saw %d tools", index, count)
		}
	}
	if idle := pool.IdleFor(); idle <= 0 {
		t.Fatalf("idle = %v, want the time since the last use", idle)
	}
}
