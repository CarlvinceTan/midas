package browser

import (
	"context"
)

func (c *Context) AddInitScript(ctx context.Context, source string) error {
	if source == "" {
		return nil
	}
	c.mu.Lock()
	if !containsString(c.initScripts, source) {
		c.initScripts = append(c.initScripts, source)
	}
	pages := make([]*Page, 0, len(c.pagesByTarget))
	for _, page := range c.pagesByTarget {
		pages = append(pages, page)
	}
	c.mu.Unlock()
	for _, page := range pages {
		if err := page.AddInitScript(ctx, source); err != nil {
			return err
		}
	}
	return nil
}

func (c *Context) SetExtraHTTPHeaders(ctx context.Context, headers map[string]string) error {
	headers = cloneStringMap(headers)
	c.mu.Lock()
	c.extraHTTPHeaders = headers
	pages := make([]*Page, 0, len(c.pagesByTarget))
	for _, page := range c.pagesByTarget {
		pages = append(pages, page)
	}
	c.mu.Unlock()
	for _, page := range pages {
		if err := page.SetExtraHTTPHeaders(ctx, headers); err != nil {
			return err
		}
	}
	return nil
}

func (c *Context) Cookies(ctx context.Context, urls ...string) ([]Cookie, error) {
	var res struct {
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
	}
	if err := c.conn.Send(ctx, "Storage.getCookies", nil, &res); err != nil {
		return nil, err
	}
	out := make([]Cookie, 0, len(res.Cookies))
	for _, cookie := range res.Cookies {
		out = append(out, normalizeCookie(Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Expires:  cookie.Expires,
			HTTPOnly: cookie.HTTPOnly,
			Secure:   cookie.Secure,
			SameSite: cookie.SameSite,
		}))
	}
	return filterCookies(out, urls), nil
}

func (c *Context) AddCookies(ctx context.Context, cookies ...Cookie) error {
	payload := make([]map[string]any, 0, len(cookies))
	for _, cookie := range cookies {
		payload = append(payload, cookieToCDP(cookie))
	}
	return c.conn.Send(ctx, "Storage.setCookies", map[string]any{"cookies": payload}, nil)
}

func (c *Context) ClearCookies(ctx context.Context, opts *ClearCookieOptions) error {
	if opts == nil || (opts.Name == "" && opts.Domain == "" && opts.Path == "") {
		return c.conn.Send(ctx, "Storage.clearCookies", nil, nil)
	}
	cookies, err := c.Cookies(ctx)
	if err != nil {
		return err
	}
	keep := make([]Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if !cookieMatchesFilter(cookie, *opts) {
			keep = append(keep, cookie)
		}
	}
	if err := c.conn.Send(ctx, "Storage.clearCookies", nil, nil); err != nil {
		return err
	}
	if len(keep) == 0 {
		return nil
	}
	return c.AddCookies(ctx, keep...)
}
