package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

func click(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	selector := stringArg(input, "selector")
	if selector == "" {
		return Result{}, fmt.Errorf("click requires selector")
	}
	locator := page.Locator(selector)
	if boolArg(input, "double") {
		err = locator.DblClick(ctx)
	} else {
		err = locator.Click(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Message: "click completed", Value: map[string]any{"selector": selector}}, nil
}

func typeText(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	selector := stringArg(input, "selector")
	text := stringArg(input, "text")
	if selector == "" || text == "" {
		return Result{}, fmt.Errorf("type requires selector and text")
	}
	locator := page.Locator(selector)
	if boolArg(input, "clear") {
		if err := locator.Fill(ctx, ""); err != nil {
			return Result{}, err
		}
	}
	if err := locator.Type(ctx, text, timeoutArg(input, "delay_ms", 0)); err != nil {
		return Result{}, err
	}
	return Result{Message: "typing completed", Value: map[string]any{"selector": selector, "text": text}}, nil
}

func fillForm(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	rawFields, ok := input["fields"].([]any)
	if !ok || len(rawFields) == 0 {
		return Result{}, fmt.Errorf("fill_form requires fields")
	}
	filled := make([]map[string]any, 0, len(rawFields))
	for _, item := range rawFields {
		field, ok := item.(map[string]any)
		if !ok {
			return Result{}, fmt.Errorf("fill_form fields must be objects")
		}
		selector := stringArg(field, "selector")
		value := stringArg(field, "value")
		if selector == "" {
			return Result{}, fmt.Errorf("fill_form field missing selector")
		}
		if err := page.Locator(selector).Fill(ctx, value); err != nil {
			return Result{}, err
		}
		filled = append(filled, map[string]any{"selector": selector, "value": value})
	}
	return Result{Message: "form fields filled", Value: map[string]any{"fields": filled}}, nil
}

func scroll(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	if selector := stringArg(input, "selector"); selector != "" {
		percent := floatArg(input, "percent")
		if percent == 0 {
			percent = 100
		}
		if err := page.Locator(selector).ScrollTo(ctx, percent); err != nil {
			return Result{}, err
		}
		return Result{Message: "element scrolled", Value: map[string]any{"selector": selector, "percent": percent}}, nil
	}
	deltaX := floatArg(input, "delta_x")
	deltaY := floatArg(input, "delta_y")
	if deltaX == 0 && deltaY == 0 {
		deltaY = 800
	}
	if err := page.Scroll(ctx, 0, 0, deltaX, deltaY); err != nil {
		return Result{}, err
	}
	return Result{Message: "page scrolled", Value: map[string]any{"delta_x": deltaX, "delta_y": deltaY}}, nil
}

func dragAndDrop(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	fromSelector := stringArg(input, "from_selector")
	toSelector := stringArg(input, "to_selector")
	if fromSelector == "" || toSelector == "" {
		return Result{}, fmt.Errorf("drag_and_drop requires from_selector and to_selector")
	}
	from, err := page.Locator(fromSelector).Centroid(ctx)
	if err != nil {
		return Result{}, err
	}
	to, err := page.Locator(toSelector).Centroid(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := page.DragAndDrop(ctx, from.X, from.Y, to.X, to.Y, int(floatArg(input, "steps"))); err != nil {
		return Result{}, err
	}
	return Result{
		Message: "drag and drop completed",
		Value:   map[string]any{"from_selector": fromSelector, "to_selector": toSelector},
	}, nil
}

func clickAndHold(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	selector := stringArg(input, "selector")
	if selector == "" {
		return Result{}, fmt.Errorf("click_and_hold requires selector")
	}
	locator := page.Locator(selector)
	if err := locator.Click(ctx); err != nil {
		return Result{}, err
	}
	if err := page.WaitForTimeout(ctx, timeoutArg(input, "duration_ms", time.Second)); err != nil {
		return Result{}, err
	}
	return Result{Message: "click and hold simulated", Value: map[string]any{"selector": selector}}, nil
}

func keys(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
	page, err := pageFromContext(bctx)
	if err != nil {
		return Result{}, err
	}
	key := stringArg(input, "key")
	if key == "" {
		return Result{}, fmt.Errorf("keys requires key")
	}
	if err := page.KeyPress(ctx, key); err != nil {
		return Result{}, err
	}
	return Result{Message: "key pressed", Value: map[string]any{"key": key}}, nil
}
