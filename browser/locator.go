package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser/dom"
	"github.com/carlvincetan/polymux/internal/midas/humanize"
)

type Locator struct {
	frame        *Frame
	selector     string
	index        int
	pierceShadow bool
}

type DeepLocator struct {
	Locator
}

type FrameLocator struct {
	page     *Page
	root     *Frame
	selector string
}

func (l *Locator) First() *Locator {
	copy := *l
	copy.index = 0
	return &copy
}

func (l *Locator) Nth(index int) *Locator {
	copy := *l
	copy.index = index
	return &copy
}

func (l *Locator) Count(ctx context.Context) (int, error) {
	frame, finalSelector, err := l.frame.page.resolveSelectorTarget(ctx, l.frame, l.selector, l.pierceShadow)
	if err != nil {
		return 0, err
	}
	session := frame.page.sessionForFrame(frame.frameID)
	ctxID, err := frame.page.ensureSelectorWorld(ctx, session, frame.frameID)
	if err != nil {
		return 0, err
	}
	var count int
	var res struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err = session.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    dom.BuildContextInvocation(dom.HelperCount, finalSelector, l.pierceShadow),
		"returnByValue": true,
		"awaitPromise":  true,
		"contextId":     ctxID,
	}, &res)
	if err != nil {
		return 0, err
	}
	if res.ExceptionDetails != nil {
		return 0, fmt.Errorf("%s", defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}
	buf, err := json.Marshal(res.Result.Value)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(buf, &count); err != nil {
		return 0, err
	}
	return count, nil
}

func (l *Locator) Click(ctx context.Context) error {
	return l.clickAtGeometry(ctx, 1)
}

func (l *Locator) DblClick(ctx context.Context) error {
	return l.clickAtGeometry(ctx, 2)
}

func (l *Locator) Hover(ctx context.Context) error {
	elem, release, err := l.awaitActionable(ctx, DefaultActionabilityOptions())
	if err != nil {
		return err
	}
	defer release()
	cx, cy := elem.Centroid()
	page := l.frame.page
	if page.HumanizeEnabled() {
		return page.performHumanizedHover(ctx, cx, cy)
	}
	return page.Hover(ctx, cx, cy)
}

func (l *Locator) Tap(ctx context.Context) error {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !elem.IsVisible() {
		return notVisibleError(l.selector, l.index, l.frame.frameID)
	}
	cx, cy := elem.Centroid()
	return l.frame.page.Tap(ctx, cx, cy)
}

func (l *Locator) Focus(ctx context.Context) error {
	return l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `
			this.focus();
			return true;
		`, nil)
	})
}

func (l *Locator) Fill(ctx context.Context, value string) error {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !elem.IsEditable() {
		return notEditableError(l.selector, l.index, l.frame.frameID)
	}
	return elem.node.callFunction(ctx, fmt.Sprintf(`
		this.focus();
		this.scrollIntoView({ block: "center", inline: "center" });
		if ("value" in this) {
			this.value = %q;
		}
		this.dispatchEvent(new Event("input", { bubbles: true }));
		this.dispatchEvent(new Event("change", { bubbles: true }));
		return true;
	`, value), nil)
}

func (l *Locator) Type(ctx context.Context, value string, delay time.Duration) error {
	page := l.frame.page
	if page.HumanizeEnabled() {
		cfg := page.HumanizeConfig()
		if cfg != nil {
			if err := humanize.SleepMs(ctx, humanize.RandRange(cfg.FieldSwitchDelay)); err != nil {
				return err
			}
		}
		// Click rather than focus() so the field activation looks like a
		// pointer interaction. Locator.Click routes through humanize too.
		if err := l.Click(ctx); err != nil {
			return err
		}
		if err := humanize.SleepMs(ctx, humanize.Rand(100, 250)); err != nil {
			return err
		}
		return page.performHumanizedType(ctx, value)
	}
	if err := l.Focus(ctx); err != nil {
		return err
	}
	return page.Type(ctx, value, delay)
}

func (l *Locator) Press(ctx context.Context, key string) error {
	if err := l.Focus(ctx); err != nil {
		return err
	}
	return l.frame.page.KeyPress(ctx, key)
}

func (l *Locator) SelectOption(ctx context.Context, values ...string) error {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !elem.IsSelect() {
		return notSelectError(l.selector, l.index, l.frame.frameID)
	}
	jsValues := quoteStrings(values)
	return elem.node.callFunction(ctx, fmt.Sprintf(`
		const wanted = new Set(%s);
		for (const option of this.options || []) {
			option.selected = wanted.has(option.value) || wanted.has(option.text);
		}
		this.dispatchEvent(new Event("input", { bubbles: true }));
		this.dispatchEvent(new Event("change", { bubbles: true }));
		return true;
	`, jsValues), nil)
}

func (l *Locator) ScrollTo(ctx context.Context, percent float64) error {
	return l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, fmt.Sprintf(`
			const p = Math.max(0, Math.min(100, %f));
			this.scrollIntoView({ block: "center", inline: "center" });
			this.scrollTop = (this.scrollHeight - this.clientHeight) * (p / 100);
			return true;
		`, percent), nil)
	})
}

func (l *Locator) IsVisible(ctx context.Context) (bool, error) {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return elem.IsVisible(), nil
}

func (l *Locator) IsChecked(ctx context.Context) (bool, error) {
	var checked bool
	err := l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `return !!this.checked;`, &checked)
	})
	return checked, err
}

func (l *Locator) Check(ctx context.Context) error {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !elem.IsCheckable() {
		return notCheckableError(l.selector, l.index, l.frame.frameID)
	}
	checked, err := elem.node.callFunctionBool(ctx, `return !!this.checked;`)
	if err != nil {
		return err
	}
	if checked {
		return nil
	}
	return elem.node.callFunction(ctx, `
		this.scrollIntoView({ block: "center", inline: "center" });
		if (!this.checked) {
			this.click();
		}
		if (!this.checked) {
			this.checked = true;
			this.dispatchEvent(new Event("input", { bubbles: true }));
			this.dispatchEvent(new Event("change", { bubbles: true }));
		}
		return !!this.checked;
	`, nil)
}

func (l *Locator) Uncheck(ctx context.Context) error {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return err
	}
	defer release()
	if !elem.IsCheckable() {
		return notCheckableError(l.selector, l.index, l.frame.frameID)
	}
	checked, err := elem.node.callFunctionBool(ctx, `return !!this.checked;`)
	if err != nil {
		return err
	}
	if !checked {
		return nil
	}
	return elem.node.callFunction(ctx, `
		this.scrollIntoView({ block: "center", inline: "center" });
		if (this.checked) {
			this.click();
		}
		if (this.checked) {
			this.checked = false;
			this.dispatchEvent(new Event("input", { bubbles: true }));
			this.dispatchEvent(new Event("change", { bubbles: true }));
		}
		return !this.checked;
	`, nil)
}

func (l *Locator) InputValue(ctx context.Context) (string, error) {
	var value string
	err := l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `return "value" in this ? String(this.value ?? "") : "";`, &value)
	})
	return value, err
}

func (l *Locator) TextContent(ctx context.Context) (string, error) {
	var value string
	err := l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `return this.textContent || "";`, &value)
	})
	return value, err
}

func (l *Locator) InnerHTML(ctx context.Context) (string, error) {
	var value string
	err := l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `return this.innerHTML || "";`, &value)
	})
	return value, err
}

func (l *Locator) InnerText(ctx context.Context) (string, error) {
	var value string
	err := l.withResolvedNode(ctx, func(node *resolvedNode) error {
		return node.callFunction(ctx, `return this.innerText || "";`, &value)
	})
	return value, err
}

func (l *Locator) WaitFor(ctx context.Context, opts WaitForSelectorOptions) (bool, error) {
	return l.frame.page.WaitForSelector(ctx, l.selector, opts)
}

func (l *Locator) Screenshot(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	clip, err := l.BoundingBox(ctx)
	if err != nil {
		return nil, err
	}
	opts.Clip = clip
	return l.frame.page.Screenshot(ctx, opts)
}

func (l *Locator) BoundingBox(ctx context.Context) (*ScreenshotClip, error) {
	elem, release, err := l.resolveElement(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if !elem.IsVisible() {
		return nil, notVisibleError(l.selector, l.index, l.frame.frameID)
	}
	geom := elem.Geometry()
	if geom == nil || geom.Width <= 0 || geom.Height <= 0 {
		return nil, notVisibleError(l.selector, l.index, l.frame.frameID)
	}
	return &ScreenshotClip{
		X:      geom.X,
		Y:      geom.Y,
		Width:  geom.Width,
		Height: geom.Height,
		Scale:  geom.Scale,
	}, nil
}

func (l *Locator) Centroid(ctx context.Context) (*ScreenshotClip, error) {
	clip, err := l.BoundingBox(ctx)
	if err != nil {
		return nil, err
	}
	return &ScreenshotClip{
		X:      clip.X + clip.Width/2,
		Y:      clip.Y + clip.Height/2,
		Width:  clip.Width,
		Height: clip.Height,
		Scale:  clip.Scale,
	}, nil
}

func (l *Locator) SetInputFiles(ctx context.Context, paths ...string) error {
	if _, err := normalizeFilePayloads(paths); err != nil {
		return err
	}

	node, release, err := l.resolveNode(ctx)
	if err != nil {
		return err
	}
	defer release()
	return node.session.Send(ctx, "DOM.setFileInputFiles", map[string]any{
		"objectId": node.objectID,
		"files":    paths,
	}, nil)
}

func (d *DeepLocator) First() *DeepLocator {
	return &DeepLocator{Locator: *d.Locator.First()}
}

func (d *DeepLocator) Nth(index int) *DeepLocator {
	return &DeepLocator{Locator: *d.Locator.Nth(index)}
}

func (f *FrameLocator) Resolve(ctx context.Context) (*Frame, error) {
	current := f.root
	for _, segment := range splitSelectorPath(f.selector) {
		next, err := current.resolveChildFrame(ctx, segment, true)
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func (f *FrameLocator) Locator(selector string) *Locator {
	frame, err := f.Resolve(context.Background())
	if err != nil {
		return &Locator{frame: f.root, selector: selector, pierceShadow: true}
	}
	return frame.Locator(selector)
}

func (f *FrameLocator) FrameLocator(selector string) *FrameLocator {
	joined := f.selector
	if joined != "" {
		joined += " >> "
	}
	joined += selector
	return &FrameLocator{page: f.page, root: f.root, selector: joined}
}

func (f *FrameLocator) Click(ctx context.Context, selector string) error {
	frame, err := f.Resolve(ctx)
	if err != nil {
		return err
	}
	return frame.Locator(selector).Click(ctx)
}

func buildLocatorInvocation(selector string, pierceShadow bool, index int, body string) string {
	return fmt.Sprintf(`(() => {
		%s
		const elements = queryAll(document, %q, %t);
		const el = elements[%d];
		if (!el) throw new Error("element not found");
		%s
	})()`, selectorQueryPrelude(), selector, pierceShadow, index, body)
}

func splitSelectorPath(selector string) []string {
	parts := strings.Split(selector, ">>")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func quoteStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func (l *Locator) resolveNode(ctx context.Context) (*resolvedNode, func(), error) {
	return l.frame.resolveSelector(ctx, l.selector, selectorResolveOptions{
		pierceShadow: l.pierceShadow,
		index:        l.index,
	})
}

func (l *Locator) resolveElement(ctx context.Context) (*resolvedElement, func(), error) {
	return l.frame.resolveElement(ctx, l.selector, selectorResolveOptions{
		pierceShadow: l.pierceShadow,
		index:        l.index,
	})
}

func (l *Locator) clickAtGeometry(ctx context.Context, clickCount int) error {
	elem, release, err := l.awaitActionable(ctx, DefaultActionabilityOptions())
	if err != nil {
		return err
	}
	defer release()
	page := l.frame.page
	// Humanize handles single-clicks only; double-clicks fall through to the
	// raw two-press CDP path so the timing between presses stays a single,
	// consistent value (matching what Page.Click does).
	if clickCount == 1 && page.HumanizeEnabled() {
		geom := elem.Geometry()
		if geom != nil && geom.Width > 0 && geom.Height > 0 {
			return page.performHumanizedClick(ctx, humanize.Box{
				X:      geom.X,
				Y:      geom.Y,
				Width:  geom.Width,
				Height: geom.Height,
			}, elem.IsInput())
		}
	}
	cx, cy := elem.Centroid()
	return page.Click(ctx, cx, cy, clickCount)
}

func (l *Locator) withResolvedNode(ctx context.Context, fn func(*resolvedNode) error) error {
	node, release, err := l.resolveNode(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(node)
}

func (n *resolvedNode) callFunctionBool(ctx context.Context, body string) (bool, error) {
	var result bool
	err := n.callFunction(ctx, body, &result)
	return result, err
}

func selectorQueryPrelude() string {
	return `
		const isXPathSelector = (selector) => selector.startsWith("/") || selector.startsWith("xpath=");
		const queryXPath = (root, selector) => {
			const expr = selector.startsWith("xpath=") ? selector.slice(6) : selector;
			const doc = root.ownerDocument || document;
			const snapshot = doc.evaluate(expr, root, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
			const matches = [];
			for (let i = 0; i < snapshot.snapshotLength; i++) {
				const item = snapshot.snapshotItem(i);
				if (item && item.nodeType === Node.ELEMENT_NODE) matches.push(item);
			}
			return matches;
		};
		const queryCss = (root, selector) => root.querySelectorAll ? Array.from(root.querySelectorAll(selector)) : [];
		const queryAll = (root, selector, pierceShadow) => {
			const results = [];
			const visit = (node) => {
				if (!node) return;
				const matches = isXPathSelector(selector) ? queryXPath(node, selector) : queryCss(node, selector);
				for (const match of matches) {
					if (!results.includes(match)) results.push(match);
				}
				if (!pierceShadow) return;
				const descendants = node.querySelectorAll ? node.querySelectorAll("*") : [];
				for (const el of descendants) {
					if (el.shadowRoot) visit(el.shadowRoot);
				}
			};
			visit(root);
			return results;
		};
	`
}
