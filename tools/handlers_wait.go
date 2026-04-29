package tools

import (
	"context"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

func waitTool(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	if selector := stringArg(input, "selector"); selector != "" {
		ok, err := page.WaitForSelector(ctx, selector, browser.WaitForSelectorOptions{
			Timeout: timeoutArg(input, "timeout_ms", 30*time.Second),
			State:   browser.SelectorStateVisible,
		})
		if err != nil {
			return Result{}, err
		}
		return Result{Message: "selector wait completed", Value: map[string]any{"selector": selector, "matched": ok}}, nil
	}
	duration := timeoutArg(input, "duration_ms", time.Second)
	if err := page.WaitForTimeout(ctx, duration); err != nil {
		return Result{}, err
	}
	return Result{Message: "wait completed", Value: map[string]any{"duration_ms": duration.Milliseconds()}}, nil
}

func think(_ context.Context, _ *browser.Context, input map[string]any) (Result, error) {
	note := stringArg(input, "note")
	if note == "" {
		note = "thinking"
	}
	return Result{Message: note, Value: map[string]any{"note": note}}, nil
}
