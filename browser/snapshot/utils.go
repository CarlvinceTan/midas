package snapshot

import (
	"encoding/json"
	"fmt"
	"strings"
)

func extractAXURL(node map[string]any) string {
	props, _ := node["properties"].([]any)
	for _, prop := range props {
		asMap, _ := prop.(map[string]any)
		if asString(asMap["name"]) != "url" {
			continue
		}
		return cleanSnapshotText(asAXValue(asMap["value"]))
	}
	if propsMap, ok := node["properties"].([]map[string]any); ok {
		for _, prop := range propsMap {
			if asString(prop["name"]) != "url" {
				continue
			}
			return cleanSnapshotText(asAXValue(prop["value"]))
		}
	}
	return ""
}

func asAXValue(v any) string {
	asMap, ok := v.(map[string]any)
	if !ok {
		return cleanSnapshotText(asString(v))
	}
	return cleanSnapshotText(asString(asMap["value"]))
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case float64:
		return fmt.Sprintf("%.0f", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	default:
		return ""
	}
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneJSON[T any](v T) T {
	var zero T
	buf, err := json.Marshal(v)
	if err != nil {
		return zero
	}
	if err := json.Unmarshal(buf, &zero); err != nil {
		return zero
	}
	return zero
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

func selectorQueryPrelude() string {
	return `
		const isXPathSelector = (selector) => selector.startsWith("/") || selector.startsWith("xpath=");
		const queryXPath = (root, selector) => {
			const expr = selector.startsWith("xpath=") ? selector.slice(6) : selector;
			const doc = root.ownerDocument || root;
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
			const stack = [root];
			while (stack.length) {
				const node = stack.shift();
				const matches = isXPathSelector(selector) ? queryXPath(node, selector) : queryCss(node, selector);
				for (const match of matches) results.push(match);
				const children = node.children ? Array.from(node.children) : [];
				for (const child of children) {
					stack.push(child);
					if (pierceShadow && child.shadowRoot) stack.push(child.shadowRoot);
				}
			}
			return results;
		};`
}
