package browser

import (
	"context"
	"testing"
)

func TestContextCookiesFilterByURL(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	conn.send = func(method string, _ any, result any) error {
		if method != "Storage.getCookies" {
			return nil
		}
		res := result.(*struct {
			Cookies []struct {
				Name     string  `json:"name"`
				Value    string  `json:"value"`
				Domain   string  `json:"domain"`
				Path     string  `json:"path"`
				Expires  float64 `json:"expires"`
				HTTPOnly bool    `json:"httpOnly"`
				Secure   bool    `json:"secure"`
				SameSite string  `json:"sameSite"`
			} `json:"cookies"`
		})
		res.Cookies = []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
			SameSite string  `json:"sameSite"`
		}{
			{Name: "a", Value: "1", Domain: "example.com", Path: "/"},
			{Name: "b", Value: "2", Domain: "other.test", Path: "/"},
		}
		return nil
	}

	ctx := newContext(conn)
	cookies, err := ctx.Cookies(context.Background(), "https://example.com/path")
	if err != nil {
		t.Fatalf("Cookies returned error: %v", err)
	}
	if len(cookies) != 1 || cookies[0].Name != "a" {
		t.Fatalf("unexpected filtered cookies: %#v", cookies)
	}
}

func TestContextAddInitScriptAndHeadersPropagateToPages(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	session := newFakeSession("session-1")
	page := newPage(conn, session, "target-1", frameNode{
		Frame: cdpFrame{ID: "root-1", URL: "about:blank"},
	})

	ctx := newContext(conn)
	ctx.pagesByTarget["target-1"] = page

	if err := ctx.AddInitScript(context.Background(), "window.__x = 1;"); err != nil {
		t.Fatalf("AddInitScript returned error: %v", err)
	}
	if err := ctx.SetExtraHTTPHeaders(context.Background(), map[string]string{"X-Test": "1"}); err != nil {
		t.Fatalf("SetExtraHTTPHeaders returned error: %v", err)
	}

	methods := session.methods()
	foundInit := false
	foundHeaders := false
	for _, method := range methods {
		if method == "Page.addScriptToEvaluateOnNewDocument" {
			foundInit = true
		}
		if method == "Network.setExtraHTTPHeaders" {
			foundHeaders = true
		}
	}
	if !foundInit || !foundHeaders {
		t.Fatalf("expected init script and header propagation, got %v", methods)
	}
}
