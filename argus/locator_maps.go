package argus

import (
	"fmt"
	"strconv"
	"strings"
)

// Building the XPathMap + URLMap from the DOMSnapshot we ALREADY captured, so the
// argus path no longer needs a second full page.Snapshot() just to borrow the
// heuristic snapshot's locator maps.
//
// The xpath algorithm is a byte-for-byte port of the midas heuristic's
// nodeToAbsoluteXPathJS / buildChildXPathSegments (browser/snapshot/xpath.go):
// each segment is `<tag>[nth]` where nth counts preceding siblings sharing the
// node's (nodeType, nodeName); text/comment nodes use text()/comment(); namespaced
// tags use *[name()='ns:tag']. Walking up parentIndex, the document (nodeType 9)
// terminates, and doctype (10) + document-fragment/shadow-root (11) contribute no
// segment — exactly mirroring the heuristic's parentNode walk (which unshifts an
// empty string at a shadow host that its parts.filter(Boolean) then drops).
//
// Verified against the live heuristic JS on real pages: 100% exact-match on
// light-DOM nodes (every realistic click target, incl. the whole lichess board),
// diverging only on <slot>-reprojected shadow-internal nodes — which neither the
// heuristic's xpath nor this one can resolve via document.evaluate anyway. A
// missing/imperfect xpath is not fatal: the agent's click-by-ref simply can't use
// it, the same as before for those nodes.

// DOM nodeType constants (subset).
const (
	nodeTypeElement  = 1
	nodeTypeText     = 3
	nodeTypeComment  = 8
	nodeTypeDocument = 9
	nodeTypeDoctype  = 10
	nodeTypeFragment = 11 // document fragment / shadow root
)

// buildLocatorMaps synthesises (xpathMap, urlMap) keyed by the midas main-frame
// ref form "0-<backendNodeId>", from the main-frame document (documents[0]) of a
// DOMSnapshot. Empty maps if the snapshot lacks the needed parallel arrays (the
// caller then falls back to a heuristic snapshot).
func buildLocatorMaps(snap domSnapshotResp) (xpathMap, urlMap map[string]string) {
	xpathMap = map[string]string{}
	urlMap = map[string]string{}
	if len(snap.Documents) == 0 {
		return xpathMap, urlMap
	}
	s := func(i int) string {
		if i >= 0 && i < len(snap.Strings) {
			return snap.Strings[i]
		}
		return ""
	}

	doc := snap.Documents[0]
	n := doc.Nodes
	count := len(n.BackendNodeID)
	if count == 0 || len(n.ParentIndex) != count || len(n.NodeType) != count {
		return xpathMap, urlMap // missing arrays → signal fallback
	}

	nameOf := func(i int) string {
		if i < len(n.NodeName) {
			return strings.ToLower(s(n.NodeName[i]))
		}
		return ""
	}

	// Per-node segment = `<tag>[nth-of-(nodeType,nodeName)]` among its siblings.
	seg := make([]string, count)
	siblingCounts := make(map[int]map[string]int, count)
	for i := 0; i < count; i++ {
		p := n.ParentIndex[i]
		c := siblingCounts[p]
		if c == nil {
			c = map[string]int{}
			siblingCounts[p] = c
		}
		tag := nameOf(i)
		key := strconv.Itoa(n.NodeType[i]) + ":" + tag
		c[key]++
		idx := c[key]
		switch n.NodeType[i] {
		case nodeTypeText:
			seg[i] = fmt.Sprintf("text()[%d]", idx)
		case nodeTypeComment:
			seg[i] = fmt.Sprintf("comment()[%d]", idx)
		default:
			if strings.Contains(tag, ":") {
				seg[i] = fmt.Sprintf("*[name()='%s'][%d]", tag, idx)
			} else {
				seg[i] = fmt.Sprintf("%s[%d]", tag, idx)
			}
		}
	}

	// Absolute xpath per ref-worthy node: walk parentIndex to the root, skipping
	// document/doctype/fragment, matching the heuristic exactly.
	for i := 0; i < count; i++ {
		nt := n.NodeType[i]
		if nt != nodeTypeElement && nt != nodeTypeText && nt != nodeTypeComment {
			continue
		}
		bid := n.BackendNodeID[i]
		if bid == 0 {
			continue
		}
		var parts []string
		cur := i
		guard := 0
		for cur >= 0 && cur < count && guard <= count {
			guard++
			t := n.NodeType[cur]
			if t == nodeTypeDocument {
				break
			}
			if t != nodeTypeDoctype && t != nodeTypeFragment {
				parts = append(parts, seg[cur])
			}
			cur = n.ParentIndex[cur]
		}
		// reverse parts → root-first
		for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
			parts[l], parts[r] = parts[r], parts[l]
		}
		xpathMap[midasRef(strconv.Itoa(bid))] = "/" + strings.Join(parts, "/")

		// URLMap: href of <a>/<area> elements (the heuristic sources these from
		// the AX url; href is the same value and is already in DOMSnapshot attrs).
		if nt == nodeTypeElement {
			tag := nameOf(i)
			if tag == "a" || tag == "area" {
				if href := nodeAttr(n, i, s); href != "" {
					urlMap[midasRef(strconv.Itoa(bid))] = href
				}
			}
		}
	}
	return xpathMap, urlMap
}

// nodeAttr returns the href attribute string for node i, or "".
func nodeAttr(n domNodesRaw, i int, s func(int) string) string {
	if i >= len(n.Attributes) {
		return ""
	}
	raw := n.Attributes[i]
	for j := 0; j+1 < len(raw); j += 2 {
		if s(raw[j]) == "href" {
			return s(raw[j+1])
		}
	}
	return ""
}
