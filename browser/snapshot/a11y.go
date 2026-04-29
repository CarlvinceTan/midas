package snapshot

import (
	"context"
	"fmt"
	"strings"
)

type accessibilityTreeResult struct {
	Outline      string
	URLMap       map[string]string
	ScopeApplied bool
}

type a11yOptions struct {
	FocusSelector string
	Experimental  bool
	TagNameMap    map[string]string
	ScrollableMap map[string]bool
	Encode        func(int) string
}

func a11yForFrame(ctx context.Context, session Session, frameID string, opts a11yOptions) (*accessibilityTreeResult, error) {
	_ = session.Send(ctx, "Accessibility.enable", nil, nil)
	_ = session.Send(ctx, "Runtime.enable", nil, nil)
	_ = session.Send(ctx, "DOM.enable", nil, nil)

	var res struct {
		Nodes []map[string]any `json:"nodes"`
	}
	params := map[string]any{}
	if frameID != "" {
		params["frameId"] = frameID
	}
	if err := session.Send(ctx, "Accessibility.getFullAXTree", params, &res); err != nil {
		if frameID == "" {
			return nil, err
		}
		msg := err.Error()
		if !strings.Contains(msg, "Frame with the given") &&
			!strings.Contains(msg, "does not belong to the target") &&
			!strings.Contains(msg, "is not found") {
			return nil, err
		}
		if err := session.Send(ctx, "Accessibility.getFullAXTree", nil, &res); err != nil {
			return nil, err
		}
	}

	urlMap := make(map[string]string)
	for _, node := range res.Nodes {
		be := asInt(node["backendDOMNodeId"])
		if be <= 0 {
			continue
		}
		if url := extractAXURL(node); url != "" {
			urlMap[opts.Encode(be)] = url
		}
	}

	nodesForOutline := res.Nodes
	scopeApplied := false
	if sel := strings.TrimSpace(opts.FocusSelector); sel != "" {
		objectID, err := resolveObjectIDForSelector(ctx, session, sel, frameID)
		if err == nil && objectID != "" {
			var described struct {
				Node struct {
					BackendNodeID int `json:"backendNodeId"`
				} `json:"node"`
			}
			if err := session.Send(ctx, "DOM.describeNode", map[string]any{
				"objectId": objectID,
			}, &described); err == nil && described.Node.BackendNodeID > 0 {
				targetID := findAXNodeIDByBackendID(res.Nodes, described.Node.BackendNodeID)
				if targetID != "" {
					scopeApplied = true
					nodesForOutline = filterAXSubtree(res.Nodes, targetID)
				}
			}
			_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
				"objectId": objectID,
			}, nil)
		}
	}

	decorated := decorateRoles(nodesForOutline, opts)
	tree := buildHierarchicalTree(decorated, opts)
	lines := make([]string, 0, len(tree))
	for _, node := range tree {
		lines = append(lines, formatTreeLine(node, 0))
	}
	return &accessibilityTreeResult{
		Outline:      strings.TrimSpace(strings.Join(lines, "\n")),
		URLMap:       urlMap,
		ScopeApplied: scopeApplied,
	}, nil
}

func decorateRoles(raw []map[string]any, opts a11yOptions) []*treeA11yNode {
	out := make([]*treeA11yNode, 0, len(raw))
	for _, node := range raw {
		be := asInt(node["backendDOMNodeId"])
		encodedID := ""
		if be > 0 {
			encodedID = opts.Encode(be)
		}
		role := asAXValue(node["role"])
		tag := opts.TagNameMap[encodedID]
		if encodedID != "" && (opts.ScrollableMap[encodedID] || tag == "html") && tag != "#document" {
			tagLabel := strings.TrimPrefix(tag, "#")
			if tagLabel != "" {
				role = "scrollable, " + tagLabel
			} else if role != "" {
				role = "scrollable, " + role
			} else {
				role = "scrollable"
			}
		}
		out = append(out, &treeA11yNode{
			Role:             role,
			Name:             asAXValue(node["name"]),
			Description:      asAXValue(node["description"]),
			Value:            rawAXValue(node["value"]),
			NodeID:           asString(node["nodeId"]),
			BackendDOMNodeID: be,
			ParentID:         asString(node["parentId"]),
			ChildIDs:         asStringSlice(node["childIds"]),
			EncodedID:        encodedID,
		})
	}
	return out
}

func buildHierarchicalTree(nodes []*treeA11yNode, opts a11yOptions) []*treeA11yNode {
	nodeMap := make(map[string]*treeA11yNode, len(nodes))
	for _, node := range nodes {
		keep := (strings.TrimSpace(node.Name) != "") || len(node.ChildIDs) > 0 || !isStructuralRole(node.Role)
		if !keep {
			continue
		}
		copy := *node
		copy.Children = nil
		nodeMap[node.NodeID] = &copy
	}
	for _, node := range nodes {
		if node.ParentID == "" {
			continue
		}
		parent := nodeMap[node.ParentID]
		cur := nodeMap[node.NodeID]
		if parent != nil && cur != nil {
			parent.Children = append(parent.Children, cur)
		}
	}
	var roots []*treeA11yNode
	for _, node := range nodes {
		if node.ParentID == "" {
			if root := pruneStructuralTree(nodeMap[node.NodeID], opts); root != nil {
				roots = append(roots, root)
			}
		}
	}
	return roots
}

func pruneStructuralTree(node *treeA11yNode, opts a11yOptions) *treeA11yNode {
	if node == nil {
		return nil
	}
	if strings.HasPrefix(node.NodeID, "-") {
		return nil
	}
	if len(node.Children) == 0 {
		if isStructuralRole(node.Role) {
			return nil
		}
		return node
	}
	cleanedKids := make([]*treeA11yNode, 0, len(node.Children))
	for _, child := range node.Children {
		if pruned := pruneStructuralTree(child, opts); pruned != nil {
			cleanedKids = append(cleanedKids, pruned)
		}
	}
	cleanedKids = removeRedundantStaticTextChildren(node, cleanedKids)
	if isStructuralRole(node.Role) {
		if len(cleanedKids) == 1 {
			return cleanedKids[0]
		}
		if len(cleanedKids) == 0 {
			return nil
		}
	}
	newRole := node.Role
	if (newRole == "generic" || newRole == "none") && node.EncodedID != "" {
		if tag := opts.TagNameMap[node.EncodedID]; tag != "" {
			newRole = tag
		}
	}
	if newRole == "combobox" && node.EncodedID != "" {
		if tag := opts.TagNameMap[node.EncodedID]; tag == "select" {
			newRole = "select"
		}
	}
	node.Role = newRole
	node.Children = cleanedKids
	return node
}

func removeRedundantStaticTextChildren(parent *treeA11yNode, children []*treeA11yNode) []*treeA11yNode {
	if parent == nil || parent.Name == "" {
		return children
	}
	parentNorm := strings.TrimSpace(normalizeSpaces(parent.Name))
	var combined strings.Builder
	for _, child := range children {
		if child.Role == "StaticText" && child.Name != "" {
			combined.WriteString(strings.TrimSpace(normalizeSpaces(child.Name)))
		}
	}
	if combined.String() != parentNorm {
		return children
	}
	out := make([]*treeA11yNode, 0, len(children))
	for _, child := range children {
		if child.Role != "StaticText" {
			out = append(out, child)
		}
	}
	return out
}

func isStructuralRole(role string) bool {
	switch strings.ToLower(role) {
	case "generic", "none", "inlinetextbox":
		return true
	default:
		return false
	}
}

func resolveObjectIDForSelector(ctx context.Context, session Session, selector, frameID string) (string, error) {
	expr := buildResolveSelectorInvocation(selector)
	params := map[string]any{
		"expression":    expr,
		"returnByValue": false,
		"awaitPromise":  true,
	}
	var res struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := session.Send(ctx, "Runtime.evaluate", params, &res); err != nil {
		return "", err
	}
	if res.ExceptionDetails != nil {
		return "", fmt.Errorf("selector resolution failed: %s", res.ExceptionDetails.Text)
	}
	return res.Result.ObjectID, nil
}

func buildResolveSelectorInvocation(selector string) string {
	return fmt.Sprintf(`(() => {
		%s
		const selector = %q;
		const root = document;
		if (selector.startsWith("/") || selector.startsWith("xpath=")) {
			const expr = selector.startsWith("xpath=") ? selector.slice(6) : selector;
			const node = document.evaluate(expr, root, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null).singleNodeValue;
			return node || null;
		}
		const matches = queryAll(root, selector, true);
		return matches[0] || null;
	})()`, selectorQueryPrelude(), selector)
}

func findAXNodeIDByBackendID(nodes []map[string]any, backendNodeID int) string {
	for _, node := range nodes {
		if asInt(node["backendDOMNodeId"]) == backendNodeID {
			return asString(node["nodeId"])
		}
	}
	return ""
}

func filterAXSubtree(nodes []map[string]any, targetNodeID string) []map[string]any {
	lookup := make(map[string]map[string]any, len(nodes))
	for _, node := range nodes {
		lookup[asString(node["nodeId"])] = node
	}
	keep := map[string]bool{targetNodeID: true}
	queue := []string{targetNodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		node := lookup[current]
		if node == nil {
			continue
		}
		for _, childID := range asStringSlice(node["childIds"]) {
			if !keep[childID] {
				keep[childID] = true
				queue = append(queue, childID)
			}
		}
	}
	out := make([]map[string]any, 0, len(keep))
	for _, node := range nodes {
		id := asString(node["nodeId"])
		if !keep[id] {
			continue
		}
		copied := cloneJSON(node)
		if id == targetNodeID {
			delete(copied, "parentId")
		}
		out = append(out, copied)
	}
	return out
}

func rawAXValue(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	return m["value"]
}

func asStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		if typed, ok := v.([]string); ok {
			return append([]string(nil), typed...)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := asString(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
