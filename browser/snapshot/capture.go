package snapshot

import (
	"context"
	"strconv"
	"strings"
)

type iframeChainStep struct {
	ParentSession       Session
	IFrameBackendNodeID int
}

func Capture(ctx context.Context, page Page, opts Options) (*Result, error) {
	opts = normalizeSnapshotOptions(opts)
	context := buildFrameContext(page)
	if scoped, err := tryScopedSnapshot(ctx, page, opts, context); err == nil && scoped != nil {
		return scoped, nil
	}

	frameIDs := []string{context.RootID}
	if opts.includeIframes() {
		frameIDs = append(frameIDs[:0], context.Frames...)
		if len(frameIDs) == 0 {
			frameIDs = []string{context.RootID}
		}
	}

	sessionIndexes, err := buildSessionIndexes(ctx, page, frameIDs, opts.pierceShadow())
	if err != nil {
		return nil, err
	}
	perFrameMaps, perFrameOutlines, err := collectPerFrameMaps(ctx, page, context, sessionIndexes, opts, frameIDs)
	if err != nil {
		return nil, err
	}
	absPrefix, iframeHostByChild := computeFramePrefixes(ctx, page, context, perFrameMaps, frameIDs)
	return mergeFramesIntoSnapshot(context, perFrameMaps, perFrameOutlines, absPrefix, iframeHostByChild, frameIDs), nil
}

func tryScopedSnapshot(ctx context.Context, page Page, opts Options, context frameContext) (*Result, error) {
	focus := strings.TrimSpace(opts.FocusSelector)
	if focus == "" {
		return nil, nil
	}

	targetFrameID := context.RootID
	tailSelector := ""
	absPrefix := ""
	var err error
	if strings.HasPrefix(focus, "/") || strings.HasPrefix(strings.ToLower(focus), "xpath=") {
		var resolved *resolvedFocusFrame
		resolved, err = resolveFocusFrameAndTail(ctx, page, normalizeXPath(focus), context.ParentByFrame, context.RootID)
		if err != nil {
			return nil, nil
		}
		targetFrameID = resolved.TargetFrameID
		tailSelector = resolved.TailXPath
		absPrefix = resolved.AbsPrefix
	} else {
		var resolved *resolvedCSSFocus
		resolved, err = resolveCSSFocusFrameAndTail(ctx, page, focus, context.ParentByFrame, context.RootID)
		if err != nil {
			return nil, nil
		}
		targetFrameID = resolved.TargetFrameID
		tailSelector = resolved.TailSelector
		absPrefix = resolved.AbsPrefix
	}

	session := page.SessionForFrame(targetFrameID)
	parentID := context.ParentByFrame[targetFrameID]
	sameSessionAsParent := parentID != "" && page.SessionForFrame(parentID) == session
	domMaps, err := domMapsForSession(ctx, session, targetFrameID, opts.pierceShadow(), func(fid string, backendNodeID int) string {
		return encodedNodeID(page, fid, backendNodeID)
	}, sameSessionAsParent)
	if err != nil {
		return nil, nil
	}
	a11y, err := a11yForFrame(ctx, session, targetFrameID, a11yOptions{
		FocusSelector: tailSelector,
		Experimental:  opts.Experimental,
		TagNameMap:    domMaps.TagNameMap,
		ScrollableMap: domMaps.ScrollableMap,
		Encode: func(backendNodeID int) string {
			return encodedNodeID(page, targetFrameID, backendNodeID)
		},
	})
	if err != nil || !a11y.ScopeApplied {
		return nil, nil
	}

	scopedXPathMap := make(map[string]string, len(domMaps.XPathMap))
	if absPrefix == "" || absPrefix == "/" {
		for key, value := range domMaps.XPathMap {
			scopedXPathMap[key] = value
		}
	} else {
		for key, value := range domMaps.XPathMap {
			scopedXPathMap[key] = prefixXPath(absPrefix, value)
		}
	}
	return &Result{
		FormattedTree: a11y.Outline,
		XPathMap:      scopedXPathMap,
		URLMap:        cloneStringMap(a11y.URLMap),
		PerFrame: []PerFrame{{
			FrameID:       targetFrameID,
			FormattedTree: a11y.Outline,
			XPathMap:      cloneStringMap(domMaps.XPathMap),
			URLMap:        cloneStringMap(a11y.URLMap),
		}},
	}, nil
}

func collectPerFrameMaps(ctx context.Context, page Page, context frameContext, sessionIndexes map[string]*sessionDOMIndex, opts Options, frameIDs []string) (map[string]*frameDOMMaps, []PerFrame, error) {
	perFrameMaps := make(map[string]*frameDOMMaps)
	perFrame := make([]PerFrame, 0, len(frameIDs))
	for _, frameID := range frameIDs {
		session := page.SessionForFrame(frameID)
		key := session.ID()
		if key == "" {
			key = "root"
		}
		idx := sessionIndexes[key]
		if idx == nil {
			var err error
			idx, err = buildSessionDOMIndex(ctx, session, opts.pierceShadow())
			if err != nil {
				return nil, nil, err
			}
			sessionIndexes[key] = idx
		}
		parentID := context.ParentByFrame[frameID]
		sameSessionAsParent := parentID != "" && page.SessionForFrame(parentID) == session
		docRootBE := idx.RootBackend
		if sameSessionAsParent {
			var owner struct {
				BackendNodeID int `json:"backendNodeId"`
			}
			if err := session.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": frameID}, &owner); err == nil && owner.BackendNodeID > 0 {
				if cdBE := idx.ContentDocRootByIFrame[owner.BackendNodeID]; cdBE > 0 {
					docRootBE = cdBE
				}
			}
		}
		maps := &frameDOMMaps{
			TagNameMap:    make(map[string]string),
			XPathMap:      make(map[string]string),
			ScrollableMap: make(map[string]bool),
			URLMap:        make(map[string]string),
		}
		baseAbs := idx.AbsByBackend[docRootBE]
		for be, nodeAbs := range idx.AbsByBackend {
			if idx.DocRootOf[be] != docRootBE {
				continue
			}
			key := encodedNodeID(page, frameID, be)
			maps.XPathMap[key] = relativizeXPath(baseAbs, nodeAbs)
			if tag := idx.TagByBackend[be]; tag != "" {
				maps.TagNameMap[key] = tag
			}
			if idx.ScrollableByBackend[be] {
				maps.ScrollableMap[key] = true
			}
		}

		a11y, err := a11yForFrame(ctx, session, frameID, a11yOptions{
			Experimental:  opts.Experimental,
			TagNameMap:    maps.TagNameMap,
			ScrollableMap: maps.ScrollableMap,
			Encode: func(backendNodeID int) string {
				return encodedNodeID(page, frameID, backendNodeID)
			},
		})
		if err != nil {
			return nil, nil, err
		}
		for key, value := range a11y.URLMap {
			maps.URLMap[key] = value
		}
		perFrameMaps[frameID] = maps
		perFrame = append(perFrame, PerFrame{
			FrameID:       frameID,
			FormattedTree: a11y.Outline,
			XPathMap:      cloneStringMap(maps.XPathMap),
			URLMap:        cloneStringMap(maps.URLMap),
		})
	}
	return perFrameMaps, perFrame, nil
}

func computeFramePrefixes(ctx context.Context, page Page, context frameContext, perFrameMaps map[string]*frameDOMMaps, frameIDs []string) (map[string]string, map[string]string) {
	absPrefix := map[string]string{context.RootID: ""}
	iframeHostByChild := make(map[string]string)
	included := make(map[string]bool, len(frameIDs))
	for _, frameID := range frameIDs {
		included[frameID] = true
	}
	queue := []string{context.RootID}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		parentAbs := absPrefix[parent]
		for _, child := range context.Frames {
			if !included[child] || context.ParentByFrame[child] != parent {
				continue
			}
			queue = append(queue, child)
			parentSess := page.SessionForFrame(parent)
			if parent := context.ParentByFrame[child]; parent == "" {
				parentSess = page.SessionForFrame(child)
			}
			var owner struct {
				BackendNodeID int `json:"backendNodeId"`
			}
			iframeXPath := page.OwnerXPath(child)
			if err := parentSess.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": child}, &owner); err == nil && owner.BackendNodeID > 0 {
				iframeEnc := encodedNodeID(page, parent, owner.BackendNodeID)
				iframeXPath = ""
				if parentMaps := perFrameMaps[parent]; parentMaps != nil {
					iframeXPath = parentMaps.XPathMap[iframeEnc]
				}
				if iframeXPath != "" {
					iframeHostByChild[child] = iframeEnc
				}
			}
			childAbs := parentAbs
			if iframeXPath != "" {
				childAbs = prefixXPath(firstNonEmpty(parentAbs, "/"), iframeXPath)
			}
			absPrefix[child] = childAbs
		}
	}
	return absPrefix, iframeHostByChild
}

func mergeFramesIntoSnapshot(context frameContext, perFrameMaps map[string]*frameDOMMaps, perFrame []PerFrame, absPrefix map[string]string, iframeHostByChild map[string]string, frameIDs []string) *Result {
	combinedXPathMap := make(map[string]string)
	combinedURLMap := make(map[string]string)
	for _, frameID := range frameIDs {
		maps := perFrameMaps[frameID]
		if maps == nil {
			continue
		}
		abs := absPrefix[frameID]
		isRoot := abs == "" || abs == "/"
		for key, value := range maps.XPathMap {
			if isRoot {
				combinedXPathMap[key] = value
			} else {
				combinedXPathMap[key] = prefixXPath(abs, value)
			}
		}
		for key, value := range maps.URLMap {
			combinedURLMap[key] = value
		}
	}
	idToTree := make(map[string]string)
	rootOutline := ""
	for _, frame := range perFrame {
		if frame.FrameID == context.RootID && rootOutline == "" {
			rootOutline = frame.FormattedTree
		}
		if parentEnc := iframeHostByChild[frame.FrameID]; parentEnc != "" {
			idToTree[parentEnc] = frame.FormattedTree
		}
	}
	if rootOutline == "" && len(perFrame) > 0 {
		rootOutline = perFrame[0].FormattedTree
	}
	combinedTree := injectSubtrees(rootOutline, idToTree)
	for _, frame := range perFrame {
		if frame.FrameID == context.RootID {
			continue
		}
		host := iframeHostByChild[frame.FrameID]
		if host != "" && strings.Contains(combinedTree, "["+host+"]") {
			continue
		}
		if strings.TrimSpace(frame.FormattedTree) == "" {
			continue
		}
		if strings.TrimSpace(combinedTree) == "" {
			combinedTree = frame.FormattedTree
			continue
		}
		combinedTree += "\n" + frame.FormattedTree
	}
	return &Result{
		FormattedTree: combinedTree,
		XPathMap:      combinedXPathMap,
		URLMap:        combinedURLMap,
		PerFrame:      perFrame,
	}
}

func encodedNodeID(page Page, frameID string, backendNodeID int) string {
	return strconv.Itoa(page.Ordinal(frameID)) + "-" + strconv.Itoa(backendNodeID)
}

func normalizeSnapshotOptions(opts Options) Options {
	return opts
}

func (o Options) pierceShadow() bool {
	if o.PierceShadow == nil {
		return true
	}
	return *o.PierceShadow
}

func (o Options) includeIframes() bool {
	if o.IncludeIframes == nil {
		return true
	}
	return *o.IncludeIframes
}
