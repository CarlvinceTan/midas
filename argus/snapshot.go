package argus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/PolymuxOrg/midas/browser"
)

// computedStyleProps is the DOMSnapshot computedStyles request — the order is the
// contract (layout.styles rows are string indices in this order). cursor is
// near-ground-truth clickability; display/visibility let the renderer drop hidden
// overlays. Mirrors serve/snapshot_core.py COMPUTED_STYLES.
var computedStyleProps = []string{"cursor", "display", "visibility", "opacity"}

// Snapshot captures the page, classifies it via the argus server, and returns a
// *browser.SnapshotResult whose FormattedTree is the semantic graph rendering.
// XPathMap/URLMap/PerFrame are taken from the heuristic snapshot so click-by-ref
// (and self-heal) keep working — argus node refs are rendered as the midas
// main-frame form "0-<backendDOMNodeId>" to match those maps.
//
// Any error (server down, non-200, capture failure) is returned so the caller
// falls back to the heuristic snapshot — argus never hard-fails the agent.
func Snapshot(ctx context.Context, page *browser.Page, c *Client) (*browser.SnapshotResult, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("argus: not enabled")
	}

	viewW, viewH, scrollX, scrollY, docHeight, err := layoutMetrics(ctx, page)
	if err != nil {
		return nil, err
	}

	// JPEG (not PNG): the argus server's resize_and_tile only uses PIL's
	// decode-time draft downscale for JPEG inputs, so JPEG shrinks the wire
	// payload AND activates the server's fast decode path. ViT-S tile-pooled
	// features are insensitive to q85 artifacts.
	shot, err := page.Screenshot(ctx, browser.ScreenshotOptions{Format: "jpeg", Quality: 85})
	if err != nil {
		return nil, fmt.Errorf("argus: screenshot: %w", err)
	}
	shotB64 := base64.StdEncoding.EncodeToString(shot)

	axNodes, err := getFullAXTree(ctx, page)
	if err != nil {
		return nil, err
	}
	domInfo, domSnap, err := captureDOMSnapshot(ctx, page)
	if err != nil {
		return nil, err
	}
	nodes := buildNodesPayload(axNodes, domInfo, scrollX, scrollY)

	graph, err := c.parse(ctx, ParseRequest{
		PageID:           page.URL(),
		ScreenshotB64:    shotB64,
		ViewportWidth:    int(viewW),
		ViewportHeight:   int(viewH),
		ScreenshotWidth:  int(viewW),
		ScreenshotHeight: int(viewH),
		Nodes:            nodes,
	})
	if err != nil {
		return nil, err
	}

	byID := make(map[string]*NodeInput, len(nodes))
	for i := range nodes {
		byID[nodes[i].EncodedID] = &nodes[i]
	}
	tree := buildArgusText(graph, byID, viewW, viewH, scrollY, docHeight)

	// Build the click-by-ref locator maps (XPathMap/URLMap, keyed
	// "0-<backendNodeId>" to match our rendered refs) from the DOMSnapshot we
	// ALREADY captured — no second full page.Snapshot(). The xpath synthesis is a
	// verified byte-for-byte port of the heuristic's algorithm (locator_maps.go).
	// If synthesis comes up empty (snapshot lacked parentIndex/nodeType), fall
	// back to the heuristic snapshot so the maps are never silently lost.
	xpathMap, urlMap := buildLocatorMaps(domSnap)
	if len(xpathMap) == 0 {
		base, ferr := page.Snapshot(ctx)
		if ferr != nil {
			return nil, fmt.Errorf("argus: base snapshot for xpath map: %w", ferr)
		}
		base.FormattedTree = tree
		return base, nil
	}
	frameID := page.MainFrameID()
	return &browser.SnapshotResult{
		FormattedTree: tree,
		XPathMap:      xpathMap,
		URLMap:        urlMap,
		PerFrame: []browser.PerFrameSnapshot{{
			FrameID:       frameID,
			FormattedTree: tree,
			XPathMap:      xpathMap,
			URLMap:        urlMap,
		}},
	}, nil
}

// --- CDP capture --------------------------------------------------------------

type cdpViewport struct {
	ClientWidth  float64 `json:"clientWidth"`
	ClientHeight float64 `json:"clientHeight"`
	PageX        float64 `json:"pageX"`
	PageY        float64 `json:"pageY"`
}

type cdpContentSize struct {
	Height float64 `json:"height"`
}

type layoutMetricsResp struct {
	CSSLayoutViewport *cdpViewport    `json:"cssLayoutViewport"`
	LayoutViewport    *cdpViewport    `json:"layoutViewport"`
	CSSVisualViewport *cdpViewport    `json:"cssVisualViewport"`
	VisualViewport    *cdpViewport    `json:"visualViewport"`
	CSSContentSize    *cdpContentSize `json:"cssContentSize"`
	ContentSize       *cdpContentSize `json:"contentSize"`
}

// layoutMetrics mirrors MidasAdapter._layout_metrics: CSS-px viewport size,
// scroll offset (visual viewport), and document height.
func layoutMetrics(ctx context.Context, page *browser.Page) (viewW, viewH, scrollX, scrollY, docHeight float64, err error) {
	var m layoutMetricsResp
	if err = page.SendCDP(ctx, "Page.getLayoutMetrics", map[string]any{}, &m); err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("argus: getLayoutMetrics: %w", err)
	}
	layout := firstViewport(m.CSSLayoutViewport, m.LayoutViewport)
	visual := firstViewport(m.CSSVisualViewport, m.VisualViewport)
	content := m.CSSContentSize
	if content == nil {
		content = m.ContentSize
	}

	viewW, viewH = 1280, 720
	if layout != nil {
		if layout.ClientWidth > 0 {
			viewW = layout.ClientWidth
		}
		if layout.ClientHeight > 0 {
			viewH = layout.ClientHeight
		}
	}
	if visual != nil {
		scrollX, scrollY = visual.PageX, visual.PageY
	} else if layout != nil {
		scrollX, scrollY = layout.PageX, layout.PageY
	}
	docHeight = viewH
	if content != nil && content.Height > 0 {
		docHeight = content.Height
	}
	return viewW, viewH, scrollX, scrollY, docHeight, nil
}

func firstViewport(a, b *cdpViewport) *cdpViewport {
	if a != nil {
		return a
	}
	return b
}

type axValue struct {
	Value json.RawMessage `json:"value"`
}

type axNode struct {
	NodeID           string   `json:"nodeId"`
	BackendDOMNodeID int      `json:"backendDOMNodeId"`
	Role             axValue  `json:"role"`
	Name             axValue  `json:"name"`
	ParentID         string   `json:"parentId"`
	ChildIDs         []string `json:"childIds"`
}

func axStr(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func getFullAXTree(ctx context.Context, page *browser.Page) ([]axNode, error) {
	// Best-effort domain enables (a raw CDP bridge may need them).
	_ = page.SendCDP(ctx, "DOM.enable", map[string]any{}, nil)
	_ = page.SendCDP(ctx, "Accessibility.enable", map[string]any{}, nil)
	var out struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := page.SendCDP(ctx, "Accessibility.getFullAXTree", map[string]any{"fetchRelativeNodes": false}, &out); err != nil {
		return nil, fmt.Errorf("argus: getFullAXTree: %w", err)
	}
	return out.Nodes, nil
}

type domInfo struct {
	Tag        string
	Attrs      map[string]string
	BBox       *BBox
	Value      string
	Cursor     string
	Display    string
	Visibility string
	Opacity    string
}

type domRareString struct {
	Index []int `json:"index"`
	Value []int `json:"value"`
}

type domNodesRaw struct {
	BackendNodeID []int         `json:"backendNodeId"`
	ParentIndex   []int         `json:"parentIndex"`
	NodeType      []int         `json:"nodeType"`
	NodeName      []int         `json:"nodeName"`
	Attributes    [][]int       `json:"attributes"`
	InputValue    domRareString `json:"inputValue"`
}

type domLayoutRaw struct {
	NodeIndex []int       `json:"nodeIndex"`
	Bounds    [][]float64 `json:"bounds"`
	Styles    [][]int     `json:"styles"`
}

type domDocRaw struct {
	Nodes  domNodesRaw  `json:"nodes"`
	Layout domLayoutRaw `json:"layout"`
}

type domSnapshotResp struct {
	Strings   []string    `json:"strings"`
	Documents []domDocRaw `json:"documents"`
}

// captureDOMSnapshot runs DOMSnapshot.captureSnapshot and parses it, returning
// both the per-node info map (for the prompt) and the raw response (so the
// locator maps can be synthesised from it instead of a second page.Snapshot()).
func captureDOMSnapshot(ctx context.Context, page *browser.Page) (map[int]*domInfo, domSnapshotResp, error) {
	var snap domSnapshotResp
	if err := page.SendCDP(ctx, "DOMSnapshot.captureSnapshot", map[string]any{"computedStyles": computedStyleProps}, &snap); err != nil {
		return nil, domSnapshotResp{}, fmt.Errorf("argus: captureSnapshot: %w", err)
	}
	return parseDOMSnapshot(snap), snap, nil
}

// parseDOMSnapshot is the Go port of snapshot_core.parse_dom_snapshot:
// one DOMSnapshot.captureSnapshot result → backendNodeId → {tag, attrs, bbox, value}.
func parseDOMSnapshot(snap domSnapshotResp) map[int]*domInfo {
	s := func(i int) string {
		if i >= 0 && i < len(snap.Strings) {
			return snap.Strings[i]
		}
		return ""
	}
	index := map[int]*domInfo{}
	for _, doc := range snap.Documents {
		n := doc.Nodes
		for i, bid := range n.BackendNodeID {
			var attrsRaw []int
			if i < len(n.Attributes) {
				attrsRaw = n.Attributes[i]
			}
			attrs := map[string]string{}
			for j := 0; j+1 < len(attrsRaw); j += 2 {
				attrs[s(attrsRaw[j])] = s(attrsRaw[j+1])
			}
			tag := "div"
			if i < len(n.NodeName) {
				tag = strings.ToLower(s(n.NodeName[i]))
			}
			index[bid] = &domInfo{Tag: tag, Attrs: attrs}
		}
		for k, ni := range n.InputValue.Index {
			if ni < len(n.BackendNodeID) && k < len(n.InputValue.Value) {
				if di := index[n.BackendNodeID[ni]]; di != nil {
					di.Value = s(n.InputValue.Value[k])
				}
			}
		}
		for li, ni := range doc.Layout.NodeIndex {
			if ni >= len(n.BackendNodeID) {
				continue
			}
			di := index[n.BackendNodeID[ni]]
			if di == nil {
				continue
			}
			if li < len(doc.Layout.Bounds) {
				b := doc.Layout.Bounds[li]
				if len(b) == 4 && b[2] > 0 && b[3] > 0 {
					di.BBox = &BBox{X: b[0], Y: b[1], W: b[2], H: b[3]}
				}
			}
			if li < len(doc.Layout.Styles) {
				row := doc.Layout.Styles[li]
				for k, name := range computedStyleProps {
					if k >= len(row) {
						break
					}
					v := s(row[k])
					if v == "" {
						continue
					}
					switch name {
					case "cursor":
						di.Cursor = v
					case "display":
						di.Display = v
					case "visibility":
						di.Visibility = v
					case "opacity":
						di.Opacity = v
					}
				}
			}
		}
	}
	return index
}

// buildNodesPayload is the Go port of snapshot_core.build_nodes_payload:
// AX tree + DOM index → ParseRequest nodes, with the document→viewport bbox
// translation by (-scrollX, -scrollY).
func buildNodesPayload(axNodes []axNode, dom map[int]*domInfo, scrollX, scrollY float64) []NodeInput {
	axToBackend := map[string]string{}
	for _, n := range axNodes {
		if n.BackendDOMNodeID != 0 {
			axToBackend[n.NodeID] = strconv.Itoa(n.BackendDOMNodeID)
		}
	}
	out := make([]NodeInput, 0, len(axNodes))
	for _, ax := range axNodes {
		if ax.BackendDOMNodeID == 0 {
			continue
		}
		info := dom[ax.BackendDOMNodeID]
		var bbox *BBox
		tag := "div"
		attrs := map[string]string{}
		value, cursor, display, visibility, opacity := "", "", "", "", ""
		if info != nil {
			tag = info.Tag
			if info.Attrs != nil {
				attrs = info.Attrs
			}
			value = info.Value
			cursor, display, visibility, opacity = info.Cursor, info.Display, info.Visibility, info.Opacity
			if info.BBox != nil {
				bbox = &BBox{
					X: info.BBox.X - scrollX,
					Y: info.BBox.Y - scrollY,
					W: info.BBox.W,
					H: info.BBox.H,
				}
			}
		}
		var parent *string
		if p, ok := axToBackend[ax.ParentID]; ok {
			parent = &p
		}
		out = append(out, NodeInput{
			EncodedID:  strconv.Itoa(ax.BackendDOMNodeID),
			Tag:        tag,
			Text:       axStr(ax.Name.Value),
			Attrs:      attrs,
			AriaRole:   axStr(ax.Role.Value),
			ParentID:   parent,
			ChildIDs:   ax.ChildIDs,
			BBox:       bbox,
			InputValue: value,
			Cursor:     cursor,
			Display:    display,
			Visibility: visibility,
			Opacity:    opacity,
		})
	}
	return out
}

// --- prompt rendering (port of snapshot_core.build_argus_text) ----------------

var interactiveTypes = map[string]bool{
	"button": true, "link": true, "input_text": true, "input_password": true,
	"input_checkbox": true, "input_radio": true, "select_dropdown": true,
	"textarea": true, "tab": true,
}

var contextTypes = map[string]bool{
	"heading": true, "modal": true, "tooltip": true, "breadcrumb": true, "nav": true,
}

const (
	collapseThreshold = 8
	collapseKeep      = 5
)

func confOr(c *float64) float64 {
	if c != nil {
		return *c
	}
	return 1.0
}

// midasRef bridges an argus bare backendDOMNodeId to the midas main-frame ref
// form ("0-<id>") so the rendered prompt's refs match midas's XPathMap keys and
// click-by-ref resolution.
func midasRef(id string) string { return "0-" + id }

// --- affordance surfacing (port of serve/snapshot_core.py helpers) ------------
//
// Uses the model's OWN interact-head prediction only — deliberately NOT the CSS
// cursor. cursor is the answer to the perception task the interact head exists to
// solve; surfacing it would mask whether argus is actually good. cursor stays a
// training signal, not a runtime crutch.

func promptInteractThreshold() float64 {
	if v := os.Getenv("ARGUS_PROMPT_INTERACT_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0.6
}

// affordanceKind = the model's predicted affordance above the prompt threshold.
func affordanceKind(n UINode) string {
	if (n.Interact == "clickable" || n.Interact == "draggable") && n.InteractScore >= promptInteractThreshold() {
		return n.Interact
	}
	return ""
}

// interactableSurface admits an affordance the model's interact head predicts but
// the type/role filter would drop. ARGUS_PROMPT_INTERACT=0 disables it.
func interactableSurface(n UINode) bool {
	if os.Getenv("ARGUS_PROMPT_INTERACT") == "0" {
		return false
	}
	return affordanceKind(n) != ""
}

func interactSuffix(n UINode) string {
	switch affordanceKind(n) {
	case "draggable":
		return " draggable"
	case "clickable":
		if !interactiveTypes[n.Type] {
			return " clickable"
		}
	}
	return ""
}

// isHidden drops visibility:hidden / display:none nodes (they keep a layout box):
// a real STATE fact (not on screen), not a perception task. opacity:0 is excluded
// (transparent-but-clickable overlays).
func isHidden(visibility, display string) bool {
	return visibility == "hidden" || display == "none"
}

func buildArgusText(graph *UIGraph, payloadByID map[string]*NodeInput, viewW, viewH, scrollY, docHeight float64) string {
	childToParent := map[string]string{}
	for _, edge := range graph.Structure {
		for _, child := range edge.Children {
			childToParent[child] = edge.Parent
		}
	}

	idToDepth := map[string]int{}
	var nodeDepth func(nid string, seen map[string]bool) int
	nodeDepth = func(nid string, seen map[string]bool) int {
		if d, ok := idToDepth[nid]; ok {
			return d
		}
		parent, ok := childToParent[nid]
		if !ok || parent == "" || seen[parent] {
			idToDepth[nid] = 0
			return 0
		}
		seen[nid] = true
		d := nodeDepth(parent, seen) + 1
		idToDepth[nid] = d
		return d
	}

	tags := map[string]bool{}
	for _, n := range payloadByID {
		tags[n.Tag] = true
	}

	// Pass 1: filter + classify against the viewport.
	var visible []UINode
	nAbove, nBelow := 0, 0
	for _, n := range graph.Nodes {
		conf := confOr(n.Confidence)
		if conf < 0.5 && (n.Role == "structural" || n.Role == "decoration") {
			continue
		}
		p := payloadByID[n.ID]
		if p != nil && isHidden(p.Visibility, p.Display) {
			continue
		}
		hasText := n.Label != "" && !tags[strings.ToLower(n.Label)]
		switch {
		case interactiveTypes[n.Type]:
		case contextTypes[n.Type] && hasText:
		case hasText && (n.Role == "content" || n.Role == "search" || n.Role == "navigation" ||
			n.Role == "primary_action" || n.Role == "secondary_action"):
		case interactableSurface(n):
			// Affordance the model's interact head recovered but type/role didn't
			// surface — DOM-invisible clickable/draggable nodes.
		default:
			continue
		}

		var bbox *BBox
		if p != nil {
			bbox = p.BBox
		}
		if bbox == nil {
			continue
		}
		bx, by, bw, bh := bbox.X, bbox.Y, bbox.W, bbox.H
		if by+bh <= 0 {
			nAbove++
			continue
		}
		if by >= viewH {
			nBelow++
			continue
		}
		if bx+bw <= 0 || bx >= viewW {
			continue
		}
		visible = append(visible, n)
	}

	// Pass 2: render with same-shaped-sibling collapse.
	groupKey := func(n UINode) string {
		return childToParent[n.ID] + "\x00" + n.Type + "\x00" + n.Role
	}
	groupSizes := map[string]int{}
	for _, n := range visible {
		groupSizes[groupKey(n)]++
	}

	var lines []string
	emitted := map[string]int{}
	for _, n := range visible {
		key := groupKey(n)
		count := emitted[key]
		emitted[key] = count + 1
		depth := nodeDepth(n.ID, map[string]bool{})
		if depth > 6 {
			depth = 6
		}
		indent := strings.Repeat("  ", depth)

		if groupSizes[key] > collapseThreshold && count >= collapseKeep {
			if count == collapseKeep {
				lines = append(lines, fmt.Sprintf("%s… and %d more %s elements (same group)",
					indent, groupSizes[key]-collapseKeep, n.Type))
			}
			continue
		}

		line := fmt.Sprintf("%s[%s] type:%s role:%s%s", indent, midasRef(n.ID), n.Type, n.Role, interactSuffix(n))
		if n.Label != "" {
			line += fmt.Sprintf(" %q", truncate(n.Label, 80))
		}
		if p := payloadByID[n.ID]; p != nil && p.InputValue != "" {
			line += fmt.Sprintf(" value=%q", truncate(p.InputValue, 40))
		}
		lines = append(lines, line)
	}

	var header []string
	if scrollY > 0 {
		header = append(header, fmt.Sprintf("(viewport at y=%dpx of %dpx)", int(scrollY), int(docHeight)))
	}
	if nAbove > 0 {
		header = append(header, fmt.Sprintf("⬆ %d more elements above — scroll up to reveal", nAbove))
	}
	var footer []string
	if nBelow > 0 {
		footer = append(footer, fmt.Sprintf("⬇ %d more elements below — scroll down to reveal", nBelow))
	}

	all := append(append(header, lines...), footer...)
	text := strings.Join(all, "\n")
	if text == "" {
		return "(empty semantic graph)"
	}
	return text
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
