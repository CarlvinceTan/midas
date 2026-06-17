package browser

import (
	"context"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/internal/cdp"
)

func TestContextBootstrapAttachesOnlyTopLevelWebTargets(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	conn.targets = []cdp.TargetInfo{
		{TargetID: "page-1", Type: "page", URL: "https://example.com"},
		{TargetID: "iframe-1", Type: "iframe", URL: "https://example.com/frame"},
		{TargetID: "worker-1", Type: "worker", URL: "https://example.com/worker"},
		{TargetID: "chrome-1", Type: "page", URL: "chrome://settings"},
	}

	pageSession := newFakeSession("session-page-1")
	pageSession.respond("Page.getFrameTree", func(_ any, result any) error {
		res := result.(*struct {
			FrameTree frameNode `json:"frameTree"`
		})
		res.FrameTree = frameNode{Frame: cdpFrame{ID: "root-1", URL: "https://example.com"}}
		return nil
	})

	iframeSession := newFakeSession("session-iframe-1")
	iframeSession.respond("Page.getFrameTree", func(_ any, result any) error {
		res := result.(*struct {
			FrameTree frameNode `json:"frameTree"`
		})
		res.FrameTree = frameNode{Frame: cdpFrame{ID: "child-1", ParentID: "root-1", URL: "https://example.com/frame"}}
		return nil
	})
	workerSession := newFakeSession("session-worker-1")
	chromeSession := newFakeSession("session-chrome-1")

	conn.attach = func(targetID string) (sessionLike, error) {
		switch targetID {
		case "page-1":
			conn.addSession("session-page-1", pageSession)
			conn.dispatch("Target.attachedToTarget", map[string]any{
				"sessionId": "session-page-1",
				"targetInfo": map[string]any{
					"targetId": "page-1",
					"type":     "page",
					"url":      "https://example.com",
				},
			})
			return pageSession, nil
		case "iframe-1":
			conn.addSession("session-iframe-1", iframeSession)
			conn.dispatch("Target.attachedToTarget", map[string]any{
				"sessionId": "session-iframe-1",
				"targetInfo": map[string]any{
					"targetId": "iframe-1",
					"type":     "iframe",
					"url":      "https://example.com/frame",
				},
			})
			return iframeSession, nil
		case "worker-1":
			conn.addSession("session-worker-1", workerSession)
			conn.dispatch("Target.attachedToTarget", map[string]any{
				"sessionId": "session-worker-1",
				"targetInfo": map[string]any{
					"targetId": "worker-1",
					"type":     "worker",
					"url":      "https://example.com/worker",
				},
			})
			return workerSession, nil
		case "chrome-1":
			conn.addSession("session-chrome-1", chromeSession)
			conn.dispatch("Target.attachedToTarget", map[string]any{
				"sessionId": "session-chrome-1",
				"targetInfo": map[string]any{
					"targetId": "chrome-1",
					"type":     "page",
					"url":      "chrome://settings",
				},
			})
			return chromeSession, nil
		default:
			t.Fatalf("unexpected attach target: %s", targetID)
			return nil, nil
		}
	}

	ctx := newContext(conn)
	if err := ctx.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap returned error: %v", err)
	}

	pages := ctx.Pages()
	if len(pages) != 1 {
		t.Fatalf("expected 1 top-level page, got %d", len(pages))
	}
	if pages[0].TargetID() != "page-1" {
		t.Fatalf("unexpected page target: %s", pages[0].TargetID())
	}
}

func TestContextNewPageReturnsAttachedPage(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	session := newFakeSession("session-new")
	session.respond("Page.getFrameTree", func(_ any, result any) error {
		res := result.(*struct {
			FrameTree frameNode `json:"frameTree"`
		})
		res.FrameTree = frameNode{Frame: cdpFrame{ID: "root-new", URL: "about:blank"}}
		return nil
	})
	conn.addSession("session-new", session)
	conn.send = func(method string, params any, result any) error {
		if method == "Target.createTarget" {
			res := result.(*struct {
				TargetID string `json:"targetId"`
			})
			res.TargetID = "target-new"
			go func() {
				time.Sleep(25 * time.Millisecond)
				conn.dispatch("Target.attachedToTarget", map[string]any{
					"sessionId": "session-new",
					"targetInfo": map[string]any{
						"targetId": "target-new",
						"type":     "page",
						"url":      "about:blank",
					},
				})
			}()
		}
		return nil
	}

	ctx := newContext(conn)
	if err := ctx.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap returned error: %v", err)
	}
	page, err := ctx.NewPage(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("NewPage returned error: %v", err)
	}
	if page.TargetID() != "target-new" {
		t.Fatalf("unexpected target id: %s", page.TargetID())
	}
}
