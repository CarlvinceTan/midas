package dom

import (
	"fmt"
	"strings"
)

func BootstrapScript() string {
	return `(() => {
		if (globalThis.__polymuxSelectorHelper) return true;
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
		const isVisible = (el) => {
			if (!el || !el.isConnected) return false;
			const style = getComputedStyle(el);
			if (style.visibility === "hidden" || style.display === "none") return false;
			const rect = el.getBoundingClientRect();
			return rect.width > 0 && rect.height > 0;
		};
		const isEnabled = (el) => {
			if (!el) return false;
			if (el.disabled) return false;
			const tagName = el.tagName ? el.tagName.toLowerCase() : "";
			if (tagName === "input" || tagName === "textarea" || tagName === "select" || tagName === "button" || tagName === "option" || tagName === "optgroup") {
				return !el.disabled;
			}
			return true;
		};
		const isEditable = (el) => {
			if (!el || !isEnabled(el)) return false;
			const tagName = el.tagName ? el.tagName.toLowerCase() : "";
			if (tagName === "input") {
				const inputType = (el.type || "text").toLowerCase();
				const readOnlyTypes = ["checkbox", "radio", "submit", "button", "image", "file", "range", "color"];
				if (readOnlyTypes.includes(inputType)) return false;
				return !el.readOnly;
			}
			if (tagName === "textarea") return !el.readOnly;
			if (el.isContentEditable) return true;
			return false;
		};
		const getGeometry = (el) => {
			if (!el || !el.isConnected) return null;
			el.scrollIntoView({ block: "center", inline: "center" });
			const rect = el.getBoundingClientRect();
			if (rect.width <= 0 || rect.height <= 0) return null;
			return { x: rect.x, y: rect.y, width: rect.width, height: rect.height, scale: 1 };
		};
		const getElementState = (el) => {
			if (!el) return null;
			const tagName = el.tagName ? el.tagName.toLowerCase() : "";
			const inputType = tagName === "input" ? (el.type || "text").toLowerCase() : "";
			return {
				visible: isVisible(el),
				enabled: isEnabled(el),
				editable: isEditable(el),
				tagName: tagName,
				inputType: inputType
			};
		};
		globalThis.__polymuxSelectorHelper = {
			resolve(selector, pierceShadow, index) {
				const elements = queryAll(document, selector, pierceShadow);
				return elements[index] || null;
			},
			count(selector, pierceShadow) {
				return queryAll(document, selector, pierceShadow).length;
			},
			matchesState(selector, state, pierceShadow) {
				const elements = queryAll(document, selector, pierceShadow);
				switch (state) {
				case "attached":
					return elements.length > 0;
				case "detached":
					return elements.length === 0;
				case "hidden":
					return elements.length === 0 || !isVisible(elements[0]);
				default:
					return elements.length > 0 && isVisible(elements[0]);
				}
			},
			actionability(selector, pierceShadow, index) {
				const elements = queryAll(document, selector, pierceShadow);
				const el = elements[index];
				if (!el) return { found: false, error: "element not found" };
				const state = getElementState(el);
				const geometry = getGeometry(el);
				return { found: true, state, geometry };
			}
		};
		return true;
	})()`
}

func BuildContextInvocation(name HelperName, args ...any) string {
	payloads := make([]string, 0, len(args))
	for _, arg := range args {
		payloads = append(payloads, fmt.Sprintf("%#v", arg))
	}
	return `globalThis.__polymuxSelectorHelper.` + string(name) + `(` + strings.Join(payloads, ", ") + `)`
}

func BuildElementInvocation(name HelperName, args ...string) string {
	argsStr := ""
	for i, arg := range args {
		if i > 0 {
			argsStr += ", "
		}
		argsStr += arg
	}
	return `globalThis.__polymuxSelectorHelper.` + string(name) + `.call(this` + argsStr + `)`
}
