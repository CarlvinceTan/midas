package snapshot

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type axis string

const (
	axisChild axis = "child"
	axisDesc  axis = "desc"
)

type xpathStep struct {
	Axis axis
	Raw  string
	Name string
}

type resolvedFocusFrame struct {
	TargetFrameID string
	TailXPath     string
	AbsPrefix     string
}

type resolvedCSSFocus struct {
	TargetFrameID string
	TailSelector  string
	AbsPrefix     string
}

var iframeStepRE = regexp.MustCompile(`^i?frame(?:\[\d+])?$`)

func parseXPathToSteps(path string) []xpathStep {
	s := strings.TrimSpace(path)
	i := 0
	steps := make([]xpathStep, 0)
	for i < len(s) {
		kind := axisChild
		if strings.HasPrefix(s[i:], "//") {
			kind = axisDesc
			i += 2
		} else if s[i] == '/' {
			i++
		}
		start := i
		for i < len(s) && s[i] != '/' {
			i++
		}
		raw := strings.TrimSpace(s[start:i])
		if raw == "" {
			continue
		}
		name := strings.ToLower(regexp.MustCompile(`\[\d+]\s*$`).ReplaceAllString(raw, ""))
		steps = append(steps, xpathStep{Axis: kind, Raw: raw, Name: name})
	}
	return steps
}

func buildXPathFromSteps(steps []xpathStep) string {
	if len(steps) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, step := range steps {
		if step.Axis == axisDesc {
			b.WriteString("//")
		} else {
			b.WriteString("/")
		}
		b.WriteString(step.Raw)
	}
	return b.String()
}

func resolveFocusFrameAndTail(ctx context.Context, page Page, absoluteXPath string, parentByFrame map[string]string, rootID string) (*resolvedFocusFrame, error) {
	steps := parseXPathToSteps(absoluteXPath)
	ctxFrameID := rootID
	buffer := make([]xpathStep, 0)
	absPrefix := ""

	flushIntoChild := func() error {
		if len(buffer) == 0 {
			return nil
		}
		selector := buildXPathFromSteps(buffer)
		parentSession := page.SessionForFrame(ctxFrameID)
		objectID, err := resolveObjectIDForSelector(ctx, parentSession, selector, ctxFrameID)
		if err != nil || objectID == "" {
			return fmt.Errorf("failed to resolve iframe element for %q", selector)
		}
		defer func() {
			_ = parentSession.Send(context.Background(), "Runtime.releaseObject", map[string]any{"objectId": objectID}, nil)
		}()
		var desc struct {
			Node struct {
				BackendNodeID int `json:"backendNodeId"`
			} `json:"node"`
		}
		if err := parentSession.Send(ctx, "DOM.describeNode", map[string]any{"objectId": objectID}, &desc); err != nil {
			return err
		}
		var childFrameID string
		for _, fid := range listChildrenOf(parentByFrame, ctxFrameID) {
			var owner struct {
				BackendNodeID int `json:"backendNodeId"`
			}
			if err := parentSession.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": fid}, &owner); err == nil && owner.BackendNodeID == desc.Node.BackendNodeID {
				childFrameID = fid
				break
			}
		}
		if childFrameID == "" {
			return fmt.Errorf("could not map iframe selector %q to child frame", selector)
		}
		absPrefix = prefixXPath(firstNonEmpty(absPrefix, "/"), selector)
		ctxFrameID = childFrameID
		buffer = buffer[:0]
		return nil
	}

	for _, step := range steps {
		buffer = append(buffer, step)
		if iframeStepRE.MatchString(step.Name) {
			if err := flushIntoChild(); err != nil {
				return nil, err
			}
		}
	}
	return &resolvedFocusFrame{
		TargetFrameID: ctxFrameID,
		TailXPath:     buildXPathFromSteps(buffer),
		AbsPrefix:     absPrefix,
	}, nil
}

func resolveCSSFocusFrameAndTail(ctx context.Context, page Page, rawSelector string, parentByFrame map[string]string, rootID string) (*resolvedCSSFocus, error) {
	parts := splitSelectorPath(rawSelector)
	ctxFrameID := rootID
	absPrefix := ""
	for i := 0; i < len(parts)-1; i++ {
		parentSession := page.SessionForFrame(ctxFrameID)
		objectID, err := resolveObjectIDForSelector(ctx, parentSession, parts[i], ctxFrameID)
		if err != nil || objectID == "" {
			return nil, fmt.Errorf("failed to resolve iframe selector %q", parts[i])
		}
		defer func(id string) {
			_ = parentSession.Send(context.Background(), "Runtime.releaseObject", map[string]any{"objectId": id}, nil)
		}(objectID)
		var desc struct {
			Node struct {
				BackendNodeID int `json:"backendNodeId"`
			} `json:"node"`
		}
		if err := parentSession.Send(ctx, "DOM.describeNode", map[string]any{"objectId": objectID}, &desc); err != nil {
			return nil, err
		}
		var childFrameID string
		for _, fid := range listChildrenOf(parentByFrame, ctxFrameID) {
			var owner struct {
				BackendNodeID int `json:"backendNodeId"`
			}
			if err := parentSession.Send(ctx, "DOM.getFrameOwner", map[string]any{"frameId": fid}, &owner); err == nil && owner.BackendNodeID == desc.Node.BackendNodeID {
				childFrameID = fid
				break
			}
		}
		if childFrameID == "" {
			return nil, fmt.Errorf("could not map iframe hop %q to child frame", parts[i])
		}
		ctxFrameID = childFrameID
	}
	tail := "*"
	if len(parts) > 0 {
		tail = parts[len(parts)-1]
	}
	return &resolvedCSSFocus{
		TargetFrameID: ctxFrameID,
		TailSelector:  tail,
		AbsPrefix:     absPrefix,
	}, nil
}

func listChildrenOf(parentByFrame map[string]string, parentID string) []string {
	out := make([]string, 0)
	for fid, parent := range parentByFrame {
		if parent == parentID {
			out = append(out, fid)
		}
	}
	return out
}
