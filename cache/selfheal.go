package cache

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/carlvincetan/polymux/internal/midas/browser"
)

type HealLogger interface {
	Debug(msg string, keys ...string)
	Warn(msg string, keys ...string)
}

type nopLogger struct{}

func (nopLogger) Debug(msg string, keys ...string) {}
func (nopLogger) Warn(msg string, keys ...string)  {}

type defaultLogger struct{}

func (defaultLogger) Debug(msg string, keys ...string) {}
func (defaultLogger) Warn(msg string, keys ...string) {
	var b strings.Builder
	b.WriteString("[polymux cache] WARN: ")
	b.WriteString(msg)
	for i := 0; i < len(keys); i += 2 {
		if i+1 < len(keys) {
			fmt.Fprintf(&b, " %s=%s", keys[i], keys[i+1])
		}
	}
	fmt.Fprintln(os.Stderr, b.String())
}

type HealedAction struct {
	OriginalSelector string
	HealedSelector   string
	Strategy         string
}

type SelfHealConfig struct {
	Enabled bool
	Timeout time.Duration
	Logger  HealLogger
}

type SelfHealer struct {
	page    *browser.Page
	logger  HealLogger
	timeout time.Duration
}

func NewSelfHealer(page *browser.Page, cfg SelfHealConfig) *SelfHealer {
	logger := cfg.Logger
	if logger == nil {
		logger = defaultLogger{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &SelfHealer{
		page:    page,
		logger:  logger,
		timeout: timeout,
	}
}

type selectorInfo struct {
	isXPath bool
	xpath   string
	css     string
	tag     string
	id      string
	name    string
	classes []string
	attrs   map[string]string
}

type elementSignature struct {
	text        string
	role        string
	name        string
	tag         string
	id          string
	nameAttr    string
	typeAttr    string
	value       string
	placeholder string
	classes     []string
	dataAttrs   map[string]string
	xpath       string
	xpathParts  []xpathSegment
}

type xpathSegment struct {
	tag      string
	index    int
	attrs    map[string]string
	textPred string
}

func (h *SelfHealer) Heal(ctx context.Context, action Action) (*HealedAction, error) {
	selector := action.Selector
	if selector == "" {
		return nil, fmt.Errorf("cannot heal empty selector")
	}

	info := parseSelector(selector)
	if info == nil {
		return nil, fmt.Errorf("failed to parse selector: %s", selector)
	}

	snap, err := h.page.Snapshot(ctx)
	if err != nil {
		h.logger.Warn("self-heal: failed to capture snapshot", "error", err.Error())
		return nil, err
	}

	strategies := []struct {
		name string
		fn   func(context.Context, *selectorInfo, *browser.SnapshotResult) (string, error)
	}{
		{"text_match", h.textMatch},
		{"aria_match", h.ariaMatch},
		{"attr_match", h.attrMatch},
		{"xpath_relax", h.xpathRelaxation},
	}

	for _, strategy := range strategies {
		healedSelector, err := strategy.fn(ctx, info, snap)
		if err != nil {
			continue
		}
		if healedSelector == "" {
			continue
		}

		if healedSelector == selector {
			continue
		}

		verified, err := h.verifySelector(ctx, healedSelector)
		if err != nil || !verified {
			continue
		}

		return &HealedAction{
			OriginalSelector: selector,
			HealedSelector:   healedSelector,
			Strategy:         strategy.name,
		}, nil
	}

	return nil, fmt.Errorf("self-heal failed: no matching element found for selector: %s", selector)
}

func (h *SelfHealer) textMatch(ctx context.Context, info *selectorInfo, snap *browser.SnapshotResult) (string, error) {
	sig := extractSignatureFromXPath(info.xpath)
	if sig == nil {
		return "", fmt.Errorf("cannot extract signature from selector")
	}

	if sig.text != "" {
		healed, err := h.findByText(ctx, sig.text)
		if err == nil && healed != "" {
			return healed, nil
		}
	}

	if sig.placeholder != "" {
		healed, err := h.findByPlaceholder(ctx, sig.placeholder)
		if err == nil && healed != "" {
			return healed, nil
		}
	}

	if sig.value != "" && sig.tag == "input" {
		healed, err := h.findByValue(ctx, sig.value)
		if err == nil && healed != "" {
			return healed, nil
		}
	}

	return "", fmt.Errorf("no text match found")
}

func (h *SelfHealer) ariaMatch(ctx context.Context, info *selectorInfo, snap *browser.SnapshotResult) (string, error) {
	sig := extractSignatureFromXPath(info.xpath)
	if sig == nil {
		return "", fmt.Errorf("cannot extract signature for aria match")
	}

	role := sig.role
	name := sig.name

	if role == "" && sig.tag != "" {
		role = tagToRole(sig.tag)
	}

	if role == "" || name == "" {
		return "", fmt.Errorf("insufficient aria information")
	}

	healed, err := h.findByAria(ctx, role, name)
	if err != nil {
		return "", err
	}

	return healed, nil
}

func (h *SelfHealer) attrMatch(ctx context.Context, info *selectorInfo, snap *browser.SnapshotResult) (string, error) {
	sig := extractSignatureFromXPath(info.xpath)
	if sig == nil {
		return "", fmt.Errorf("cannot extract signature for attr match")
	}

	if sig.id != "" {
		healed := fmt.Sprintf(`xpath=//*[@id=%q]`, sig.id)
		verified, err := h.verifySelector(ctx, healed)
		if err == nil && verified {
			return healed, nil
		}
	}

	if sig.nameAttr != "" {
		healed := fmt.Sprintf(`xpath=//*[@name=%q]`, sig.nameAttr)
		verified, err := h.verifySelector(ctx, healed)
		if err == nil && verified {
			return healed, nil
		}
	}

	if sig.typeAttr != "" && sig.tag != "" {
		healed := fmt.Sprintf(`xpath=//%s[@type=%q]`, sig.tag, sig.typeAttr)
		verified, err := h.verifySelector(ctx, healed)
		if err == nil && verified {
			return healed, nil
		}
	}

	if len(sig.classes) > 0 {
		for _, class := range sig.classes {
			healed := fmt.Sprintf(`xpath=//*[contains(@class,%q)]`, class)
			verified, err := h.verifySelector(ctx, healed)
			if err == nil && verified {
				return healed, nil
			}
		}
	}

	for key, value := range sig.dataAttrs {
		healed := fmt.Sprintf(`xpath=//*[@%s=%q]`, key, value)
		verified, err := h.verifySelector(ctx, healed)
		if err == nil && verified {
			return healed, nil
		}
	}

	return "", fmt.Errorf("no attr match found")
}

func (h *SelfHealer) xpathRelaxation(ctx context.Context, info *selectorInfo, snap *browser.SnapshotResult) (string, error) {
	if !info.isXPath {
		return "", fmt.Errorf("xpath relaxation only works for xpath selectors")
	}

	segments := parseXPathSegments(info.xpath)
	if len(segments) < 2 {
		return "", fmt.Errorf("xpath too short to relax")
	}

	tag := segments[len(segments)-1].tag
	if tag == "" {
		tag = "*"
	}

	lastSeg := segments[len(segments)-1]

	for i := len(segments) - 1; i >= 0; i-- {
		prefix := segments[:i]

		var prefixStr string
		for _, seg := range prefix {
			prefixStr += fmt.Sprintf("/%s", seg.tag)
			if seg.index > 0 {
				prefixStr += fmt.Sprintf("[%d]", seg.index)
			}
		}

		var relaxed string
		if len(lastSeg.attrs) > 0 {
			var preds []string
			for k, v := range lastSeg.attrs {
				preds = append(preds, fmt.Sprintf("@%s=%q", k, v))
			}
			relaxed = fmt.Sprintf("%s//%s[%s]", prefixStr, tag, strings.Join(preds, " and "))
		} else if lastSeg.textPred != "" {
			relaxed = fmt.Sprintf("%s//%s[%s]", prefixStr, tag, lastSeg.textPred)
		} else if tag != "*" {
			relaxed = fmt.Sprintf("%s//%s", prefixStr, tag)
		} else {
			continue
		}

		verified, err := h.verifySelector(ctx, relaxed)
		if err == nil && verified {
			return relaxed, nil
		}
	}

	sig := extractSignatureFromXPath(info.xpath)
	if sig != nil && sig.tag != "" {
		tag := sig.tag
		for _, attr := range []string{"id", "name"} {
			var attrVal string
			if attr == "id" && sig.id != "" {
				attrVal = sig.id
			} else if attr == "name" && sig.nameAttr != "" {
				attrVal = sig.nameAttr
			}
			if attrVal != "" {
				relaxed := fmt.Sprintf(`xpath=//%s[@%s=%q]`, tag, attr, attrVal)
				verified, err := h.verifySelector(ctx, relaxed)
				if err == nil && verified {
					return relaxed, nil
				}
			}
		}
	}

	return "", fmt.Errorf("xpath relaxation failed")
}

func (h *SelfHealer) verifySelector(ctx context.Context, selector string) (bool, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	locator := h.page.Locator(selector)
	count, err := locator.Count(timeoutCtx)
	if err != nil {
		return false, err
	}

	if count == 0 {
		return false, fmt.Errorf("element not found")
	}

	visible, err := locator.First().IsVisible(timeoutCtx)
	if err != nil {
		return false, err
	}

	return visible, nil
}

func (h *SelfHealer) findByText(ctx context.Context, text string) (string, error) {
	escaped := escapeXPathString(text)
	selector := fmt.Sprintf(`xpath=//*[contains(normalize-space(text()), %s)]`, escaped)
	verified, err := h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}

	selector = fmt.Sprintf(`xpath=//*[text()=%s]`, escaped)
	verified, err = h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}

	return "", fmt.Errorf("text match failed")
}

func (h *SelfHealer) findByPlaceholder(ctx context.Context, placeholder string) (string, error) {
	escaped := escapeXPathString(placeholder)
	selector := fmt.Sprintf(`xpath=//input[@placeholder=%s] | //textarea[@placeholder=%s]`, escaped, escaped)
	verified, err := h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}
	return "", fmt.Errorf("placeholder match failed")
}

func (h *SelfHealer) findByValue(ctx context.Context, value string) (string, error) {
	escaped := escapeXPathString(value)
	selector := fmt.Sprintf(`xpath=//input[@value=%s]`, escaped)
	verified, err := h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}
	return "", fmt.Errorf("value match failed")
}

func (h *SelfHealer) findByAria(ctx context.Context, role, name string) (string, error) {
	escaped := escapeXPathString(name)
	selector := fmt.Sprintf(`xpath=//*[contains(@role, %s)][contains(@aria-label, %s) or contains(@title, %s) or contains(text(), %s)]`, escapeXPathString(role), escaped, escaped, escaped)
	verified, err := h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}

	selector = fmt.Sprintf(`xpath=//%s[contains(text(), %s)]`, roleToTag(role), escaped)
	verified, err = h.verifySelector(ctx, selector)
	if err == nil && verified {
		return selector, nil
	}

	return "", fmt.Errorf("aria match failed")
}

func parseSelector(selector string) *selectorInfo {
	if strings.HasPrefix(selector, "xpath=") {
		xpath := strings.TrimPrefix(selector, "xpath=")
		return &selectorInfo{
			isXPath: true,
			xpath:   xpath,
		}
	}

	if strings.HasPrefix(selector, "/") {
		return &selectorInfo{
			isXPath: true,
			xpath:   selector,
		}
	}

	return &selectorInfo{
		isXPath: false,
		css:     selector,
	}
}

func extractSignatureFromXPath(xpath string) *elementSignature {
	sig := &elementSignature{
		dataAttrs: make(map[string]string),
	}

	segments := parseXPathSegments(xpath)
	if len(segments) == 0 {
		return nil
	}

	lastSeg := segments[len(segments)-1]
	sig.tag = lastSeg.tag
	sig.xpath = xpath
	sig.xpathParts = segments

	for k, v := range lastSeg.attrs {
		switch strings.ToLower(k) {
		case "id":
			sig.id = v
		case "name":
			sig.nameAttr = v
		case "type":
			sig.typeAttr = v
		case "value":
			sig.value = v
		case "placeholder":
			sig.placeholder = v
		case "role":
			sig.role = v
		case "aria-label", "arialabel":
			sig.name = v
		case "title":
			if sig.name == "" {
				sig.name = v
			}
		case "class":
			sig.classes = strings.Fields(v)
		default:
			if strings.HasPrefix(k, "data-") {
				sig.dataAttrs[k] = v
			}
		}
	}

	sig.text = lastSeg.textPred

	return sig
}

func parseXPathSegments(xpath string) []xpathSegment {
	xpath = strings.TrimPrefix(xpath, "xpath=")

	var segments []xpathSegment

	parts := strings.Split(xpath, "/")
	for _, part := range parts {
		if part == "" {
			continue
		}

		if strings.HasPrefix(part, "//") {
			part = strings.TrimPrefix(part, "/")
		}

		seg := xpathSegment{
			attrs: make(map[string]string),
		}

		predStart := strings.Index(part, "[")
		if predStart == -1 {
			seg.tag = part
			segments = append(segments, seg)
			continue
		}

		seg.tag = part[:predStart]

		predStr := part[predStart:]
		preds := parsePredicates(predStr)

		for _, pred := range preds {
			if strings.HasPrefix(pred, "@") {
				kv := strings.TrimPrefix(pred, "@")
				if eq := strings.Index(kv, "="); eq != -1 {
					key := kv[:eq]
					val := strings.Trim(kv[eq+1:], `'"`)
					seg.attrs[key] = val
				} else if strings.HasPrefix(kv, "contains") {
					containsMatch := containsAttrRegex.FindStringSubmatch(kv)
					if len(containsMatch) == 3 {
						seg.attrs[containsMatch[1]] = containsMatch[2]
					}
				}
			} else if strings.HasPrefix(pred, "text()") {
				textMatch := textPredRegex.FindStringSubmatch(pred)
				if len(textMatch) == 2 {
					seg.textPred = textMatch[0]
				}
			} else if num, ok := isIndex(pred); ok {
				seg.index = num
			}
		}

		segments = append(segments, seg)
	}

	return segments
}

var containsAttrRegex = regexp.MustCompile(`contains\s*\(\s*@(\w+)\s*,\s*['"]([^'"]+)['"]\s*\)`)
var textPredRegex = regexp.MustCompile(`text\(\)\s*(?:=|contains)\s*\([^)]+\)`)

func parsePredicates(s string) []string {
	var preds []string
	depth := 0
	start := -1

	for i, c := range s {
		if c == '[' {
			if depth == 0 {
				start = i + 1
			}
			depth++
		} else if c == ']' {
			depth--
			if depth == 0 && start != -1 {
				preds = append(preds, s[start:i])
				start = -1
			}
		}
	}

	return preds
}

func isIndex(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return 0, false
	}

	if s[0] >= '0' && s[0] <= '9' {
		var num int
		_, err := fmt.Sscanf(s, "%d", &num)
		if err == nil {
			return num, true
		}
	}

	return 0, false
}

func escapeXPathString(s string) string {
	if strings.Contains(s, `"`) && strings.Contains(s, `'`) {
		escaped := strings.ReplaceAll(s, `"`, `&quot;`)
		return fmt.Sprintf(`"%s"`, escaped)
	}
	if strings.Contains(s, `"`) {
		return fmt.Sprintf(`'%s'`, s)
	}
	return fmt.Sprintf(`"%s"`, s)
}

func tagToRole(tag string) string {
	switch strings.ToLower(tag) {
	case "button":
		return "button"
	case "a":
		return "link"
	case "input":
		return "textbox"
	case "textarea":
		return "textbox"
	case "select":
		return "combobox"
	case "img":
		return "img"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "heading"
	case "ul", "ol":
		return "list"
	case "li":
		return "listitem"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "article":
		return "article"
	case "section":
		return "region"
	case "form":
		return "form"
	case "table":
		return "table"
	case "tr":
		return "row"
	case "td", "th":
		return "cell"
	case "label":
		return "label"
	case "checkbox":
		return "checkbox"
	case "radio":
		return "radio"
	default:
		return ""
	}
}

func roleToTag(role string) string {
	switch strings.ToLower(role) {
	case "button":
		return "button"
	case "link":
		return "a"
	case "textbox":
		return "input"
	case "combobox":
		return "select"
	case "img":
		return "img"
	case "heading":
		return "h1 | h2 | h3 | h4 | h5 | h6"
	case "list":
		return "ul | ol"
	case "listitem":
		return "li"
	case "checkbox":
		return "input[@type='checkbox']"
	case "radio":
		return "input[@type='radio']"
	default:
		return "*"
	}
}
