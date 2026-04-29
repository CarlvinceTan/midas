package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

type ReplayOptions struct {
	Variables       map[string]string
	Timeout         time.Duration
	ContinueOnError bool
	SelfHeal        bool
	SelfHealTimeout time.Duration
	Logger          HealLogger
}

type Replayer struct {
	cache  *Cache
	logger HealLogger
}

func NewReplayer(cache *Cache) *Replayer {
	return &Replayer{cache: cache, logger: nopLogger{}}
}

func NewReplayerWithLogger(cache *Cache, logger HealLogger) *Replayer {
	if logger == nil {
		logger = nopLogger{}
	}
	return &Replayer{cache: cache, logger: logger}
}

func (r *Replayer) Replay(ctx context.Context, page *browser.Page, entry *Entry, opts ReplayOptions, currentURL string) (*ReplayResult, error) {
	actions := r.cache.InterpolateVariables(entry, opts.Variables)

	if len(actions) == 0 {
		return &ReplayResult{
			Success:  true,
			Executed: 0,
			Actions:  []ActionResult{},
		}, nil
	}

	results := make([]ActionResult, 0, len(actions))
	var urlWarning string
	if entry.URL != "" && currentURL != "" && entry.URL != currentURL {
		urlWarning = fmt.Sprintf("cached URL '%s' differs from current URL '%s'", entry.URL, currentURL)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	selfHealConfig := SelfHealConfig{
		Enabled: opts.SelfHeal,
		Timeout: opts.SelfHealTimeout,
		Logger:  opts.Logger,
	}
	if selfHealConfig.Timeout <= 0 {
		selfHealConfig.Timeout = timeout
	}

	var hasHealedActions bool
	healedActions := make(map[int]string)

	for i, action := range actions {
		actionCtx, cancel := context.WithTimeout(ctx, timeout)

		if r.isSelectorAction(action) && action.Selector != "" {
			r.waitForCachedSelector(actionCtx, page, action.Selector, timeout)
		}

		result := r.executeAction(actionCtx, page, action)
		cancel()

		results = append(results, result)

		if !result.Success {
			if opts.SelfHeal && r.isSelectorAction(action) && action.Selector != "" {
				healedResult := r.selfHealAction(ctx, page, action, selfHealConfig)
				if healedResult.Success {
					results[len(results)-1] = *healedResult
					if healedResult.Action.Selector != "" && healedResult.Action.Selector != action.Selector {
						healedActions[i] = healedResult.Action.Selector
						hasHealedActions = true
					}
					continue
				}
			}

			if !opts.ContinueOnError {
				return &ReplayResult{
					Success:      false,
					Executed:     i + 1,
					FailedIndex:  i,
					FailedAction: &action,
					Error:        result.Error,
					Actions:      results,
					URLWarning:   urlWarning,
				}, nil
			}
		}
	}

	if hasHealedActions {
		updatedActions := make([]Action, len(actions))
		copy(updatedActions, actions)
		for i, healedSelector := range healedActions {
			updatedActions[i].Selector = healedSelector
		}
		updatedEntry := &Entry{
			Version:   entry.Version,
			Key:       entry.Key,
			URL:       currentURL,
			Actions:   updatedActions,
			Variables: entry.Variables,
			Metadata:  entry.Metadata,
			Timestamp: time.Now().UTC(),
		}
		if err := r.cache.UpdateEntry(updatedEntry); err != nil {
			r.logger.Warn("failed to update cache entry after self-heal", "key", entry.Key, "error", err.Error())
		}
	}

	return &ReplayResult{
		Success:    true,
		Executed:   len(actions),
		Actions:    results,
		URLWarning: urlWarning,
	}, nil
}

func (r *Replayer) waitForCachedSelector(ctx context.Context, page *browser.Page, selector string, timeout time.Duration) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, err := page.WaitForSelector(waitCtx, selector, browser.WaitForSelectorOptions{
		State: browser.SelectorStateAttached,
	})
	if err != nil {
		r.logger.Warn("waitForSelector timeout for cached action, proceeding anyway", "selector", selector, "error", err.Error())
	}
}

func (r *Replayer) executeAction(ctx context.Context, page *browser.Page, action Action) ActionResult {
	var err error

	switch action.Type {
	case ActionTypeClick:
		err = r.executeClick(ctx, page, action)
	case ActionTypeType:
		err = r.executeType(ctx, page, action)
	case ActionTypeGoto:
		err = r.executeGoto(ctx, page, action)
	case ActionTypeScroll:
		err = r.executeScroll(ctx, page, action)
	case ActionTypeWait:
		err = r.executeWait(ctx, page, action)
	case ActionTypePress:
		err = r.executePress(ctx, page, action)
	case ActionTypeDragDrop:
		err = r.executeDragDrop(ctx, page, action)
	case ActionTypeFillForm:
		err = r.executeFillForm(ctx, page, action)
	case ActionTypeClickHold:
		err = r.executeClickHold(ctx, page, action)
	case ActionTypeNavBack:
		err = r.executeNavBack(ctx, page, action)
	default:
		return ActionResult{
			Action:  action,
			Success: false,
			Error:   fmt.Sprintf("unknown action type: %s", action.Type),
		}
	}

	if err != nil {
		return ActionResult{
			Action:  action,
			Success: false,
			Error:   err.Error(),
		}
	}

	return ActionResult{
		Action:  action,
		Success: true,
	}
}

func (r *Replayer) executeClick(ctx context.Context, page *browser.Page, action Action) error {
	locator := page.Locator(action.Selector)
	if action.Double {
		return locator.DblClick(ctx)
	}
	return locator.Click(ctx)
}

func (r *Replayer) executeType(ctx context.Context, page *browser.Page, action Action) error {
	locator := page.Locator(action.Selector)
	if action.Clear {
		if err := locator.Fill(ctx, ""); err != nil {
			return err
		}
	}
	return locator.Type(ctx, action.Value, 0)
}

func (r *Replayer) executeGoto(ctx context.Context, page *browser.Page, action Action) error {
	waitUntil := browser.LoadStateLoad
	if action.WaitUntil != "" {
		switch action.WaitUntil {
		case string(browser.LoadStateDOMContentLoaded):
			waitUntil = browser.LoadStateDOMContentLoaded
		case string(browser.LoadStateNetworkIdle):
			waitUntil = browser.LoadStateNetworkIdle
		}
	}
	if _, err := page.Goto(ctx, action.Value); err != nil {
		return err
	}
	return page.WaitForMainLoadState(ctx, waitUntil)
}

func (r *Replayer) executeScroll(ctx context.Context, page *browser.Page, action Action) error {
	if action.Selector != "" {
		percent := action.Percent
		if percent == 0 {
			percent = 100
		}
		return page.Locator(action.Selector).ScrollTo(ctx, percent)
	}
	deltaX := action.DeltaX
	deltaY := action.DeltaY
	if deltaX == 0 && deltaY == 0 {
		deltaY = 800
	}
	return page.Scroll(ctx, 0, 0, deltaX, deltaY)
}

func (r *Replayer) executeWait(ctx context.Context, page *browser.Page, action Action) error {
	if action.Selector != "" {
		timeout := time.Duration(action.Timeout) * time.Millisecond
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		_, err := page.WaitForSelector(ctx, action.Selector, browser.WaitForSelectorOptions{
			Timeout: timeout,
			State:   browser.SelectorStateVisible,
		})
		return err
	}
	duration := time.Duration(action.Duration) * time.Millisecond
	if duration == 0 {
		duration = time.Second
	}
	return page.WaitForTimeout(ctx, duration)
}

func (r *Replayer) executePress(ctx context.Context, page *browser.Page, action Action) error {
	return page.KeyPress(ctx, action.Key)
}

func (r *Replayer) executeDragDrop(ctx context.Context, page *browser.Page, action Action) error {
	from, err := page.Locator(action.Selector).Centroid(ctx)
	if err != nil {
		return err
	}
	toSelector, ok := action.Options["to_selector"].(string)
	if !ok {
		return fmt.Errorf("drag_and_drop requires to_selector in options")
	}
	to, err := page.Locator(toSelector).Centroid(ctx)
	if err != nil {
		return err
	}
	steps := action.Steps
	if steps == 0 {
		steps = 1
	}
	return page.DragAndDrop(ctx, from.X, from.Y, to.X, to.Y, steps)
}

func (r *Replayer) executeFillForm(ctx context.Context, page *browser.Page, action Action) error {
	for _, field := range action.Fields {
		if err := page.Locator(field.Selector).Fill(ctx, field.Value); err != nil {
			return err
		}
	}
	return nil
}

func (r *Replayer) executeClickHold(ctx context.Context, page *browser.Page, action Action) error {
	locator := page.Locator(action.Selector)
	if err := locator.Click(ctx); err != nil {
		return err
	}
	duration := time.Duration(action.Duration) * time.Millisecond
	if duration == 0 {
		duration = time.Second
	}
	return page.WaitForTimeout(ctx, duration)
}

func (r *Replayer) executeNavBack(ctx context.Context, page *browser.Page, action Action) error {
	waitUntil := browser.LoadStateLoad
	if action.WaitUntil != "" {
		switch action.WaitUntil {
		case string(browser.LoadStateDOMContentLoaded):
			waitUntil = browser.LoadStateDOMContentLoaded
		case string(browser.LoadStateNetworkIdle):
			waitUntil = browser.LoadStateNetworkIdle
		}
	}
	_, err := page.GoBack(ctx, waitUntil, 30*time.Second)
	return err
}

func (r *Replayer) isSelectorAction(action Action) bool {
	switch action.Type {
	case ActionTypeClick, ActionTypeType, ActionTypeScroll, ActionTypeDragDrop, ActionTypeFillForm, ActionTypeClickHold, ActionTypeWait:
		return action.Selector != ""
	default:
		return false
	}
}

func (r *Replayer) selfHealAction(ctx context.Context, page *browser.Page, action Action, config SelfHealConfig) *ActionResult {
	healer := NewSelfHealer(page, config)
	healed, err := healer.Heal(ctx, action)
	if err != nil {
		return &ActionResult{
			Action:  action,
			Success: false,
			Error:   err.Error(),
		}
	}

	healedAction := action
	healedAction.Selector = healed.HealedSelector

	result := r.executeAction(ctx, page, healedAction)
	if result.Success {
		result.Action = healedAction
	}
	return &result
}
