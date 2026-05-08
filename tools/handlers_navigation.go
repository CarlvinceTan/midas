package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

func goTo(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	url := stringArg(input, "url")
	if url == "" {
		return Result{}, fmt.Errorf("go_to requires url")
	}
	waitUntil := loadStateArg(input)
	if waitUntil == "" {
		waitUntil = browser.LoadStateLoad
	}
	if _, err := page.Goto(ctx, url); err != nil {
		return Result{}, err
	}
	if err := page.WaitForMainLoadState(ctx, waitUntil); err != nil {
		return Result{}, err
	}
	return Result{
		Message: "navigation completed",
		Value: map[string]any{
			"url": page.URL(),
		},
	}, nil
}

func navBack(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	waitUntil := loadStateArg(input)
	if waitUntil == "" {
		waitUntil = browser.LoadStateLoad
	}
	if _, err := page.GoBack(ctx, waitUntil, timeoutArg(input, "timeout_ms", 30*time.Second)); err != nil {
		return Result{}, err
	}
	return Result{
		Message: "navigated back",
		Value: map[string]any{
			"url": page.URL(),
		},
	}, nil
}
