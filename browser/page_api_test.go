package browser

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubSelectorWorld(session *fakeSession, ctxID int64, runtimeEval func(params any, result any) error) {
	session.respond("Page.createIsolatedWorld", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"executionContextId": ctxID,
		})
	})
	session.respond("Runtime.evaluate", runtimeEval)
}

func stubLocatorResolution(session *fakeSession, objectID string, backendNodeID int, callFn func(params any, result any) error) {
	stubSelectorWorld(session, 91, func(params any, result any) error {
		expr := params.(map[string]any)["expression"].(string)
		if strings.Contains(expr, "globalThis.__polymuxSelectorHelper =") || strings.Contains(expr, "globalThis.__polymuxSelectorHelper)") {
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		}
		return populateJSONResult(result, map[string]any{
			"result": map[string]any{"objectId": objectID},
		})
	})
	session.respond("DOM.describeNode", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"node": map[string]any{
				"nodeId":        1,
				"backendNodeId": backendNodeID,
			},
		})
	})
	if callFn != nil {
		session.respond("Runtime.callFunctionOn", callFn)
	}
}

func stubLocatorResolutionWithState(session *fakeSession, objectID string, backendNodeID int, visible, enabled, editable bool, tagName, inputType string) {
	stubLocatorResolution(session, objectID, backendNodeID, func(params any, result any) error {
		fn := params.(map[string]any)["functionDeclaration"].(string)
		switch {
		case strings.Contains(fn, "this.isConnected"):
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"visible":   visible,
						"enabled":   enabled,
						"editable":  editable,
						"tagName":   tagName,
						"inputType": inputType,
					},
				},
			})
		case strings.Contains(fn, "elementFromPoint"):
			// Actionability check (stability + occlusion). When the element
			// is visible we tell the caller it's stable and unoccluded so
			// click tests can proceed straight to Input.dispatchMouseEvent.
			if !visible {
				return populateJSONResult(result, map[string]any{
					"result": map[string]any{
						"value": map[string]any{"ok": false, "reason": "zero_size"},
					},
				})
			}
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"ok":     true,
						"x":      float64(10),
						"y":      float64(20),
						"width":  float64(40),
						"height": float64(30),
					},
				},
			})
		case strings.Contains(fn, "getBoundingClientRect"):
			if !visible {
				return populateJSONResult(result, map[string]any{
					"result": map[string]any{
						"value": nil,
					},
				})
			}
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"x":      10,
						"y":      20,
						"width":  40,
						"height": 30,
						"scale":  1,
					},
				},
			})
		default:
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		}
	})
}

func TestPageEvaluateReturnsValue(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		res := result.(*struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = "hello"
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	var value string
	if err := page.Evaluate(context.Background(), `'hello'`, &value); err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if value != "hello" {
		t.Fatalf("unexpected evaluate result: %q", value)
	}
}

func TestPageSetExtraHTTPHeadersAppliesToAllSessions(t *testing.T) {
	t.Parallel()

	main := newFakeSession("main")
	child := newFakeSession("child")
	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	page.adoptOopifSession(child, "child-frame")

	if err := page.SetExtraHTTPHeaders(context.Background(), map[string]string{"X-Test": "1"}); err != nil {
		t.Fatalf("SetExtraHTTPHeaders returned error: %v", err)
	}
	for _, session := range []*fakeSession{main, child} {
		methods := session.methods()
		joined := strings.Join(methods, ",")
		if !strings.Contains(joined, "Network.enable") || !strings.Contains(joined, "Network.setExtraHTTPHeaders") {
			t.Fatalf("expected network header setup for session %s, got %v", session.ID(), methods)
		}
	}
}

func TestPageConsoleListenerReceivesEvents(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	ch := make(chan ConsoleMessage, 1)
	page.AddConsoleListener(func(msg ConsoleMessage) {
		ch <- msg
	})

	session.dispatch("Runtime.consoleAPICalled", map[string]any{
		"type":      "log",
		"args":      []map[string]any{{"type": "string", "value": "hello"}},
		"timestamp": 1234,
	})

	select {
	case msg := <-ch:
		if msg.Text != "hello" || msg.Type != "log" {
			t.Fatalf("unexpected console message: %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for console message")
	}
}

func TestPageGoBackUsesHistoryEntry(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Page.getNavigationHistory", func(_ any, result any) error {
		res := result.(*struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID  int    `json:"id"`
				URL string `json:"url"`
			} `json:"entries"`
		})
		res.CurrentIndex = 1
		res.Entries = []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		}{
			{ID: 11, URL: "https://previous.test"},
			{ID: 12, URL: "https://current.test"},
		}
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "https://current.test"},
	})

	resp, err := page.GoBack(context.Background(), "", time.Second)
	if err != nil {
		t.Fatalf("GoBack returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response without network events, got %#v", resp)
	}
	if got := page.URL(); got != "https://previous.test" {
		t.Fatalf("expected previous URL, got %s", got)
	}
}

func TestPageGotoResponseExposesHeadersAndBody(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		setReadyStateResult(result, "complete")
		return nil
	})
	session.respond("Page.navigate", func(_ any, result any) error {
		res := result.(*pageNavigateResult)
		res.LoaderID = "loader-1"
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
					"headers": map[string]any{
						"Content-Type": "application/json",
						"X-Test":       "a, b",
					},
				},
			})
			session.dispatch("Network.responseReceivedExtraInfo", map[string]any{
				"requestId": "req-1",
				"headers": map[string]any{
					"set-cookie": "session=1",
				},
			})
			session.dispatch("Network.loadingFinished", map[string]any{"requestId": "req-1"})
			session.dispatch("Page.lifecycleEvent", map[string]any{"frameId": "root-1", "name": "load"})
		}()
		return nil
	})
	session.respond("Network.getResponseBody", func(_ any, result any) error {
		res := result.(*struct {
			Body          string `json:"body"`
			Base64Encoded bool   `json:"base64Encoded"`
		})
		res.Body = base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
		res.Base64Encoded = true
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
		t.Fatal("expected response")
	}
	// The test stub responds to Runtime.evaluate with readyState=complete,
	// so waitForMainLoadState short-circuits and doGoto returns as soon as
	// Network.responseReceived fires — before the spawned dispatch goroutine
	// has gotten to Network.responseReceivedExtraInfo. Sync on Finished so
	// extra headers and body bookkeeping are observable; this exercises the
	// per-Response listener lifetime that survives tracker.Dispose.
	if err := resp.Finished(); err != nil {
		t.Fatalf("Finished returned error: %v", err)
	}
	if resp.StatusText() != "OK" {
		t.Fatalf("unexpected status text: %q", resp.StatusText())
	}
	if got := resp.HeaderValue("x-test"); got != "a, b" {
		t.Fatalf("unexpected header value: %q", got)
	}
	if got := resp.AllHeaders()["set-cookie"]; got != "session=1" {
		t.Fatalf("expected extra headers to be surfaced, got %#v", resp.AllHeaders())
	}
	var body struct {
		Ok bool `json:"ok"`
	}
	if err := resp.JSON(&body); err != nil {
		t.Fatalf("JSON returned error: %v", err)
	}
	if !body.Ok {
		t.Fatal("expected parsed response body")
	}
}

func TestPageSnapshotIncludesAllFrames(t *testing.T) {
	t.Parallel()

	main := newFakeSession("main")
	child := newFakeSession("child")

	main.respond("Runtime.evaluate", func(_ any, result any) error {
		res := result.(*struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = "Main"
		return nil
	})
	child.respond("Runtime.evaluate", func(_ any, result any) error {
		res := result.(*struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = "Child"
		return nil
	})
	main.respond("DOM.getDocument", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"root": map[string]any{
				"nodeName": "#document",
				"children": []map[string]any{{
					"nodeName":      "HTML",
					"backendNodeId": 1,
					"children": []map[string]any{{
						"nodeName":      "BODY",
						"backendNodeId": 2,
						"children": []map[string]any{{
							"nodeName":      "A",
							"backendNodeId": 3,
							"attributes":    []string{"href", "/link"},
						}},
					}},
				}},
			},
		})
	})
	child.respond("DOM.getDocument", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"root": map[string]any{
				"nodeName": "#document",
				"children": []map[string]any{{
					"nodeName":      "HTML",
					"backendNodeId": 11,
					"children": []map[string]any{{
						"nodeName":      "BODY",
						"backendNodeId": 12,
						"children": []map[string]any{{
							"nodeName":      "BUTTON",
							"backendNodeId": 13,
						}},
					}},
				}},
			},
		})
	})
	main.respond("Accessibility.getFullAXTree", func(_ any, result any) error {
		res := result.(*struct {
			Nodes []map[string]any `json:"nodes"`
		})
		res.Nodes = []map[string]any{
			{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Main"}, "backendDOMNodeId": float64(1)},
			{"nodeId": "2", "parentId": "1", "role": map[string]any{"value": "link"}, "name": map[string]any{"value": "Link"}, "backendDOMNodeId": float64(3), "properties": []map[string]any{{"name": "url", "value": map[string]any{"value": "https://main.test/link"}}}},
		}
		return nil
	})
	child.respond("Accessibility.getFullAXTree", func(_ any, result any) error {
		res := result.(*struct {
			Nodes []map[string]any `json:"nodes"`
		})
		res.Nodes = []map[string]any{
			{"nodeId": "11", "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Child"}, "backendDOMNodeId": float64(11)},
			{"nodeId": "12", "parentId": "11", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Go"}, "backendDOMNodeId": float64(13)},
		}
		return nil
	})

	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "https://main.test"},
	})
	page.onFrameAttached("child-frame", "root-1", child)
	page.adoptOopifSession(child, "child-frame")
	page.onFrameNavigated(cdpFrame{ID: "child-frame", ParentID: "root-1", URL: "https://child.test"}, child)
	page.setFrameOwnerMetadata("child-frame", 42, "/html[1]/body[1]/iframe[1]")

	snapshot, err := page.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if got := snapshot.URLMap["0-3"]; got != "https://main.test/link" {
		t.Fatalf("unexpected URL map: %#v", snapshot.URLMap)
	}
	if got := snapshot.XPathMap["1-13"]; got != "/html[1]/body[1]/iframe[1]/html[1]/body[1]/button[1]" {
		t.Fatalf("expected exact child xpath prefix, got %#v", snapshot.XPathMap)
	}
	if len(snapshot.PerFrame) != 2 {
		t.Fatalf("expected per-frame payloads, got %#v", snapshot.PerFrame)
	}
	if !strings.Contains(snapshot.FormattedTree, "[0-3] link: Link") {
		t.Fatalf("expected a11y link node in formatted tree, got:\n%s", snapshot.FormattedTree)
	}
	if !strings.Contains(snapshot.FormattedTree, "[1-13] button: Go") {
		t.Fatalf("expected a11y button node in formatted tree, got:\n%s", snapshot.FormattedTree)
	}
}

func TestPageSnapshotWithOptionsScopesToSelector(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("DOM.getDocument", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"root": map[string]any{
				"nodeId":        1,
				"nodeType":      9,
				"nodeName":      "#document",
				"backendNodeId": 100,
				"children": []map[string]any{{
					"nodeId":        2,
					"nodeType":      1,
					"nodeName":      "HTML",
					"backendNodeId": 101,
					"children": []map[string]any{{
						"nodeId":        3,
						"nodeType":      1,
						"nodeName":      "BODY",
						"backendNodeId": 102,
						"children": []map[string]any{{
							"nodeId":        4,
							"nodeType":      1,
							"nodeName":      "BUTTON",
							"backendNodeId": 103,
						}},
					}},
				}},
			},
		})
	})
	session.respond("Runtime.evaluate", func(params any, result any) error {
		expr := params.(map[string]any)["expression"].(string)
		switch {
		case strings.Contains(expr, "selector.startsWith(\"/\")"):
			res := result.(*struct {
				Result struct {
					ObjectID string `json:"objectId"`
				} `json:"result"`
				ExceptionDetails *struct {
					Text string `json:"text"`
				} `json:"exceptionDetails"`
			})
			res.Result.ObjectID = "obj-1"
		case strings.Contains(expr, "document.title"):
			setReadyStateResult(result, "ignored")
		}
		return nil
	})
	session.respond("DOM.describeNode", func(params any, result any) error {
		switch p := params.(type) {
		case map[string]any:
			if _, ok := p["objectId"]; ok {
				res := result.(*struct {
					Node struct {
						BackendNodeID int `json:"backendNodeId"`
					} `json:"node"`
				})
				res.Node.BackendNodeID = 103
			}
		}
		return nil
	})
	session.respond("Accessibility.getFullAXTree", func(_ any, result any) error {
		res := result.(*struct {
			Nodes []map[string]any `json:"nodes"`
		})
		res.Nodes = []map[string]any{
			{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Main"}, "backendDOMNodeId": float64(100), "childIds": []any{"2"}},
			{"nodeId": "2", "parentId": "1", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Submit"}, "backendDOMNodeId": float64(103)},
		}
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "https://main.test"},
	})
	pierce := true
	snapshot, err := page.SnapshotWithOptions(context.Background(), SnapshotOptions{
		FocusSelector: `xpath=//button[1]`,
		PierceShadow:  &pierce,
	})
	if err != nil {
		t.Fatalf("SnapshotWithOptions returned error: %v", err)
	}
	if !strings.Contains(snapshot.FormattedTree, "[0-103] button: Submit") {
		t.Fatalf("expected scoped button in snapshot, got:\n%s", snapshot.FormattedTree)
	}
	if len(snapshot.PerFrame) != 1 || snapshot.PerFrame[0].FrameID != "root-1" {
		t.Fatalf("expected single-frame scoped snapshot, got %#v", snapshot.PerFrame)
	}
}

func TestPageActiveElementXPathAndResolveXPathForLocation(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(params any, result any) error {
		expr := params.(map[string]any)["expression"].(string)
		switch {
		case strings.Contains(expr, "document.hasFocus"):
			res := result.(*struct {
				Result struct {
					Value bool `json:"value"`
				} `json:"result"`
			})
			res.Result.Value = true
		case strings.Contains(expr, "activeElement"):
			res := result.(*struct {
				Result struct {
					ObjectID string `json:"objectId"`
				} `json:"result"`
			})
			res.Result.ObjectID = "obj-active"
		case strings.Contains(expr, "window.scrollX"):
			res := result.(*struct {
				Result struct {
					Value struct {
						SX int `json:"sx"`
						SY int `json:"sy"`
					} `json:"value"`
				} `json:"result"`
			})
			res.Result.Value.SX = 0
			res.Result.Value.SY = 0
		}
		return nil
	})
	session.respond("DOM.resolveNode", func(_ any, result any) error {
		switch res := result.(type) {
		case *struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}:
			res.Object.ObjectID = "obj-node"
		}
		return nil
	})
	session.respond("Runtime.callFunctionOn", func(params any, result any) error {
		fn := params.(map[string]any)["functionDeclaration"].(string)
		switch {
		case strings.Contains(fn, "getBoundingClientRect"):
			res := result.(*struct {
				Result struct {
					Value struct {
						Left int `json:"left"`
						Top  int `json:"top"`
					} `json:"value"`
				} `json:"result"`
			})
			res.Result.Value.Left = 0
			res.Result.Value.Top = 0
		default:
			res := result.(*struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			})
			res.Result.Value = "/html[1]/body[1]/button[1]"
		}
		return nil
	})
	session.respond("DOM.getNodeForLocation", func(_ any, result any) error {
		res := result.(*struct {
			BackendNodeID int    `json:"backendNodeId"`
			FrameID       string `json:"frameId"`
		})
		res.BackendNodeID = 103
		res.FrameID = "root-1"
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "https://main.test"},
	})

	activeXPath, err := page.ActiveElementXPath(context.Background())
	if err != nil {
		t.Fatalf("ActiveElementXPath returned error: %v", err)
	}
	if activeXPath != "/html[1]/body[1]/button[1]" {
		t.Fatalf("unexpected active element xpath: %q", activeXPath)
	}

	location, err := page.ResolveXPathForLocation(context.Background(), 10, 20)
	if err != nil {
		t.Fatalf("ResolveXPathForLocation returned error: %v", err)
	}
	if location == nil || location.AbsoluteXPath != "/html[1]/body[1]/button[1]" {
		t.Fatalf("unexpected location result: %#v", location)
	}
}

func TestLocatorSetInputFilesUsesCDP(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "upload.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	session := newFakeSession("session-1")
	stubLocatorResolution(session, "obj-1", 101, nil)

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("input[type=file]").SetInputFiles(context.Background(), path); err != nil {
		t.Fatalf("SetInputFiles returned error: %v", err)
	}

	methods := strings.Join(session.methods(), ",")
	if !strings.Contains(methods, "Runtime.evaluate") || !strings.Contains(methods, "DOM.setFileInputFiles") {
		t.Fatalf("expected DOM file input methods, got %v", session.methods())
	}
}

func TestLocatorSetInputFilesSupportsFrameHop(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "upload.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	main := newFakeSession("main")
	child := newFakeSession("child")
	stubLocatorResolution(main, "iframe-obj", 42, nil)
	stubLocatorResolution(child, "input-obj", 84, nil)

	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	page.onFrameAttached("child-frame", "root-1", child)
	page.adoptOopifSession(child, "child-frame")
	page.onFrameNavigated(cdpFrame{ID: "child-frame", ParentID: "root-1", URL: "https://child.test"}, child)
	page.setFrameOwnerMetadata("child-frame", 42, "/html[1]/body[1]/iframe[1]")

	if err := page.Locator("iframe >> input[type=file]").SetInputFiles(context.Background(), path); err != nil {
		t.Fatalf("SetInputFiles returned error: %v", err)
	}

	if joined := strings.Join(child.methods(), ","); !strings.Contains(joined, "DOM.setFileInputFiles") {
		t.Fatalf("expected child session file input call, got %v", child.methods())
	}
	if joined := strings.Join(main.methods(), ","); strings.Contains(joined, "DOM.setFileInputFiles") {
		t.Fatalf("expected main session to avoid DOM.setFileInputFiles, got %v", main.methods())
	}
}

func TestPageWaitForSelectorSupportsXPath(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubSelectorWorld(session, 91, func(params any, result any) error {
		expr := params.(map[string]any)["expression"].(string)
		switch {
		case strings.Contains(expr, "globalThis.__polymuxSelectorHelper ="), strings.Contains(expr, "globalThis.__polymuxSelectorHelper)"):
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		case strings.Contains(expr, "matchesState("):
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		default:
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": "complete"},
			})
		}
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	ok, err := page.WaitForSelector(context.Background(), `xpath=//button[normalize-space(.)="Submit"]`, WaitForSelectorOptions{
		State:   SelectorStateVisible,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("WaitForSelector returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected xpath selector to match")
	}
}

func TestLocatorSelectorHelperInstalledOncePerWorld(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	installCalls := 0
	stubSelectorWorld(session, 91, func(params any, result any) error {
		expr := params.(map[string]any)["expression"].(string)
		switch {
		case strings.Contains(expr, "globalThis.__polymuxSelectorHelper ="), strings.Contains(expr, "globalThis.__polymuxSelectorHelper)"):
			installCalls++
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		default:
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"objectId": "obj-1"},
			})
		}
	})
	session.respond("DOM.describeNode", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"node": map[string]any{
				"nodeId":        1,
				"backendNodeId": 101,
			},
		})
	})
	session.respond("Runtime.callFunctionOn", func(params any, result any) error {
		fn := params.(map[string]any)["functionDeclaration"].(string)
		switch {
		case strings.Contains(fn, "this.isConnected"):
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"visible":   true,
						"enabled":   true,
						"editable":  false,
						"tagName":   "button",
						"inputType": "",
					},
				},
			})
		case strings.Contains(fn, "getBoundingClientRect"):
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{
					"value": map[string]any{
						"x":      10,
						"y":      20,
						"width":  40,
						"height": 30,
						"scale":  1,
					},
				},
			})
		default:
			return populateJSONResult(result, map[string]any{
				"result": map[string]any{"value": true},
			})
		}
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("button").Focus(context.Background()); err != nil {
		t.Fatalf("Focus returned error: %v", err)
	}
	if _, err := page.Locator("button").BoundingBox(context.Background()); err != nil {
		t.Fatalf("BoundingBox returned error: %v", err)
	}
	if installCalls != 1 {
		t.Fatalf("expected selector helper install once, got %d", installCalls)
	}
}

func TestLocatorClickUsesMouseInputAtCentroid(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "button", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("button").Click(context.Background()); err != nil {
		t.Fatalf("Click returned error: %v", err)
	}

	if joined := strings.Join(session.methods(), ","); !strings.Contains(joined, "Input.dispatchMouseEvent") {
		t.Fatalf("expected click to use mouse input, got %v", session.methods())
	}
}

func TestLocatorPressFocusesThenUsesKeyPress(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolution(session, "obj-1", 101, func(_ any, result any) error {
		res := result.(*struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = true
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("input").Press(context.Background(), "Enter"); err != nil {
		t.Fatalf("Press returned error: %v", err)
	}

	methods := strings.Join(session.methods(), ",")
	if !strings.Contains(methods, "Runtime.evaluate") || strings.Count(methods, "Input.dispatchKeyEvent") < 2 {
		t.Fatalf("expected focus plus keydown/up, got %v", session.methods())
	}
}

func TestLocatorCheckAndUncheckRespectCurrentState(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	var checked bool
	stubLocatorResolution(session, "obj-1", 101, func(params any, result any) error {
		fn := params.(map[string]any)["functionDeclaration"].(string)
		res := result.(*struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		})
		switch {
		case strings.Contains(fn, "this.isConnected"):
			res.Result.Value = map[string]any{
				"visible":   true,
				"enabled":   true,
				"editable":  false,
				"tagName":   "input",
				"inputType": "checkbox",
			}
		case strings.Contains(fn, "getBoundingClientRect"):
			res.Result.Value = map[string]any{
				"x":      10,
				"y":      20,
				"width":  40,
				"height": 30,
				"scale":  1,
			}
		case strings.Contains(fn, "this.checked = true"):
			checked = true
			res.Result.Value = true
		case strings.Contains(fn, "this.checked = false"):
			checked = false
			res.Result.Value = true
		case strings.Contains(fn, "return !!this.checked"):
			res.Result.Value = checked
		default:
			res.Result.Value = true
		}
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("input[type=checkbox]").Check(context.Background()); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !checked {
		t.Fatal("expected locator to become checked")
	}
	if err := page.Locator("input[type=checkbox]").Uncheck(context.Background()); err != nil {
		t.Fatalf("Uncheck returned error: %v", err)
	}
	if checked {
		t.Fatal("expected locator to become unchecked")
	}
}

func TestPageScreenshotAppliesCleanupAcrossFrames(t *testing.T) {
	t.Parallel()

	main := newFakeSession("main")
	child := newFakeSession("child")
	main.respond("Page.captureScreenshot", func(_ any, result any) error {
		res := result.(*struct {
			Data string `json:"data"`
		})
		res.Data = base64.StdEncoding.EncodeToString([]byte("png"))
		return nil
	})

	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	page.onFrameAttached("child-frame", "root-1", child)
	page.adoptOopifSession(child, "child-frame")
	page.onFrameNavigated(cdpFrame{ID: "child-frame", ParentID: "root-1", URL: "https://child.test"}, child)

	if _, err := page.Screenshot(context.Background(), ScreenshotOptions{
		Animations:     ScreenshotAnimationsDisabled,
		Caret:          ScreenshotCaretHide,
		OmitBackground: true,
	}); err != nil {
		t.Fatalf("Screenshot returned error: %v", err)
	}

	if joined := strings.Join(main.methods(), ","); !strings.Contains(joined, "Emulation.setDefaultBackgroundColorOverride") {
		t.Fatalf("expected main session background override, got %v", main.methods())
	}
	if joined := strings.Join(child.methods(), ","); !strings.Contains(joined, "Runtime.evaluate") {
		t.Fatalf("expected child frame cleanup styles, got %v", child.methods())
	}
}

func TestPageScreenshotRejectsInvalidClip(t *testing.T) {
	t.Parallel()

	page := newPage(newFakeConn(), newFakeSession("session-1"), "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if _, err := page.Screenshot(context.Background(), ScreenshotOptions{
		Clip: &ScreenshotClip{X: math.NaN(), Y: 0, Width: 10, Height: 10},
	}); err == nil {
		t.Fatal("expected invalid clip to return error")
	}
}

func TestFrameScreenshotUsesOwningSession(t *testing.T) {
	t.Parallel()

	main := newFakeSession("main")
	child := newFakeSession("child")
	child.respond("Page.captureScreenshot", func(_ any, result any) error {
		res := result.(*struct {
			Data string `json:"data"`
		})
		res.Data = base64.StdEncoding.EncodeToString([]byte("frame"))
		return nil
	})

	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	page.onFrameAttached("child-frame", "root-1", child)
	page.adoptOopifSession(child, "child-frame")
	page.onFrameNavigated(cdpFrame{ID: "child-frame", ParentID: "root-1", URL: "https://child.test"}, child)

	if _, err := page.Frame("child-frame").Screenshot(context.Background(), ScreenshotOptions{}); err != nil {
		t.Fatalf("Frame screenshot returned error: %v", err)
	}
	if joined := strings.Join(child.methods(), ","); !strings.Contains(joined, "Page.captureScreenshot") {
		t.Fatalf("expected child session capture, got %v", child.methods())
	}
	if joined := strings.Join(main.methods(), ","); strings.Contains(joined, "Page.captureScreenshot") {
		t.Fatalf("expected main session to stay unused for child frame screenshot, got %v", main.methods())
	}
}

func TestComputeScreenshotScale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     ScreenshotScaleMode
		dpr      float64
		expected float64
	}{
		{"device mode returns 0", ScreenshotScaleDevice, 2.0, 0},
		{"empty mode returns 0", "", 2.0, 0},
		{"css mode with dpr 2 returns 0.5", ScreenshotScaleCSS, 2.0, 0.5},
		{"css mode with dpr 1 returns 1", ScreenshotScaleCSS, 1.0, 1.0},
		{"css mode with dpr 3 returns 0.33 (clamped to 0.1)", ScreenshotScaleCSS, 3.0, 0.3333333333333333},
		{"css mode with dpr 0.5 returns 2 (clamped to 2)", ScreenshotScaleCSS, 0.5, 2.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			session := newFakeSession("session-1")
			session.respond("Runtime.evaluate", func(params any, result any) error {
				p := params.(map[string]any)
				expr := p["expression"].(string)
				if strings.Contains(expr, "devicePixelRatio") {
					return populateJSONResult(result, map[string]any{
						"result": map[string]any{"value": tc.dpr},
					})
				}
				return populateJSONResult(result, map[string]any{
					"result": map[string]any{"value": true},
				})
			})

			page := newPage(newFakeConn(), session, "target-1", frameNode{
				Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
			})

			scale := computeScreenshotScale(context.Background(), page, tc.mode)
			if tc.mode != ScreenshotScaleCSS {
				if scale != 0 {
					t.Fatalf("expected 0 for non-css mode, got %v", scale)
				}
			} else {
				if math.Abs(scale-tc.expected) > 0.001 {
					t.Fatalf("expected %v, got %v", tc.expected, scale)
				}
			}
		})
	}
}

func TestApplyStyleToFramesWithToken(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Runtime.evaluate", func(params any, result any) error {
		return populateJSONResult(result, map[string]any{
			"result": map[string]any{"value": true},
		})
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	frames := []*Frame{page.MainFrame()}
	cleanup := applyStyleToFrames(context.Background(), frames, "body { background: red; }", "test")
	methods := session.methods()
	found := false
	for _, m := range methods {
		if m == "Runtime.evaluate" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected Runtime.evaluate to be called for style injection")
	}

	cleanup()
}

func TestParseMaskRectResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		expected *maskRect
	}{
		{"valid rect", map[string]any{"x": 10.0, "y": 20.0, "width": 100.0, "height": 50.0, "rootToken": "abc123"}, &maskRect{X: 10, Y: 20, Width: 100, Height: 50, RootToken: "abc123"}},
		{"rect without rootToken", map[string]any{"x": 0.0, "y": 0.0, "width": 50.0, "height": 50.0}, &maskRect{X: 0, Y: 0, Width: 50, Height: 50, RootToken: ""}},
		{"zero dimensions returns nil", map[string]any{"x": 0.0, "y": 0.0, "width": 0.0, "height": 50.0}, nil},
		{"negative dimensions returns nil", map[string]any{"x": 0.0, "y": 0.0, "width": -10.0, "height": 50.0}, nil},
		{"nil input returns nil", nil, nil},
		{"non-map input returns nil", "not a map", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseMaskRectResult(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.expected == nil {
				if result != nil {
					t.Fatalf("expected nil, got %+v", result)
				}
			} else {
				if result == nil {
					t.Fatal("expected result, got nil")
				}
				if result.X != tc.expected.X || result.Y != tc.expected.Y || result.Width != tc.expected.Width || result.Height != tc.expected.Height {
					t.Fatalf("expected %+v, got %+v", tc.expected, result)
				}
				if result.RootToken != tc.expected.RootToken {
					t.Fatalf("expected RootToken %q, got %q", tc.expected.RootToken, result.RootToken)
				}
			}
		})
	}
}

func TestFormatMaskRectsJSON(t *testing.T) {
	t.Parallel()

	rects := []maskRect{
		{X: 10, Y: 20, Width: 100, Height: 50, RootToken: "abc"},
		{X: 0, Y: 0, Width: 50, Height: 50, RootToken: ""},
	}
	result := formatMaskRectsJSON(rects)
	if !strings.Contains(result, `"abc"`) {
		t.Fatal("expected rootToken to be quoted")
	}
	if !strings.Contains(result, "10") || !strings.Contains(result, "20") {
		t.Fatal("expected coordinates in output")
	}
}

func TestLocatorScreenshotWithScale(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	session.respond("Page.captureScreenshot", func(_ any, result any) error {
		res := result.(*struct {
			Data string `json:"data"`
		})
		res.Data = base64.StdEncoding.EncodeToString([]byte("png"))
		return nil
	})

	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	clip := &ScreenshotClip{X: 10, Y: 20, Width: 100, Height: 50, Scale: 0}
	_, err := page.Screenshot(context.Background(), ScreenshotOptions{
		Clip:  clip,
		Scale: ScreenshotScaleDevice,
	})
	if err != nil {
		t.Fatalf("Screenshot returned error: %v", err)
	}
}

func TestLocatorClickFailsOnHiddenElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, false, true, false, "button", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("button").Click(context.Background())
	if err == nil {
		t.Fatal("expected error for hidden element")
	}
	if !errors.Is(err, ErrElementNotVisible) {
		t.Fatalf("expected ErrElementNotVisible, got %v", err)
	}
}

func TestLocatorBoundingBoxFailsOnHiddenElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, false, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	_, err := page.Locator("div").BoundingBox(context.Background())
	if err == nil {
		t.Fatal("expected error for hidden element")
	}
	if !errors.Is(err, ErrElementNotVisible) {
		t.Fatalf("expected ErrElementNotVisible, got %v", err)
	}
}

func TestLocatorHoverFailsOnHiddenElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, false, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("div").Hover(context.Background())
	if err == nil {
		t.Fatal("expected error for hidden element")
	}
	if !errors.Is(err, ErrElementNotVisible) {
		t.Fatalf("expected ErrElementNotVisible, got %v", err)
	}
}

func TestLocatorFillRejectsNonEditableElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("div").Fill(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for non-editable element")
	}
	if !errors.Is(err, ErrElementNotEditable) {
		t.Fatalf("expected ErrElementNotEditable, got %v", err)
	}
}

func TestLocatorFillAcceptsEditableInputElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, true, "input", "text")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("input").Fill(context.Background(), "test value")
	if err != nil {
		t.Fatalf("expected no error for editable input, got %v", err)
	}
}

func TestLocatorSelectOptionRejectsNonSelectElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("div").SelectOption(context.Background(), "option1")
	if err == nil {
		t.Fatal("expected error for non-select element")
	}
	if !errors.Is(err, ErrNotSelectElement) {
		t.Fatalf("expected ErrNotSelectElement, got %v", err)
	}
}

func TestLocatorCheckRejectsNonCheckableElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("div").Check(context.Background())
	if err == nil {
		t.Fatal("expected error for non-checkable element")
	}
	if !errors.Is(err, ErrNotCheckable) {
		t.Fatalf("expected ErrNotCheckable, got %v", err)
	}
}

func TestLocatorUncheckRejectsNonCheckableElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("div").Uncheck(context.Background())
	if err == nil {
		t.Fatal("expected error for non-checkable element")
	}
	if !errors.Is(err, ErrNotCheckable) {
		t.Fatalf("expected ErrNotCheckable, got %v", err)
	}
}

func TestLocatorIsVisibleReturnsFalseForHiddenElements(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, false, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	visible, err := page.Locator("div").IsVisible(context.Background())
	if err != nil {
		t.Fatalf("IsVisible returned error: %v", err)
	}
	if visible {
		t.Fatal("expected hidden element to report IsVisible=false")
	}
}

func TestLocatorIsVisibleReturnsTrueForVisibleElements(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "div", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	visible, err := page.Locator("div").IsVisible(context.Background())
	if err != nil {
		t.Fatalf("IsVisible returned error: %v", err)
	}
	if !visible {
		t.Fatal("expected visible element to report IsVisible=true")
	}
}

func TestPageSendCDPForwardsCommandsToMainSession(t *testing.T) {
	t.Parallel()

	session := newFakeSession("main")
	session.respond("Browser.getVersion", func(_ any, result any) error {
		res := result.(*struct {
			Product string `json:"product"`
		})
		res.Product = "Chrome/120.0.0.0"
		return nil
	})

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	var res struct {
		Product string `json:"product"`
	}
	if err := page.SendCDP(context.Background(), "Browser.getVersion", nil, &res); err != nil {
		t.Fatalf("SendCDP returned error: %v", err)
	}
	if res.Product != "Chrome/120.0.0.0" {
		t.Fatalf("unexpected version: %q", res.Product)
	}
}

func TestPageSendCDPToFrameTargetsCorrectSession(t *testing.T) {
	t.Parallel()

	main := newFakeSession("main")
	child := newFakeSession("child")

	main.respond("DOM.getDocument", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"root": map[string]any{"nodeId": 1},
		})
	})
	child.respond("DOM.getDocument", func(_ any, result any) error {
		return populateJSONResult(result, map[string]any{
			"root": map[string]any{"nodeId": 2},
		})
	})

	page := newPage(newFakeConn(), main, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})
	page.onFrameAttached("child-frame", "root-1", child)
	page.adoptOopifSession(child, "child-frame")
	page.onFrameNavigated(cdpFrame{ID: "child-frame", ParentID: "root-1", URL: "https://child.test"}, child)

	var mainRes struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := page.SendCDPToFrame(context.Background(), "root-1", "DOM.getDocument", nil, &mainRes); err != nil {
		t.Fatalf("SendCDPToFrame for main frame returned error: %v", err)
	}
	if mainRes.Root.NodeID != 1 {
		t.Fatalf("expected main frame node ID 1, got %d", mainRes.Root.NodeID)
	}

	var childRes struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := page.SendCDPToFrame(context.Background(), "child-frame", "DOM.getDocument", nil, &childRes); err != nil {
		t.Fatalf("SendCDPToFrame for child frame returned error: %v", err)
	}
	if childRes.Root.NodeID != 2 {
		t.Fatalf("expected child frame node ID 2, got %d", childRes.Root.NodeID)
	}

	mainMethods := strings.Join(main.methods(), ",")
	if !strings.Contains(mainMethods, "DOM.getDocument") {
		t.Fatalf("expected main session to receive DOM.getDocument, got %v", main.methods())
	}
	childMethods := strings.Join(child.methods(), ",")
	if !strings.Contains(childMethods, "DOM.getDocument") {
		t.Fatalf("expected child session to receive DOM.getDocument, got %v", child.methods())
	}
}

func TestPageTapDispatchesTouchEvents(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Tap(context.Background(), 100, 200); err != nil {
		t.Fatalf("Tap returned error: %v", err)
	}

	methods := strings.Join(session.methods(), ",")
	if !strings.Contains(methods, "Input.dispatchTouchEvent") {
		t.Fatalf("expected Input.dispatchTouchEvent calls, got %v", session.methods())
	}
}

func TestLocatorTapUsesTouchInput(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, true, true, false, "button", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	if err := page.Locator("button").Tap(context.Background()); err != nil {
		t.Fatalf("Tap returned error: %v", err)
	}

	methods := strings.Join(session.methods(), ",")
	if !strings.Contains(methods, "Input.dispatchTouchEvent") {
		t.Fatalf("expected Tap to use touch events, got %v", session.methods())
	}
}

func TestLocatorTapFailsOnHiddenElement(t *testing.T) {
	t.Parallel()

	session := newFakeSession("session-1")
	stubLocatorResolutionWithState(session, "obj-1", 101, false, true, false, "button", "")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	err := page.Locator("button").Tap(context.Background())
	if err == nil {
		t.Fatal("expected error for hidden element")
	}
	if !errors.Is(err, ErrElementNotVisible) {
		t.Fatalf("expected ErrElementNotVisible, got %v", err)
	}
}

func TestTouchStartMoveEndSequence(t *testing.T) {
	session := newFakeSession("session-1")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	touch := page.Touch()
	if err := touch.TouchStart(context.Background(), TouchPoint{X: 10, Y: 20}); err != nil {
		t.Fatalf("TouchStart returned error: %v", err)
	}
	if err := touch.TouchMove(context.Background(), TouchPoint{X: 30, Y: 40}); err != nil {
		t.Fatalf("TouchMove returned error: %v", err)
	}
	if err := touch.TouchEnd(context.Background()); err != nil {
		t.Fatalf("TouchEnd returned error: %v", err)
	}

	calls := session.methodParams()
	var touchCalls []methodCall
	for _, c := range calls {
		if c.Method == "Input.dispatchTouchEvent" {
			touchCalls = append(touchCalls, c)
		}
	}
	if len(touchCalls) != 3 {
		t.Fatalf("expected 3 touch event calls, got %d", len(touchCalls))
	}
	if touchCalls[0].Params["type"] != "touchStart" {
		t.Fatalf("expected touchStart, got %v", touchCalls[0])
	}
	if touchCalls[1].Params["type"] != "touchMove" {
		t.Fatalf("expected touchMove, got %v", touchCalls[1])
	}
	if touchCalls[2].Params["type"] != "touchEnd" {
		t.Fatalf("expected touchEnd, got %v", touchCalls[2])
	}
}

func TestTouchCancelDispatchesCorrectEvent(t *testing.T) {
	session := newFakeSession("session-1")

	page := newPage(newFakeConn(), session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	touch := page.Touch()
	if err := touch.TouchCancel(context.Background()); err != nil {
		t.Fatalf("TouchCancel returned error: %v", err)
	}

	calls := session.methodParams()
	var touchCalls []methodCall
	for _, c := range calls {
		if c.Method == "Input.dispatchTouchEvent" {
			touchCalls = append(touchCalls, c)
		}
	}
	if len(touchCalls) != 1 {
		t.Fatalf("expected 1 touch call, got %d", len(touchCalls))
	}
	if touchCalls[0].Params["type"] != "touchCancel" {
		t.Fatalf("expected touchCancel, got %v", touchCalls[0])
	}
}
