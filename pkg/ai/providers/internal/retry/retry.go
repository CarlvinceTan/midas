// Package retry wraps provider HTTP requests with bounded, jittered retries for
// the responses a provider says are worth retrying.
package retry

import (
	"bytes"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultMaxDelay = 2 * time.Second

// Do sends an HTTP request and retries transient transport failures and
// retryable statuses. The request body is recreated for every attempt.
func Do(client *http.Client, request *http.Request, body []byte, maxRetries int, maxDelay time.Duration) (*http.Response, error) {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxDelay <= 0 {
		maxDelay = defaultMaxDelay
	}
	for attempt := 0; ; attempt++ {
		current := request.Clone(request.Context())
		current.Body = io.NopCloser(bytes.NewReader(body))
		current.ContentLength = int64(len(body))
		response, err := client.Do(current)
		if err == nil && !retryable(response.StatusCode) {
			return response, nil
		}
		if contextErr := request.Context().Err(); contextErr != nil {
			if response != nil {
				_ = response.Body.Close()
			}
			return nil, contextErr
		}
		if attempt >= maxRetries {
			return response, err
		}
		delay := backoff(attempt, maxDelay)
		if response != nil {
			if retryAfter := retryAfter(response.Header.Get("Retry-After"), time.Now()); retryAfter >= 0 {
				delay = min(retryAfter, maxDelay)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
		}
		timer := time.NewTimer(delay)
		select {
		case <-request.Context().Done():
			timer.Stop()
			return nil, request.Context().Err()
		case <-timer.C:
		}
	}
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= 500
}

func backoff(attempt int, maximum time.Duration) time.Duration {
	delay := 250 * time.Millisecond
	for range attempt {
		if delay >= maximum/2 {
			return jitter(maximum)
		}
		delay *= 2
	}
	return jitter(min(delay, maximum))
}

// jitter spreads a delay over ±20% so that clients which failed together do not
// retry together.
func jitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	spread := delay / 5
	return delay - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, at.Sub(now))
	}
	return -1
}
