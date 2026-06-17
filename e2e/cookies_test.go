package e2e

import (
	"strings"
	"testing"

	"github.com/PolymuxOrg/midas/browser"
)

// Context cookie management. These tests share the harness browser context, so
// each clears cookies before and after to stay isolated.

func clearAllCookies(t *testing.T) {
	t.Helper()
	h := requireHarness(t)
	if err := h.bctx.ClearCookies(testCtx(t), nil); err != nil {
		t.Fatalf("clear cookies: %v", err)
	}
}

func TestAddAndReadCookies(t *testing.T) {
	h := requireHarness(t)
	clearAllCookies(t)
	t.Cleanup(func() { clearAllCookies(t) })

	url := h.server.URL
	if err := h.bctx.AddCookies(testCtx(t), browser.Cookie{
		Name:  "session",
		Value: "abc123",
		URL:   url,
	}); err != nil {
		t.Fatalf("add cookies: %v", err)
	}

	cookies, err := h.bctx.Cookies(testCtx(t), url)
	if err != nil {
		t.Fatalf("read cookies: %v", err)
	}
	var found *browser.Cookie
	for i := range cookies {
		if cookies[i].Name == "session" {
			found = &cookies[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("cookie 'session' not found among %d cookies", len(cookies))
	}
	if found.Value != "abc123" {
		t.Errorf("cookie value = %q, want abc123", found.Value)
	}
}

func TestCookieVisibleToPage(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	clearAllCookies(t)
	t.Cleanup(func() { clearAllCookies(t) })

	if err := h.bctx.AddCookies(testCtx(t), browser.Cookie{
		Name:  "pagecookie",
		Value: "visible",
		URL:   h.server.URL,
	}); err != nil {
		t.Fatalf("add cookie: %v", err)
	}
	gotoPath(t, page, "/empty.html")
	got := evalString(t, page, "document.cookie")
	if got == "" {
		t.Fatal("document.cookie is empty; cookie not applied to the origin")
	}
	if !strings.Contains(got, "pagecookie=visible") {
		t.Errorf("document.cookie = %q, want it to contain pagecookie=visible", got)
	}
}

func TestClearCookiesRemovesAll(t *testing.T) {
	h := requireHarness(t)
	clearAllCookies(t)

	if err := h.bctx.AddCookies(testCtx(t),
		browser.Cookie{Name: "a", Value: "1", URL: h.server.URL},
		browser.Cookie{Name: "b", Value: "2", URL: h.server.URL},
	); err != nil {
		t.Fatalf("add cookies: %v", err)
	}
	if err := h.bctx.ClearCookies(testCtx(t), nil); err != nil {
		t.Fatalf("clear cookies: %v", err)
	}
	cookies, err := h.bctx.Cookies(testCtx(t), h.server.URL)
	if err != nil {
		t.Fatalf("read cookies: %v", err)
	}
	for _, c := range cookies {
		if c.Name == "a" || c.Name == "b" {
			t.Errorf("cookie %q survived ClearCookies", c.Name)
		}
	}
}
