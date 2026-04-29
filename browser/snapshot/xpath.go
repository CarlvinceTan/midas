package snapshot

import (
	"context"
	"fmt"
	"strings"
)

func buildAbsoluteXPathFromChain(ctx context.Context, chain []iframeChainStep, leafSession Session, leafBackendNodeID int) string {
	prefix := ""
	for _, step := range chain {
		xp := absoluteXPathForBackendNode(ctx, step.ParentSession, step.IFrameBackendNodeID)
		if xp == "" {
			continue
		}
		if prefix == "" {
			prefix = normalizeXPath(xp)
			continue
		}
		prefix = prefixXPath(prefix, xp)
	}
	leaf := absoluteXPathForBackendNode(ctx, leafSession, leafBackendNodeID)
	if leaf == "" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if prefix == "" {
		return normalizeXPath(leaf)
	}
	return prefixXPath(prefix, leaf)
}

func absoluteXPathForBackendNode(ctx context.Context, session Session, backendNodeID int) string {
	if backendNodeID <= 0 {
		return ""
	}
	var resolved struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := session.Send(ctx, "DOM.resolveNode", map[string]any{
		"backendNodeId": backendNodeID,
	}, &resolved); err != nil || resolved.Object.ObjectID == "" {
		return ""
	}
	defer func() {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": resolved.Object.ObjectID,
		}, nil)
	}()

	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := session.Send(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            resolved.Object.ObjectID,
		"functionDeclaration": nodeToAbsoluteXPathJS(),
		"returnByValue":       true,
	}, &res); err != nil {
		return ""
	}
	return normalizeXPath(res.Result.Value)
}

func prefixXPath(parentAbs, child string) string {
	p := strings.TrimSuffix(parentAbs, "/")
	if parentAbs == "/" {
		p = ""
	}
	if child == "" || child == "/" {
		if p == "" {
			return "/"
		}
		return p
	}
	if strings.HasPrefix(child, "//") {
		if p == "" {
			return child
		}
		return p + child
	}
	c := strings.TrimPrefix(child, "/")
	if p == "" {
		return "/" + c
	}
	return p + "/" + c
}

func normalizeXPath(x string) string {
	if x == "" {
		return ""
	}
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(x, "xpath="), "XPath="))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

func buildChildXPathSegments(kids []domNode) []string {
	segs := make([]string, 0, len(kids))
	counts := map[string]int{}
	for _, child := range kids {
		tag := strings.ToLower(child.NodeName)
		key := fmt.Sprintf("%d:%s", child.NodeType, tag)
		counts[key]++
		idx := counts[key]
		switch child.NodeType {
		case 3:
			segs = append(segs, fmt.Sprintf("text()[%d]", idx))
		case 8:
			segs = append(segs, fmt.Sprintf("comment()[%d]", idx))
		default:
			if strings.Contains(tag, ":") {
				segs = append(segs, fmt.Sprintf("*[name()='%s'][%d]", tag, idx))
			} else {
				segs = append(segs, fmt.Sprintf("%s[%d]", tag, idx))
			}
		}
	}
	return segs
}

func joinXPath(base, step string) string {
	if step == "//" {
		if base == "" || base == "/" {
			return "//"
		}
		if strings.HasSuffix(base, "/") {
			return base + "/"
		}
		return base + "//"
	}
	if base == "" || base == "/" {
		if step == "" {
			return "/"
		}
		return "/" + step
	}
	if strings.HasSuffix(base, "//") {
		return base + step
	}
	if step == "" {
		return base
	}
	return base + "/" + step
}

func relativizeXPath(baseAbs, nodeAbs string) string {
	base := normalizeXPath(baseAbs)
	abs := normalizeXPath(nodeAbs)
	if abs == base {
		return "/"
	}
	if strings.HasPrefix(abs, base) {
		tail := abs[len(base):]
		if tail == "" {
			return "/"
		}
		if strings.HasPrefix(tail, "/") || strings.HasPrefix(tail, "//") {
			return tail
		}
		return "/" + tail
	}
	if base == "/" {
		return abs
	}
	return abs
}

func findNodeByBackendID(root domNode, backendNodeID int) *domNode {
	stack := []*domNode{&root}
	for len(stack) > 0 {
		last := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if last.BackendNodeID == backendNodeID {
			return last
		}
		for i := len(last.Children) - 1; i >= 0; i-- {
			stack = append(stack, &last.Children[i])
		}
		for i := len(last.ShadowRoots) - 1; i >= 0; i-- {
			stack = append(stack, &last.ShadowRoots[i])
		}
		if last.ContentDocument != nil {
			stack = append(stack, last.ContentDocument)
		}
	}
	return nil
}

func nodeToAbsoluteXPathJS() string {
	return `function() {
		function idx(node) {
			let i = 1;
			let sib = node.previousSibling;
			while (sib) {
				if (sib.nodeType === node.nodeType && sib.nodeName === node.nodeName) i++;
				sib = sib.previousSibling;
			}
			return i;
		}
		function seg(node) {
			if (!node) return "";
			if (node.nodeType === Node.TEXT_NODE) return "text()[" + idx(node) + "]";
			if (node.nodeType === Node.COMMENT_NODE) return "comment()[" + idx(node) + "]";
			const name = (node.nodeName || "").toLowerCase();
			if (name.includes(":")) return "*[name()='" + name + "'][" + idx(node) + "]";
			return name + "[" + idx(node) + "]";
		}
		let node = this;
		const parts = [];
		while (node) {
			if (node.nodeType === Node.DOCUMENT_NODE) break;
			if (node.nodeType === Node.DOCUMENT_FRAGMENT_NODE && node.host) {
				parts.unshift("");
				node = node.host;
				continue;
			}
			parts.unshift(seg(node));
			node = node.parentNode;
		}
		const filtered = parts.filter(Boolean);
		return "/" + filtered.join("/");
	}`
}
