package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PolymuxOrg/midas/browser"
)

func pageFromContext(bctx *browser.Context) (*browser.Page, error) {
	if bctx == nil {
		return nil, fmt.Errorf("browser context is not available")
	}
	page := bctx.ActivePage()
	if page == nil {
		return nil, fmt.Errorf("no active page available")
	}
	return page, nil
}

func stringArg(input map[string]any, key string) string {
	raw, ok := input[key]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func boolArg(input map[string]any, key string) bool {
	raw, ok := input[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

func floatArg(input map[string]any, key string) float64 {
	raw, ok := input[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func timeoutArg(input map[string]any, key string, fallback time.Duration) time.Duration {
	value := floatArg(input, key)
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func loadStateArg(input map[string]any) browser.LoadState {
	switch strings.ToLower(stringArg(input, "wait_until")) {
	case string(browser.LoadStateDOMContentLoaded):
		return browser.LoadStateDOMContentLoaded
	case string(browser.LoadStateNetworkIdle):
		return browser.LoadStateNetworkIdle
	case string(browser.LoadStateLoad):
		return browser.LoadStateLoad
	default:
		return ""
	}
}
