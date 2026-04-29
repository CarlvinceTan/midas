package snapshot

import (
	"context"
	"fmt"
	"strings"
)

type domDocumentResult struct {
	Root domNode `json:"root"`
}

type domNode struct {
	NodeID          int       `json:"nodeId"`
	NodeType        int       `json:"nodeType"`
	NodeName        string    `json:"nodeName"`
	BackendNodeID   int       `json:"backendNodeId"`
	Attributes      []string  `json:"attributes"`
	ChildNodeCount  int       `json:"childNodeCount"`
	IsScrollable    bool      `json:"isScrollable"`
	Children        []domNode `json:"children"`
	ShadowRoots     []domNode `json:"shadowRoots"`
	ContentDocument *domNode  `json:"contentDocument"`
}

type frameContext struct {
	RootID        string
	ParentByFrame map[string]string
	Frames        []string
}

type sessionDOMIndex struct {
	RootBackend            int
	AbsByBackend           map[int]string
	TagByBackend           map[int]string
	ScrollableByBackend    map[int]bool
	DocRootOf              map[int]int
	ContentDocRootByIFrame map[int]int
}

type frameDOMMaps struct {
	TagNameMap    map[string]string
	XPathMap      map[string]string
	ScrollableMap map[string]bool
	URLMap        map[string]string
}

type sessionDOMMaps struct {
	TagNameMap    map[string]string
	XPathMap      map[string]string
	ScrollableMap map[string]bool
}

func buildFrameContext(page Page) frameContext {
	rootID := page.MainFrameID()
	tree := page.FrameTree(rootID)
	parentByFrame := make(map[string]string)
	var index func(FrameNode, string)
	index = func(node FrameNode, parent string) {
		parentByFrame[node.Frame.ID] = parent
		for _, child := range node.ChildFrames {
			index(child, node.Frame.ID)
		}
	}
	index(tree, "")
	return frameContext{
		RootID:        rootID,
		ParentByFrame: parentByFrame,
		Frames:        page.FrameIDs(),
	}
}

func buildSessionIndexes(ctx context.Context, page Page, frameIDs []string, pierce bool) (map[string]*sessionDOMIndex, error) {
	indexes := make(map[string]*sessionDOMIndex)
	sessions := make(map[string]Session)
	for _, frameID := range frameIDs {
		session := page.SessionForFrame(frameID)
		key := session.ID()
		if key == "" {
			key = "root"
		}
		sessions[key] = session
	}
	for key, session := range sessions {
		idx, err := buildSessionDOMIndex(ctx, session, pierce)
		if err != nil {
			return nil, err
		}
		indexes[key] = idx
	}
	return indexes, nil
}

func buildSessionDOMIndex(ctx context.Context, session Session, pierce bool) (*sessionDOMIndex, error) {
	root, err := getDOMTreeWithFallback(ctx, session, pierce)
	if err != nil {
		return nil, err
	}
	rootBackend := root.BackendNodeID
	out := &sessionDOMIndex{
		RootBackend:            rootBackend,
		AbsByBackend:           make(map[int]string),
		TagByBackend:           make(map[int]string),
		ScrollableByBackend:    make(map[int]bool),
		DocRootOf:              make(map[int]int),
		ContentDocRootByIFrame: make(map[int]int),
	}

	type entry struct {
		Node      *domNode
		XPath     string
		DocRootBE int
	}
	stack := []entry{{Node: root, XPath: "/", DocRootBE: rootBackend}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := current.Node
		if node.BackendNodeID > 0 {
			out.AbsByBackend[node.BackendNodeID] = current.XPath
			out.TagByBackend[node.BackendNodeID] = strings.ToLower(node.NodeName)
			if node.IsScrollable {
				out.ScrollableByBackend[node.BackendNodeID] = true
			}
			out.DocRootOf[node.BackendNodeID] = current.DocRootBE
		}

		if node.ContentDocument != nil && node.BackendNodeID > 0 && node.ContentDocument.BackendNodeID > 0 {
			out.ContentDocRootByIFrame[node.BackendNodeID] = node.ContentDocument.BackendNodeID
			stack = append(stack, entry{
				Node:      node.ContentDocument,
				XPath:     current.XPath,
				DocRootBE: node.ContentDocument.BackendNodeID,
			})
		}

		for i := len(node.ShadowRoots) - 1; i >= 0; i-- {
			stack = append(stack, entry{
				Node:      &node.ShadowRoots[i],
				XPath:     joinXPath(current.XPath, "//"),
				DocRootBE: current.DocRootBE,
			})
		}
		if len(node.Children) > 0 {
			segs := buildChildXPathSegments(node.Children)
			for i := len(node.Children) - 1; i >= 0; i-- {
				stack = append(stack, entry{
					Node:      &node.Children[i],
					XPath:     joinXPath(current.XPath, segs[i]),
					DocRootBE: current.DocRootBE,
				})
			}
		}
	}
	return out, nil
}

func domMapsForSession(ctx context.Context, session Session, frameID string, pierce bool, encode func(string, int) string, attemptOwnerLookup bool) (*sessionDOMMaps, error) {
	root, err := getDOMTreeWithFallback(ctx, session, pierce)
	if err != nil {
		return nil, err
	}
	start := root
	if attemptOwnerLookup {
		var owner struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		if err := session.Send(ctx, "DOM.getFrameOwner", map[string]any{
			"frameId": frameID,
		}, &owner); err == nil && owner.BackendNodeID > 0 {
			if ownerEl := findNodeByBackendID(*root, owner.BackendNodeID); ownerEl != nil && ownerEl.ContentDocument != nil {
				start = ownerEl.ContentDocument
			}
		}
	}

	out := &sessionDOMMaps{
		TagNameMap:    make(map[string]string),
		XPathMap:      make(map[string]string),
		ScrollableMap: make(map[string]bool),
	}
	type entry struct {
		Node  *domNode
		XPath string
	}
	stack := []entry{{Node: start, XPath: ""}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := current.Node
		if node.BackendNodeID > 0 {
			key := encode(frameID, node.BackendNodeID)
			out.TagNameMap[key] = strings.ToLower(node.NodeName)
			if current.XPath == "" {
				out.XPathMap[key] = "/"
			} else {
				out.XPathMap[key] = current.XPath
			}
			if node.IsScrollable {
				out.ScrollableMap[key] = true
			}
		}
		for i := len(node.ShadowRoots) - 1; i >= 0; i-- {
			stack = append(stack, entry{
				Node:  &node.ShadowRoots[i],
				XPath: joinXPath(current.XPath, "//"),
			})
		}
		if len(node.Children) > 0 {
			segs := buildChildXPathSegments(node.Children)
			for i := len(node.Children) - 1; i >= 0; i-- {
				stack = append(stack, entry{
					Node:  &node.Children[i],
					XPath: joinXPath(current.XPath, segs[i]),
				})
			}
		}
	}
	return out, nil
}

func getDOMTreeWithFallback(ctx context.Context, session Session, pierce bool) (*domNode, error) {
	_ = session.Send(ctx, "DOM.enable", nil, nil)
	depths := []int{-1, 256, 128, 64, 32, 16, 8, 4, 2, 1}
	var lastErr error
	for _, depth := range depths {
		var res domDocumentResult
		err := session.Send(ctx, "DOM.getDocument", map[string]any{
			"depth":  depth,
			"pierce": pierce,
		}, &res)
		if err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "CBOR: stack limit exceeded") {
				continue
			}
			return nil, err
		}
		if depth != -1 {
			if err := hydrateDOMTree(ctx, session, &res.Root, pierce); err != nil {
				return nil, err
			}
		}
		return &res.Root, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("DOM.getDocument failed")
	}
	return nil, lastErr
}

func hydrateDOMTree(ctx context.Context, session Session, root *domNode, pierce bool) error {
	type key struct {
		NodeID    int
		BackendID int
	}
	seen := make(map[key]struct{})
	stack := []*domNode{root}
	depths := []int{-1, 64, 32, 16, 8, 4, 2, 1}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		k := key{NodeID: node.NodeID, BackendID: node.BackendNodeID}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if shouldExpandDOMNode(node) && (node.NodeID > 0 || node.BackendNodeID > 0) {
			var err error
			for _, depth := range depths {
				var described struct {
					Node domNode `json:"node"`
				}
				params := map[string]any{
					"depth":  depth,
					"pierce": pierce,
				}
				if node.NodeID > 0 {
					params["nodeId"] = node.NodeID
				} else {
					params["backendNodeId"] = node.BackendNodeID
				}
				err = session.Send(ctx, "DOM.describeNode", params, &described)
				if err == nil {
					mergeDOMNodes(node, &described.Node)
					break
				}
				if !strings.Contains(err.Error(), "CBOR: stack limit exceeded") {
					return err
				}
			}
			if err != nil {
				return err
			}
		}
		for i := len(node.Children) - 1; i >= 0; i-- {
			stack = append(stack, &node.Children[i])
		}
		for i := len(node.ShadowRoots) - 1; i >= 0; i-- {
			stack = append(stack, &node.ShadowRoots[i])
		}
		if node.ContentDocument != nil {
			stack = append(stack, node.ContentDocument)
		}
	}
	return nil
}

func shouldExpandDOMNode(node *domNode) bool {
	return node.ChildNodeCount > len(node.Children)
}

func mergeDOMNodes(target, source *domNode) {
	if source.ChildNodeCount != 0 {
		target.ChildNodeCount = source.ChildNodeCount
	}
	if source.Children != nil {
		target.Children = source.Children
	}
	if source.ShadowRoots != nil {
		target.ShadowRoots = source.ShadowRoots
	}
	if source.ContentDocument != nil {
		target.ContentDocument = source.ContentDocument
	}
	if source.NodeID != 0 {
		target.NodeID = source.NodeID
	}
}
