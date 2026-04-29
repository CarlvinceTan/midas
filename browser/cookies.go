package browser

import (
	"net/url"
	"strings"
)

func normalizeCookie(cookie Cookie) Cookie {
	if cookie.Path == "" {
		cookie.Path = "/"
	}
	if cookie.SameSite == "" {
		cookie.SameSite = "Lax"
	}
	return cookie
}

func filterCookies(cookies []Cookie, urls []string) []Cookie {
	if len(urls) == 0 {
		return cookies
	}
	out := make([]Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		for _, rawURL := range urls {
			if cookieMatchesURL(cookie, rawURL) {
				out = append(out, cookie)
				break
			}
		}
	}
	return out
}

func cookieMatchesURL(cookie Cookie, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
	if domain != "" && host != domain && !strings.HasSuffix(host, "."+domain) {
		return false
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	cookiePath := cookie.Path
	if cookiePath == "" {
		cookiePath = "/"
	}
	if !strings.HasPrefix(path, cookiePath) {
		return false
	}
	if cookie.Secure && u.Scheme != "https" {
		return false
	}
	return true
}

func cookieMatchesFilter(cookie Cookie, opts ClearCookieOptions) bool {
	if opts.Name != "" && cookie.Name != opts.Name {
		return false
	}
	if opts.Domain != "" && cookie.Domain != opts.Domain {
		return false
	}
	if opts.Path != "" && cookie.Path != opts.Path {
		return false
	}
	return true
}

func cookieToCDP(cookie Cookie) map[string]any {
	cookie = normalizeCookie(cookie)
	out := map[string]any{
		"name":     cookie.Name,
		"value":    cookie.Value,
		"domain":   cookie.Domain,
		"path":     cookie.Path,
		"secure":   cookie.Secure,
		"httpOnly": cookie.HTTPOnly,
		"sameSite": cookie.SameSite,
	}
	if cookie.Expires > 0 {
		out["expires"] = cookie.Expires
	}
	if cookie.URL != "" {
		out["url"] = cookie.URL
	}
	return out
}
