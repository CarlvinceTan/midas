package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

func TestGotoReturnsOKResponse(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)

	resp, err := page.Goto(testCtx(t), h.server.URL+"/empty.html")
	if err != nil {
		t.Fatalf("goto: %v", err)
	}
	if resp == nil {
		t.Fatal("goto returned nil response")
	}
	if resp.Status() != 200 {
		t.Errorf("status = %d, want 200", resp.Status())
	}
	if !resp.Ok() {
		t.Error("Ok() = false, want true")
	}
	if !strings.HasSuffix(resp.URL(), "/empty.html") {
		t.Errorf("response URL = %q, want suffix /empty.html", resp.URL())
	}
	if !strings.HasSuffix(page.URL(), "/empty.html") {
		t.Errorf("page URL = %q, want suffix /empty.html", page.URL())
	}
}

func TestGoto404StillResolves(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)

	resp, err := page.Goto(testCtx(t), h.server.URL+"/does-not-exist.html")
	if err != nil {
		t.Fatalf("goto on 404 should not error, got: %v", err)
	}
	if resp == nil {
		t.Fatal("goto returned nil response for 404")
	}
	if resp.Status() != 404 {
		t.Errorf("status = %d, want 404", resp.Status())
	}
	if resp.Ok() {
		t.Error("Ok() = true for 404, want false")
	}
}

func TestGotoFollowsRedirectAndReportsFinalResponse(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetRedirect(t, "/redirect-start", "/empty.html")

	resp, err := page.Goto(testCtx(t), h.server.URL+"/redirect-start")
	if err != nil {
		t.Fatalf("goto: %v", err)
	}
	if resp.Status() != 200 {
		t.Errorf("status = %d, want 200 (final response after redirect)", resp.Status())
	}
	if !strings.HasSuffix(page.URL(), "/empty.html") {
		t.Errorf("page URL = %q, want final /empty.html", page.URL())
	}
}

func TestGotoDataURL(t *testing.T) {
	page := newPage(t)

	_, err := page.Goto(testCtx(t), "data:text/html,<title>Data page</title><h1>hello</h1>")
	if err != nil {
		t.Fatalf("goto data URL: %v", err)
	}
	title, err := page.Title(testCtx(t))
	if err != nil {
		t.Fatalf("title: %v", err)
	}
	if title != "Data page" {
		t.Errorf("title = %q, want %q", title, "Data page")
	}
}

func TestGotoUnreachableHostFails(t *testing.T) {
	page := newPage(t)

	_, err := page.Goto(testCtx(t), "http://127.0.0.1:1/unreachable")
	if err == nil {
		t.Fatal("goto to unreachable host should return an error")
	}
}

func TestGoBackGoForward(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/empty.html")
	gotoPath(t, page, "/button.html")

	if _, err := page.GoBack(testCtx(t), browser.LoadStateDOMContentLoaded, 10*time.Second); err != nil {
		t.Fatalf("go back: %v", err)
	}
	if !strings.HasSuffix(page.URL(), "/empty.html") {
		t.Fatalf("after back, URL = %q, want /empty.html", page.URL())
	}

	if _, err := page.GoForward(testCtx(t), browser.LoadStateDOMContentLoaded, 10*time.Second); err != nil {
		t.Fatalf("go forward: %v", err)
	}
	if !strings.HasSuffix(page.URL(), "/button.html") {
		t.Fatalf("after forward, URL = %q, want /button.html", page.URL())
	}
}

func TestReloadResetsPageState(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	if err := page.Locator("button").Click(testCtx(t)); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Fatalf("precondition failed: window.result = %q", got)
	}

	if _, err := page.Reload(testCtx(t)); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Was not clicked" {
		t.Errorf("after reload window.result = %q, want fresh state", got)
	}
}

func TestGotoCrossOriginNavigation(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)

	gotoPath(t, page, "/empty.html")
	resp, err := page.Goto(testCtx(t), h.server.CrossOriginURL()+"/button.html")
	if err != nil {
		t.Fatalf("cross-origin goto: %v", err)
	}
	if resp.Status() != 200 {
		t.Errorf("status = %d, want 200", resp.Status())
	}
	if got := evalString(t, page, "location.hostname"); got != "localhost" {
		t.Errorf("hostname = %q, want localhost", got)
	}
	// The page must still be drivable after a cross-process swap.
	if err := page.Locator("button").Click(testCtx(t)); err != nil {
		t.Fatalf("click after cross-origin navigation: %v", err)
	}
	if got := evalString(t, page, "window.result"); got != "Clicked" {
		t.Errorf("window.result = %q, want Clicked", got)
	}
}

func TestGotoSlowResponseHonorsContext(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	h.server.SetRoute(t, "/slow.html", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<title>slow</title>"))
	})

	ctx, cancel := contextWithTimeout(500 * time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := page.Goto(ctx, h.server.URL+"/slow.html")
	if err == nil {
		t.Fatal("goto should fail when context times out before response")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("goto took %s after context timeout, should abort promptly", elapsed)
	}
}

func TestWaitForMainLoadState(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	if err := page.WaitForMainLoadState(testCtx(t), browser.LoadStateLoad); err != nil {
		t.Fatalf("wait for load state on loaded page: %v", err)
	}
}
