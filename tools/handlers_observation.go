package tools

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

func extract(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	selector := stringArg(input, "selector")
	property := strings.ToLower(stringArg(input, "property"))
	if property == "" {
		property = "snapshot"
	}
	if selector == "" || property == "snapshot" {
		snapshot, err := page.Snapshot(ctx)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Message: "snapshot captured",
			Value: map[string]any{
				"url":            page.URL(),
				"formatted_tree": snapshot.FormattedTree,
				"xpath_map":      snapshot.XPathMap,
				"url_map":        snapshot.URLMap,
			},
		}, nil
	}
	locator := page.Locator(selector)
	var value string
	switch property {
	case "html":
		value, err = locator.InnerHTML(ctx)
	case "value":
		value, err = locator.InputValue(ctx)
	default:
		value, err = locator.InnerText(ctx)
		property = "text"
	}
	if err != nil {
		return Result{}, err
	}
	return Result{
		Message: "extraction completed",
		Value:   map[string]any{"selector": selector, "property": property, "value": value},
	}, nil
}

func ariaTree(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	nodes, err := page.MainFrame().GetAccessibilityTree(ctx, boolArg(input, "with_frames"))
	if err != nil {
		return Result{}, err
	}
	return Result{Message: "accessibility tree captured", Value: map[string]any{"nodes": nodes}}, nil
}

func screenshot(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	var data []byte
	if selector := stringArg(input, "selector"); selector != "" {
		data, err = page.Locator(selector).Screenshot(ctx, browser.ScreenshotOptions{})
	} else {
		data, err = page.Screenshot(ctx, browser.ScreenshotOptions{FullPage: boolArg(input, "full_page")})
	}
	if err != nil {
		return Result{}, err
	}
	return Result{
		Message: "screenshot captured",
		Value: map[string]any{
			"bytes":    len(data),
			"base64":   base64.StdEncoding.EncodeToString(data),
			"selector": stringArg(input, "selector"),
		},
	}, nil
}
