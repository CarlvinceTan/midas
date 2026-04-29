package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser"
	"github.com/carlvincetan/polymux/internal/midas/cache"
)

type CacheService struct {
	cache    *cache.Cache
	replayer *cache.Replayer
}

func NewCacheService(storage *cache.Storage) *CacheService {
	c := cache.New(storage)
	return &CacheService{
		cache:    c,
		replayer: cache.NewReplayer(c),
	}
}

func (s *CacheService) Cache() *cache.Cache {
	return s.cache
}

func (s *CacheService) Replayer() *cache.Replayer {
	return s.replayer
}

func RegisterCacheTools(r *Registry, cacheSvc *CacheService) {
	r.register(Spec{
		Name:        "cache_store",
		Description: "Store a sequence of browser actions in the cache for later replay.",
		InputHint:   `{"key":"login-flow","actions":[{"type":"type","selector":"#user","value":"%username%"}],"variables":["username"]}`,
	}, makeCacheStoreHandler(cacheSvc))

	r.register(Spec{
		Name:        "cache_lookup",
		Description: "Check if a cache entry exists and return its details.",
		InputHint:   `{"key":"login-flow"}`,
	}, makeCacheLookupHandler(cacheSvc))

	r.register(Spec{
		Name:        "cache_replay",
		Description: "Execute a cached action sequence with optional variable interpolation.",
		InputHint:   `{"key":"login-flow","variables":{"username":"john"}}`,
	}, makeCacheReplayHandler(cacheSvc))

	r.register(Spec{
		Name:        "cache_clear",
		Description: "Remove cached actions. Omit key to clear all entries.",
		InputHint:   `{"key":"login-flow"}`,
	}, makeCacheClearHandler(cacheSvc))

	r.register(Spec{
		Name:        "cache_list",
		Description: "List all cached action keys.",
		InputHint:   `{}`,
	}, makeCacheListHandler(cacheSvc))
}

func makeCacheStoreHandler(cacheSvc *CacheService) Handler {
	return func(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
		key := stringArg(input, "key")
		if key == "" {
			return Result{}, fmt.Errorf("cache_store requires key")
		}

		rawActions, ok := input["actions"].([]any)
		if !ok || len(rawActions) == 0 {
			return Result{}, fmt.Errorf("cache_store requires actions array")
		}

		page, err := pageFromContext(bctx)
		if err != nil {
			return Result{}, err
		}

		actions := make([]cache.Action, 0, len(rawActions))
		for i, raw := range rawActions {
			actionMap, ok := raw.(map[string]any)
			if !ok {
				return Result{}, fmt.Errorf("action %d is not an object", i)
			}
			action, err := parseAction(actionMap)
			if err != nil {
				return Result{}, fmt.Errorf("action %d: %w", i, err)
			}
			actions = append(actions, action)
		}

		var variables []string
		if rawVars, ok := input["variables"].([]any); ok {
			variables = make([]string, 0, len(rawVars))
			for _, v := range rawVars {
				if vs, ok := v.(string); ok {
					variables = append(variables, vs)
				}
			}
		}

		metadata := make(map[string]any)
		if rawMeta, ok := input["metadata"].(map[string]any); ok {
			for k, v := range rawMeta {
				metadata[k] = v
			}
		}

		result, err := cacheSvc.Cache().Store(ctx, key, actions, variables, metadata, page.URL())
		if err != nil {
			return Result{}, err
		}

		return Result{
			Message: result.Message,
			Value: map[string]any{
				"success":   result.Success,
				"key":       result.Key,
				"actions":   len(actions),
				"variables": variables,
			},
		}, nil
	}
}

func makeCacheLookupHandler(cacheSvc *CacheService) Handler {
	return func(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
		key := stringArg(input, "key")
		if key == "" {
			return Result{}, fmt.Errorf("cache_lookup requires key")
		}

		page, err := pageFromContext(bctx)
		if err != nil {
			return Result{}, err
		}

		result, err := cacheSvc.Cache().Lookup(ctx, key, page.URL())
		if err != nil {
			return Result{}, err
		}

		if !result.Found {
			return Result{
				Message: fmt.Sprintf("no cache entry found for key '%s'", key),
				Value: map[string]any{
					"found": false,
					"key":   key,
				},
			}, nil
		}

		return Result{
			Message: fmt.Sprintf("cache entry found for key '%s'", key),
			Value: map[string]any{
				"found":     true,
				"key":       key,
				"url":       result.Entry.URL,
				"url_match": result.URLMatch,
				"actions":   len(result.Entry.Actions),
				"variables": result.Entry.Variables,
				"timestamp": result.Entry.Timestamp.Format(time.RFC3339),
			},
		}, nil
	}
}

func makeCacheReplayHandler(cacheSvc *CacheService) Handler {
	return func(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
		key := stringArg(input, "key")
		if key == "" {
			return Result{}, fmt.Errorf("cache_replay requires key")
		}

		page, err := pageFromContext(bctx)
		if err != nil {
			return Result{}, err
		}

		lookup, err := cacheSvc.Cache().Lookup(ctx, key, page.URL())
		if err != nil {
			return Result{}, err
		}
		if !lookup.Found {
			return Result{
				Message: fmt.Sprintf("no cache entry found for key '%s'", key),
				Value: map[string]any{
					"success": false,
					"key":     key,
					"error":   "cache entry not found",
				},
			}, nil
		}

		var variables map[string]string
		if rawVars, ok := input["variables"].(map[string]any); ok {
			variables = make(map[string]string, len(rawVars))
			for k, v := range rawVars {
				if vs, ok := v.(string); ok {
					variables[k] = vs
				}
			}
		}

		if missing := cacheSvc.Cache().ValidateVariables(lookup.Entry, variables); len(missing) > 0 {
			return Result{
				Message: fmt.Sprintf("missing required variables: %v", missing),
				Value: map[string]any{
					"success": false,
					"key":     key,
					"missing": missing,
				},
			}, nil
		}

		timeout := time.Duration(floatArg(input, "timeout_ms")) * time.Millisecond
		if timeout == 0 {
			timeout = 30 * time.Second
		}

		opts := cache.ReplayOptions{
			Variables:       variables,
			Timeout:         timeout,
			ContinueOnError: boolArg(input, "continue_on_error"),
			SelfHeal:        boolArg(input, "self_heal"),
		}

		result, err := cacheSvc.Replayer().Replay(ctx, page, lookup.Entry, opts, page.URL())
		if err != nil {
			return Result{}, err
		}

		message := fmt.Sprintf("replayed %d/%d actions", result.Executed, len(lookup.Entry.Actions))
		if !result.Success {
			message = fmt.Sprintf("replay failed at action %d: %s", result.FailedIndex+1, result.Error)
		}

		return Result{
			Message: message,
			Value: map[string]any{
				"success":     result.Success,
				"key":         key,
				"executed":    result.Executed,
				"total":       len(lookup.Entry.Actions),
				"url_match":   lookup.URLMatch,
				"url_warning": result.URLWarning,
			},
		}, nil
	}
}

func makeCacheClearHandler(cacheSvc *CacheService) Handler {
	return func(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
		_ = bctx

		key := stringArg(input, "key")

		count, err := cacheSvc.Cache().Clear(ctx, key)
		if err != nil {
			return Result{}, err
		}

		message := fmt.Sprintf("cleared %d cache entries", count)
		if key != "" {
			if count == 0 {
				message = fmt.Sprintf("no cache entry found for key '%s'", key)
			} else {
				message = fmt.Sprintf("cleared cache entry '%s'", key)
			}
		}

		return Result{
			Message: message,
			Value: map[string]any{
				"success":       true,
				"cleared_count": count,
			},
		}, nil
	}
}

func makeCacheListHandler(cacheSvc *CacheService) Handler {
	return func(ctx context.Context, bctx *browser.Context, input map[string]any) (Result, error) {
		_ = input
		_ = bctx

		keys, err := cacheSvc.Cache().List(ctx)
		if err != nil {
			return Result{}, err
		}

		return Result{
			Message: fmt.Sprintf("found %d cached entries", len(keys)),
			Value: map[string]any{
				"count": len(keys),
				"keys":  keys,
			},
		}, nil
	}
}

func parseAction(raw map[string]any) (cache.Action, error) {
	action := cache.Action{}

	actionType := stringArg(raw, "type")
	if actionType == "" {
		return action, fmt.Errorf("action missing type")
	}
	action.Type = cache.ActionType(actionType)

	action.Selector = stringArg(raw, "selector")
	action.Value = stringArg(raw, "value")
	action.Button = stringArg(raw, "button")
	action.Key = stringArg(raw, "key")
	action.WaitUntil = stringArg(raw, "wait_until")

	action.DeltaX = floatArg(raw, "delta_x")
	action.DeltaY = floatArg(raw, "delta_y")
	action.Percent = floatArg(raw, "percent")

	action.Duration = int(floatArg(raw, "duration"))
	action.Timeout = int(floatArg(raw, "timeout"))
	action.Steps = int(floatArg(raw, "steps"))

	action.Double = boolArg(raw, "double")
	action.Clear = boolArg(raw, "clear")

	if rawFields, ok := raw["fields"].([]any); ok {
		action.Fields = make([]cache.FormField, 0, len(rawFields))
		for _, rf := range rawFields {
			if fieldMap, ok := rf.(map[string]any); ok {
				action.Fields = append(action.Fields, cache.FormField{
					Selector: stringArg(fieldMap, "selector"),
					Value:    stringArg(fieldMap, "value"),
				})
			}
		}
	}

	if rawOpts, ok := raw["options"].(map[string]any); ok {
		action.Options = rawOpts
	}

	return action, nil
}
