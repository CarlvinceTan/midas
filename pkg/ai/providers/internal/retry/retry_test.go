package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoRetriesTransientStatusAndReplaysBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if string(body) != "payload" {
			t.Errorf("body %d = %q", call, body)
		}
		if call < 3 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("payload"))
	response, err := Do(server.Client(), request, []byte("payload"), 2, time.Millisecond)
	if err != nil || response.StatusCode != http.StatusOK || calls.Load() != 3 {
		t.Fatalf("response=%v calls=%d err=%v", response, calls.Load(), err)
	}
	response.Body.Close()
}

func TestDoDoesNotRetryPermanentStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := Do(server.Client(), request, nil, 3, time.Millisecond)
	if err != nil || response.StatusCode != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatalf("response=%v calls=%d err=%v", response, calls.Load(), err)
	}
	response.Body.Close()
}

func TestDoHonorsCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripper(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, errors.New("temporary")
	})
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	_, err := Do(&http.Client{Transport: transport}, request, nil, 3, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (fn roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
