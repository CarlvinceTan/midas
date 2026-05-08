package browser

import (
	"context"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/cdp"
)

func TestPageWaitForMainLoadStateFollowsCurrentMainFrame(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		setReadyStateResult(result, "loading")
		return nil
	})
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- page.waitForMainLoadState(ctx, LoadStateLoad, page.beginNavigationCommand())
	}()

	time.Sleep(50 * time.Millisecond)
	page.onFrameNavigated(cdpFrame{ID: "root-2", URL: "https://example.com"}, session)
	session.dispatch("Page.lifecycleEvent", lifecycleEvent{
		FrameID: "root-1",
		Name:    "load",
	})
	select {
	case err := <-done:
		t.Fatalf("wait completed too early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	session.dispatch("Page.lifecycleEvent", lifecycleEvent{
		FrameID: "root-2",
		Name:    "load",
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for load state")
	}
}

func TestPageGotoSendsNavigateAndUpdatesURL(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		setReadyStateResult(result, "complete")
		return nil
	})
	session.respond("Page.navigate", func(params any, result any) error {
		res := result.(*pageNavigateResult)
		res.FrameID = "root-1"
		res.LoaderID = "loader-1"
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	resp, err := page.doGoto(context.Background(), "https://example.com", LoadStateLoad, time.Second)
	if err != nil {
		t.Fatalf("goto returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response without network events, got %#v", resp)
	}
	if got := page.URL(); got != "https://example.com" {
		t.Fatalf("expected updated url, got %s", got)
	}
	if got := session.lastMethod(); got != "Runtime.evaluate" {
		t.Fatalf("expected readyState probe to be last call, got %s", got)
	}
}

func TestPageGotoTracksNavigationResponse(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		setReadyStateResult(result, "complete")
		return nil
	})
	session.respond("Page.navigate", func(_ any, result any) error {
		res := result.(*pageNavigateResult)
		res.FrameID = "root-1"
		res.LoaderID = "loader-1"
		go func() {
			time.Sleep(10 * time.Millisecond)
			session.dispatch("Network.responseReceived", map[string]any{
				"requestId": "req-1",
				"frameId":   "root-1",
				"loaderId":  "loader-1",
				"type":      "Document",
				"response": map[string]any{
					"url":    "https://example.com",
					"status": 200,
				},
			})
			session.dispatch("Network.loadingFinished", map[string]any{
				"requestId": "req-1",
			})
			session.dispatch("Page.lifecycleEvent", map[string]any{
				"frameId": "root-1",
				"name":    "load",
			})
		}()
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	resp, err := page.doGoto(context.Background(), "https://example.com", LoadStateLoad, time.Second)
	if err != nil {
		t.Fatalf("goto returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.URL() != "https://example.com" || resp.Status() != 200 {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if err := resp.Finished(); err != nil {
		t.Fatalf("response should be finished cleanly: %v", err)
	}
}

var _ sessionLike = (*fakeSession)(nil)
var _ connLike = (*fakeConn)(nil)
var _ cdp.Session = (*fakeSession)(nil)
