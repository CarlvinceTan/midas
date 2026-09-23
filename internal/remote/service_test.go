package remote

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeTunnel struct {
	url    string
	closed atomic.Bool
}

func (t *fakeTunnel) URL() string  { return t.url }
func (t *fakeTunnel) Close() error { t.closed.Store(true); return nil }

type trackedCloser struct{ closed atomic.Bool }

func (c *trackedCloser) Close() error { c.closed.Store(true); return nil }

func TestServiceRequiresPasswordAndSharesRunningTerminal(t *testing.T) {
	local := &fakeTerminal{cols: 100, rows: 35}
	terminal := NewTerminal(local)
	input := make(chan string, 1)
	terminal.Start(func(value string) { input <- value }, func() {})
	terminal.SetRequestRender(func() { terminal.Write("full frame") })
	tunnel := &fakeTunnel{url: "https://midas-test.trycloudflare.com"}
	awake := &trackedCloser{}
	service, err := Start(context.Background(), Options{
		Terminal: terminal, Password: "correct horse",
		Tunnel:       func(context.Context, int) (Tunnel, error) { return tunnel, nil },
		PreventSleep: func() (io.Closer, error) { return awake, nil },
		VerifyPublic: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	response, err := http.Get(service.LocalURL())
	if err != nil {
		t.Fatal(err)
	}
	loginPage, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !bytes.Contains(loginPage, []byte("Remote password")) || bytes.Contains(loginPage, []byte("xterm.js")) {
		t.Fatalf("unauthenticated page = %s", loginPage)
	}
	wrong, err := http.Post(service.LocalURL()+"/api/login", "application/json", strings.NewReader(`{"password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d", wrong.StatusCode)
	}
	login, err := http.Post(service.LocalURL()+"/api/login", "application/json", strings.NewReader(`{"password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK || len(login.Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%#v", login.StatusCode, login.Cookies())
	}
	header := http.Header{}
	header.Set("Cookie", login.Cookies()[0].String())
	websocketURL := "ws" + strings.TrimPrefix(service.LocalURL(), "http") + "/ws"
	socket, _, err := websocket.Dial(context.Background(), websocketURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	readContext, cancelRead := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRead()
	frames := ""
	for !strings.Contains(frames, "full frame") {
		_, frame, readErr := socket.Read(readContext)
		if readErr != nil {
			t.Fatal(readErr)
		}
		frames += string(frame)
	}
	if err := socket.Write(context.Background(), websocket.MessageText, []byte(`{"type":"input","data":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-input:
		if got != "hello" {
			t.Fatalf("remote input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote input was not delivered")
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !tunnel.closed.Load() || !awake.closed.Load() {
		t.Fatalf("shutdown tunnel=%v awake=%v", tunnel.closed.Load(), awake.closed.Load())
	}
}

func TestServiceRejectsMissingPassword(t *testing.T) {
	_, err := Start(context.Background(), Options{Terminal: NewTerminal(&fakeTerminal{})})
	if err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("error = %v", err)
	}
}

// TestLoginThrottleTableIsBounded: the per-address attempt table must not grow
// for as long as the session lives when a scanner rotates source addresses.
func TestLoginThrottleTableIsBounded(t *testing.T) {
	service := &Service{tokens: map[string]struct{}{}, attempts: map[string]loginAttempt{}, sockets: map[*websocket.Conn]struct{}{}}
	now := time.Now()
	for index := 0; index < maxLoginAttempts+128; index++ {
		service.loginFailed(strconv.Itoa(index), now)
	}
	service.mu.Lock()
	size := len(service.attempts)
	service.mu.Unlock()
	if size > maxLoginAttempts {
		t.Fatalf("throttle table holds %d addresses", size)
	}
	// Expired entries are the first to go.
	service.mu.Lock()
	for key := range service.attempts {
		attempt := service.attempts[key]
		attempt.resetAt = now.Add(-time.Minute)
		service.attempts[key] = attempt
		break
	}
	service.pruneLoginAttemptsLocked(now)
	service.mu.Unlock()
	service.mu.Lock()
	size = len(service.attempts)
	service.mu.Unlock()
	if size >= maxLoginAttempts+128 {
		t.Fatalf("pruning left %d addresses", size)
	}
}
