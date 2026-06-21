package argus

import "testing"

// Build a domSnapshotResp from a compact node spec for testing.
// Each node: (backendId, parentIndex, nodeType, nodeName, attrs...).
func TestBuildLocatorMaps_XPath(t *testing.T) {
	// strings table
	str := []string{"", "html", "body", "div", "button", "piece", "a", "href", "/x", "span", "svg:use", "#text"}
	si := map[string]int{}
	for i, s := range str {
		si[s] = i
	}
	// tree:
	// 0 document(9)
	// 1 html(1) parent 0
	// 2 body(1) parent 1
	// 3 div(1) parent 2          -> /html[1]/body[1]/div[1]
	// 4 div(1) parent 2          -> /html[1]/body[1]/div[2]
	// 5 button(1) parent 4       -> /html[1]/body[1]/div[2]/button[1]
	// 6 piece(1) parent 4        -> /html[1]/body[1]/div[2]/piece[1]
	// 7 piece(1) parent 4        -> /html[1]/body[1]/div[2]/piece[2]
	// 8 a(1) parent 3 href=/x    -> /html[1]/body[1]/div[1]/a[1]   (urlMap=/x)
	// 9 text(3) parent 5         -> /html[1]/body[1]/div[2]/button[1]/text()[1]
	backend := []int{0, 100, 101, 102, 103, 104, 105, 106, 107, 108}
	parent := []int{-1, 0, 1, 2, 2, 4, 4, 4, 3, 5}
	ntype := []int{9, 1, 1, 1, 1, 1, 1, 1, 1, 3}
	nname := []int{0, si["html"], si["body"], si["div"], si["div"], si["button"], si["piece"], si["piece"], si["a"], si["#text"]}
	attrs := make([][]int, 10)
	attrs[8] = []int{si["href"], si["/x"]} // a href=/x

	snap := domSnapshotResp{
		Strings: str,
		Documents: []domDocRaw{{
			Nodes: domNodesRaw{
				BackendNodeID: backend,
				ParentIndex:   parent,
				NodeType:      ntype,
				NodeName:      nname,
				Attributes:    attrs,
			},
		}},
	}

	xp, url := buildLocatorMaps(snap)

	want := map[string]string{
		"0-102": "/html[1]/body[1]/div[1]",
		"0-103": "/html[1]/body[1]/div[2]",
		"0-104": "/html[1]/body[1]/div[2]/button[1]",
		"0-105": "/html[1]/body[1]/div[2]/piece[1]",
		"0-106": "/html[1]/body[1]/div[2]/piece[2]",
		"0-107": "/html[1]/body[1]/div[1]/a[1]",
		"0-108": "/html[1]/body[1]/div[2]/button[1]/text()[1]",
	}
	for ref, exp := range want {
		if got := xp[ref]; got != exp {
			t.Errorf("xpath[%s] = %q, want %q", ref, got, exp)
		}
	}
	// document/doctype excluded; html/body present.
	if _, ok := xp["0-0"]; ok {
		t.Error("document node should not get an xpath")
	}
	if url["0-107"] != "/x" {
		t.Errorf("urlMap[0-107] = %q, want /x", url["0-107"])
	}
	if len(url) != 1 {
		t.Errorf("urlMap should have exactly the one <a href>, got %d", len(url))
	}
}

// A shadow-root / document-fragment node (nodeType 11) contributes no segment —
// its children's xpath continues from the host, matching the heuristic.
func TestBuildLocatorMaps_ShadowFragmentSkipped(t *testing.T) {
	str := []string{"", "html", "body", "x-widget", "#document-fragment", "button"}
	// 0 doc(9); 1 html(1)<0; 2 body(1)<1; 3 x-widget(1)<2; 4 fragment(11)<3; 5 button(1)<4
	snap := domSnapshotResp{
		Strings: str,
		Documents: []domDocRaw{{Nodes: domNodesRaw{
			BackendNodeID: []int{0, 1, 2, 3, 4, 200},
			ParentIndex:   []int{-1, 0, 1, 2, 3, 4},
			NodeType:      []int{9, 1, 1, 1, 11, 1},
			NodeName:      []int{0, 1, 2, 3, 4, 5},
		}}},
	}
	xp, _ := buildLocatorMaps(snap)
	// button's xpath skips the fragment: /html[1]/body[1]/x-widget[1]/button[1]
	if got, want := xp["0-200"], "/html[1]/body[1]/x-widget[1]/button[1]"; got != want {
		t.Errorf("shadow-child xpath = %q, want %q", got, want)
	}
}

// Missing parallel arrays → empty maps (caller falls back to a heuristic snapshot).
func TestBuildLocatorMaps_FallbackOnMissingArrays(t *testing.T) {
	snap := domSnapshotResp{
		Strings: []string{"", "div"},
		Documents: []domDocRaw{{Nodes: domNodesRaw{
			BackendNodeID: []int{1, 2, 3},
			// ParentIndex / NodeType absent
			NodeName: []int{1, 1, 1},
		}}},
	}
	xp, url := buildLocatorMaps(snap)
	if len(xp) != 0 || len(url) != 0 {
		t.Errorf("expected empty maps on missing arrays, got xp=%d url=%d", len(xp), len(url))
	}
}
