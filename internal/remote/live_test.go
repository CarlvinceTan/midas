package remote

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveQuickTunnel(t *testing.T) {
	if os.Getenv("MIDAS_REMOTE_LIVE") != "1" {
		t.Skip("set MIDAS_REMOTE_LIVE=1 to exercise the public quick tunnel")
	}
	local := &fakeTerminal{cols: 80, rows: 24}
	terminal := NewTerminal(local)
	terminal.Start(func(string) {}, func() {})
	startContext, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	service, err := Start(startContext, Options{Terminal: terminal, Password: "live-test-only"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	t.Logf("public URL: %s", service.URL())
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(25 * time.Second)
	for {
		response, requestErr := client.Get(service.URL() + "/healthz")
		if requestErr == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && string(body) == "ok" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("public health check failed: %v", requestErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	response, err := client.Get(service.URL())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Remote password") {
		t.Fatalf("public login page status=%d body=%q", response.StatusCode, body)
	}
}
