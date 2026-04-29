package browser

import (
	"context"
	"testing"
	"time"
)

func TestResponseHeadersReturnsNormalizedLowercase(t *testing.T) {
	resp := &Response{
		headers: map[string]string{
			"content-type": "application/json",
			"x-custom":     "value",
		},
	}
	h := resp.Headers()
	if h["content-type"] != "application/json" {
		t.Fatalf("expected content-type header, got %#v", h)
	}
	if h["x-custom"] != "value" {
		t.Fatalf("expected x-custom header, got %#v", h)
	}
}

func TestResponseAllHeadersMergesBaseAndExtra(t *testing.T) {
	resp := &Response{
		headers: map[string]string{
			"content-type": "application/json",
			"x-test":       "a",
		},
		extraHeaders: map[string]string{
			"set-cookie": "session=abc",
			"x-test":     "b",
		},
	}
	all := resp.AllHeaders()
	if all["content-type"] != "application/json" {
		t.Fatalf("expected content-type from base headers, got %#v", all)
	}
	if all["set-cookie"] != "session=abc" {
		t.Fatalf("expected set-cookie from extra headers, got %#v", all)
	}
	if all["x-test"] != "b" {
		t.Fatalf("expected x-test to be overridden by extra headers, got %#v", all)
	}
}

func TestResponseAllHeadersReturnsNilWhenEmpty(t *testing.T) {
	resp := &Response{}
	if all := resp.AllHeaders(); all != nil {
		t.Fatalf("expected nil for empty headers, got %#v", all)
	}
}

func TestResponseHeaderValuesFromHeadersText(t *testing.T) {
	resp := &Response{
		headersText: "Content-Type: application/json\nSet-Cookie: session=abc\nSet-Cookie: user=john",
	}
	values := resp.HeaderValues("set-cookie")
	if len(values) != 2 {
		t.Fatalf("expected 2 set-cookie values, got %d: %#v", len(values), values)
	}
	if values[0] != "session=abc" || values[1] != "user=john" {
		t.Fatalf("expected separate cookie values, got %#v", values)
	}
}

func TestResponseHeaderValuesFromMapFallback(t *testing.T) {
	resp := &Response{
		headers: map[string]string{
			"x-test": "a, b, c",
		},
		headerValues: map[string][]string{
			"x-test": {"a", "b", "c"},
		},
	}
	values := resp.HeaderValues("X-Test")
	if len(values) != 3 {
		t.Fatalf("expected 3 values, got %d: %#v", len(values), values)
	}
	if values[0] != "a" || values[1] != "b" || values[2] != "c" {
		t.Fatalf("expected split values, got %#v", values)
	}
}

func TestResponseHeaderValuesFromExtraHeaders(t *testing.T) {
	resp := &Response{
		extraHeaders: map[string]string{
			"set-cookie": "session=xyz",
		},
	}
	values := resp.HeaderValues("Set-Cookie")
	if len(values) != 1 {
		t.Fatalf("expected 1 value from extra headers, got %d: %#v", len(values), values)
	}
	if values[0] != "session=xyz" {
		t.Fatalf("expected value from extra headers, got %#v", values)
	}
}

func TestResponseHeadersArrayPreservesOrderFromHeadersText(t *testing.T) {
	resp := &Response{
		headersText:  "Content-Type: application/json\nX-Custom: value1\nX-Custom: value2\nServer: Polymux/1.0",
		headersArray: parseHeadersText("Content-Type: application/json\nX-Custom: value1\nX-Custom: value2\nServer: Polymux/1.0", nil),
	}
	entries := resp.HeadersArray()
	if len(entries) != 4 {
		t.Fatalf("expected 4 header entries, got %d", len(entries))
	}
	if entries[0].Name != "Content-Type" {
		t.Fatalf("expected first header name to preserve case, got %q", entries[0].Name)
	}
	if entries[1].Value != "value1" || entries[2].Value != "value2" {
		t.Fatalf("expected multiple header values preserved, got %#v", entries)
	}
}

func TestResponseHeadersArrayFallbackFromMap(t *testing.T) {
	resp := &Response{
		headers: map[string]string{
			"content-type": "application/json",
			"x-test":       "value",
		},
		headersArray: headersMapToArray(map[string]string{
			"content-type": "application/json",
			"x-test":       "value",
		}),
	}
	entries := resp.HeadersArray()
	if len(entries) != 2 {
		t.Fatalf("expected 2 header entries from map, got %d", len(entries))
	}
}

func TestResponseServerAddressInfo(t *testing.T) {
	resp := &Response{
		remoteIPAddress: "192.168.1.1",
		remotePort:      8080,
	}
	info := resp.ServerAddressInfo()
	if info.IPAddress != "192.168.1.1" {
		t.Fatalf("expected IP address, got %q", info.IPAddress)
	}
	if info.Port != 8080 {
		t.Fatalf("expected port 8080, got %d", info.Port)
	}
	str := resp.ServerAddress()
	if str != "192.168.1.1:8080" {
		t.Fatalf("expected server address string, got %q", str)
	}
}

func TestResponseServerAddressReturnsIPAddressOnly(t *testing.T) {
	resp := &Response{
		remoteIPAddress: "10.0.0.1",
		remotePort:      0,
	}
	str := resp.ServerAddress()
	if str != "10.0.0.1" {
		t.Fatalf("expected IP only string, got %q", str)
	}
}

func TestResponseServerAddressReturnsEmptyWhenEmpty(t *testing.T) {
	resp := &Response{}
	if str := resp.ServerAddress(); str != "" {
		t.Fatalf("expected empty string for missing address, got %q", str)
	}
}

func TestResponseSerializableRoundTrip(t *testing.T) {
	original := &Response{
		RequestID:         "req-123",
		FrameID:           "frame-1",
		LoaderID:          "loader-1",
		URLValue:          "https://example.com",
		StatusCode:        200,
		statusText:        "OK",
		headers:           map[string]string{"content-type": "application/json"},
		headersText:       "Content-Type: application/json\nX-Test: value",
		mimeType:          "application/json",
		remoteIPAddress:   "192.168.1.1",
		remotePort:        443,
		fromServiceWorker: true,
		securityDetails:   map[string]any{"subjectName": "example.com"},
		extraHeaders:      map[string]string{"set-cookie": "session=abc"},
		extraHeadersText:  "Set-Cookie: session=abc",
		headersArray: []HeaderEntry{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "X-Test", Value: "value"},
		},
	}

	sr := original.Serializable()
	if sr.RequestID != "req-123" {
		t.Fatalf("expected request ID preserved, got %q", sr.RequestID)
	}
	if sr.URL != "https://example.com" {
		t.Fatalf("expected URL preserved, got %q", sr.URL)
	}
	if sr.Status != 200 {
		t.Fatalf("expected status preserved, got %d", sr.Status)
	}
	if len(sr.HeadersArray) != 2 {
		t.Fatalf("expected headers array preserved, got %d", len(sr.HeadersArray))
	}

	reconstructed := NewResponseFromSerializable(nil, nil, sr)
	if reconstructed.RequestID != original.RequestID {
		t.Fatalf("expected reconstructed request ID match, got %q", reconstructed.RequestID)
	}
	if reconstructed.StatusCode != original.StatusCode {
		t.Fatalf("expected reconstructed status match, got %d", reconstructed.StatusCode)
	}
	if len(reconstructed.headersArray) != len(original.headersArray) {
		t.Fatalf("expected reconstructed headers array length match, got %d", len(reconstructed.headersArray))
	}
}

func TestResponseApplyExtraInfoMergesHeaders(t *testing.T) {
	resp := &Response{
		headersArray: []HeaderEntry{
			{Name: "Content-Type", Value: "text/html"},
		},
	}
	resp.applyExtraInfo(map[string]string{
		"set-cookie": "session=xyz",
	}, "Set-Cookie: session=xyz")

	if resp.extraHeaders["set-cookie"] != "session=xyz" {
		t.Fatalf("expected extra headers merged, got %#v", resp.extraHeaders)
	}
	if len(resp.headersArray) != 2 {
		t.Fatalf("expected headers array extended, got %d entries", len(resp.headersArray))
	}
	if resp.headersArray[1].Name != "Set-Cookie" {
		t.Fatalf("expected set-cookie header appended, got %#v", resp.headersArray)
	}
}

func TestNavigationTrackerExtraInfoBeforeResponseReceived(t *testing.T) {
	session := newFakeSession("session-1")
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	navID := page.beginNavigationCommand()

	tracker := NewNavigationResponseTracker(page, session, navID)
	tracker.SetExpectedLoaderID("loader-1")
	defer tracker.Dispose()

	session.dispatch("Network.responseReceivedExtraInfo", map[string]any{
		"requestId":   "req-1",
		"headers":     map[string]any{"set-cookie": "early=cookie"},
		"headersText": "Set-Cookie: early=cookie",
	})

	session.dispatch("Network.responseReceived", map[string]any{
		"requestId": "req-1",
		"frameId":   "root-1",
		"loaderId":  "loader-1",
		"type":      "Document",
		"response": map[string]any{
			"url":        "https://example.com",
			"status":     200,
			"statusText": "OK",
			"headers":    map[string]any{"Content-Type": "text/html"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp := tracker.NavigationCompleted(ctx)

	if resp == nil {
		t.Fatal("expected response after buffered extra-info")
	}
	if resp.extraHeaders["set-cookie"] != "early=cookie" {
		t.Fatalf("expected extra headers applied, got %#v", resp.extraHeaders)
	}
}

func TestNavigationTrackerExtraInfoAfterResponseSelected(t *testing.T) {
	session := newFakeSession("session-1")
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	navID := page.beginNavigationCommand()

	tracker := NewNavigationResponseTracker(page, session, navID)
	tracker.SetExpectedLoaderID("loader-1")
	defer tracker.Dispose()

	go func() {
		time.Sleep(10 * time.Millisecond)
		session.dispatch("Network.responseReceived", map[string]any{
			"requestId": "req-1",
			"frameId":   "root-1",
			"loaderId":  "loader-1",
			"type":      "Document",
			"response": map[string]any{
				"url":        "https://example.com",
				"status":     200,
				"statusText": "OK",
				"headers":    map[string]any{"Content-Type": "text/html"},
			},
		})
		time.Sleep(10 * time.Millisecond)
		session.dispatch("Network.responseReceivedExtraInfo", map[string]any{
			"requestId":   "req-1",
			"headers":     map[string]any{"set-cookie": "late=cookie"},
			"headersText": "Set-Cookie: late=cookie",
		})
		session.dispatch("Network.loadingFinished", map[string]any{"requestId": "req-1"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp := tracker.NavigationCompleted(ctx)

	if resp == nil {
		t.Fatal("expected response after responseReceived")
	}

	finishErr := resp.Finished()
	if finishErr != nil {
		t.Fatalf("expected no error from Finished(), got: %v", finishErr)
	}
	if resp.extraHeaders["set-cookie"] != "late=cookie" {
		t.Fatalf("expected late extra headers applied, got %#v", resp.extraHeaders)
	}
}

func TestNavigationTrackerIgnoresDisallowedURLs(t *testing.T) {
	session := newFakeSession("session-1")
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	navID := page.beginNavigationCommand()

	tracker := NewNavigationResponseTracker(page, session, navID)
	tracker.SetExpectedLoaderID("loader-1")
	defer tracker.Dispose()

	go func() {
		time.Sleep(5 * time.Millisecond)
		session.dispatch("Network.responseReceived", map[string]any{
			"requestId": "req-1",
			"frameId":   "root-1",
			"loaderId":  "loader-1",
			"type":      "Document",
			"response": map[string]any{
				"url":        "data:text/html,<h1>Hello</h1>",
				"status":     200,
				"statusText": "OK",
			},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp := tracker.NavigationCompleted(ctx)

	if resp != nil {
		t.Fatalf("expected nil response for data URL, got %#v", resp)
	}
}

func TestNavigationTrackerLoadingFailedResolvesWithError(t *testing.T) {
	session := newFakeSession("session-1")
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	navID := page.beginNavigationCommand()

	tracker := NewNavigationResponseTracker(page, session, navID)
	tracker.SetExpectedLoaderID("loader-1")
	defer tracker.Dispose()

	go func() {
		time.Sleep(5 * time.Millisecond)
		session.dispatch("Network.responseReceived", map[string]any{
			"requestId": "req-1",
			"frameId":   "root-1",
			"loaderId":  "loader-1",
			"type":      "Document",
			"response": map[string]any{
				"url":        "https://example.com",
				"status":     200,
				"statusText": "OK",
			},
		})
		time.Sleep(5 * time.Millisecond)
		session.dispatch("Network.loadingFailed", map[string]any{
			"requestId": "req-1",
			"errorText": "net::ERR_CONNECTION_REFUSED",
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp := tracker.NavigationCompleted(ctx)

	if resp == nil {
		t.Fatal("expected response even after loading failed")
	}
	err := resp.Finished()
	if err == nil {
		t.Fatal("expected error from Finished() after loadingFailed")
	}
	if err.Error() != "net::ERR_CONNECTION_REFUSED" {
		t.Fatalf("expected specific error text, got %q", err.Error())
	}
}
