package argus

import (
	"strings"
	"testing"
)

func fptr(v float64) *float64 { return &v }

// TestBuildArgusText covers the port of snapshot_core.build_argus_text: node
// filtering, the "0-" midas ref prefix, value rendering, viewport scroll
// markers, and same-shaped-sibling collapse. Pure logic — no server needed.
func TestBuildArgusText(t *testing.T) {
	t.Run("filter_and_ref_prefix", func(t *testing.T) {
		graph := &UIGraph{
			Nodes: []UINode{
				{ID: "1", Type: "button", Role: "primary_action", Label: "Submit", Confidence: fptr(0.99)},
				// low-confidence structural → dropped
				{ID: "2", Type: "container", Role: "structural", Label: "wrap", Confidence: fptr(0.2)},
				// no text + not interactive → dropped
				{ID: "3", Type: "container", Role: "content", Label: "", Confidence: fptr(0.9)},
			},
			Structure: []UIEdge{{Parent: "root", Children: []string{"1", "2", "3"}}},
		}
		payload := map[string]*NodeInput{
			"1": {EncodedID: "1", Tag: "button", BBox: &BBox{X: 10, Y: 10, W: 100, H: 30}},
			"2": {EncodedID: "2", Tag: "div", BBox: &BBox{X: 0, Y: 0, W: 50, H: 50}},
			"3": {EncodedID: "3", Tag: "div", BBox: &BBox{X: 0, Y: 0, W: 50, H: 50}},
		}
		text := buildArgusText(graph, payload, 1280, 720, 0, 1000)
		if !strings.Contains(text, `[0-1] type:button role:primary_action "Submit"`) {
			t.Errorf("missing/incorrect button line:\n%s", text)
		}
		if strings.Contains(text, "0-2") || strings.Contains(text, "0-3") {
			t.Errorf("dropped nodes leaked into output:\n%s", text)
		}
	})

	t.Run("input_value_and_truncation", func(t *testing.T) {
		longLabel := strings.Repeat("x", 200)
		graph := &UIGraph{Nodes: []UINode{
			{ID: "5", Type: "input_text", Role: "content", Label: longLabel, Confidence: fptr(1)},
		}}
		payload := map[string]*NodeInput{
			"5": {EncodedID: "5", Tag: "input", BBox: &BBox{X: 5, Y: 5, W: 200, H: 24}, InputValue: "hello"},
		}
		text := buildArgusText(graph, payload, 1280, 720, 0, 1000)
		if !strings.Contains(text, `value="hello"`) {
			t.Errorf("input value not rendered:\n%s", text)
		}
		if strings.Contains(text, strings.Repeat("x", 81)) {
			t.Errorf("label not truncated to 80 chars:\n%s", text)
		}
	})

	t.Run("scroll_markers", func(t *testing.T) {
		graph := &UIGraph{Nodes: []UINode{
			{ID: "above", Type: "button", Role: "content", Label: "Up", Confidence: fptr(1)},
			{ID: "below", Type: "button", Role: "content", Label: "Down", Confidence: fptr(1)},
			{ID: "here", Type: "button", Role: "content", Label: "Here", Confidence: fptr(1)},
		}}
		payload := map[string]*NodeInput{
			"above": {EncodedID: "above", Tag: "button", BBox: &BBox{X: 10, Y: -200, W: 50, H: 20}}, // y+h<=0
			"below": {EncodedID: "below", Tag: "button", BBox: &BBox{X: 10, Y: 9000, W: 50, H: 20}}, // y>=viewH
			"here":  {EncodedID: "here", Tag: "button", BBox: &BBox{X: 10, Y: 100, W: 50, H: 20}},
		}
		text := buildArgusText(graph, payload, 1280, 720, 300, 10000)
		if !strings.Contains(text, "⬆ 1 more elements above") {
			t.Errorf("missing above marker:\n%s", text)
		}
		if !strings.Contains(text, "⬇ 1 more elements below") {
			t.Errorf("missing below marker:\n%s", text)
		}
		if !strings.Contains(text, "viewport at y=300px of 10000px") {
			t.Errorf("missing scroll header:\n%s", text)
		}
		if !strings.Contains(text, "[0-here]") {
			t.Errorf("in-viewport node missing:\n%s", text)
		}
	})

	t.Run("sibling_collapse", func(t *testing.T) {
		var nodes []UINode
		payload := map[string]*NodeInput{}
		for i := 0; i < 10; i++ {
			id := string(rune('a' + i))
			nodes = append(nodes, UINode{ID: id, Type: "link", Role: "navigation", Label: "L" + id, Confidence: fptr(1)})
			payload[id] = &NodeInput{EncodedID: id, Tag: "a", BBox: &BBox{X: 10, Y: float64(10 + i*22), W: 50, H: 20}}
		}
		graph := &UIGraph{
			Nodes:     nodes,
			Structure: []UIEdge{{Parent: "ul", Children: idsOf(nodes)}},
		}
		text := buildArgusText(graph, payload, 1280, 720, 0, 1000)
		// 10 same-shaped siblings > threshold(8): keep first 5 + one summary line.
		emitted := strings.Count(text, "] type:link")
		if emitted != collapseKeep {
			t.Errorf("expected %d rendered + summary, got %d link lines:\n%s", collapseKeep, emitted, text)
		}
		if !strings.Contains(text, "… and 5 more link elements (same group)") {
			t.Errorf("missing collapse summary:\n%s", text)
		}
	})
}

func idsOf(ns []UINode) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

// TestParseAndBuildNodes covers parse_dom_snapshot + build_nodes_payload:
// strings-table decode, attrs pairs, layout bbox, inputValue, the AX→backend
// parent mapping, and the (-scrollX,-scrollY) document→viewport translation.
func TestParseAndBuildNodes(t *testing.T) {
	// strings: 0=DIV,1=BUTTON,2=id,3=submit,4=typed-text
	snap := domSnapshotResp{
		Strings: []string{"DIV", "BUTTON", "id", "submit", "typed-text"},
		Documents: []domDocRaw{{
			Nodes: domNodesRaw{
				BackendNodeID: []int{100, 200},
				NodeName:      []int{0, 1},                                     // DIV, BUTTON
				Attributes:    [][]int{{}, {2, 3}},                             // node 200: id="submit"
				InputValue:    domRareString{Index: []int{1}, Value: []int{4}}, // backend 200 → "typed-text"
			},
			Layout: domLayoutRaw{
				NodeIndex: []int{1}, // layout for node index 1 (backend 200)
				Bounds:    [][]float64{{50, 400, 120, 40}},
			},
		}},
	}

	dom := parseDOMSnapshot(snap)
	if dom[200] == nil || dom[200].Tag != "button" {
		t.Fatalf("expected button tag for 200, got %+v", dom[200])
	}
	if dom[200].Attrs["id"] != "submit" {
		t.Errorf("attrs not decoded: %+v", dom[200].Attrs)
	}
	if dom[200].Value != "typed-text" {
		t.Errorf("inputValue not decoded: %q", dom[200].Value)
	}
	if dom[200].BBox == nil || dom[200].BBox.Y != 400 {
		t.Fatalf("bbox not decoded: %+v", dom[200].BBox)
	}

	ax := []axNode{
		{NodeID: "ax1", BackendDOMNodeID: 100, ChildIDs: []string{"ax2"}},
		{NodeID: "ax2", BackendDOMNodeID: 200, ParentID: "ax1"},
	}
	nodes := buildNodesPayload(ax, dom, 0, 300) // scrollY=300
	var btn *NodeInput
	for i := range nodes {
		if nodes[i].EncodedID == "200" {
			btn = &nodes[i]
		}
	}
	if btn == nil {
		t.Fatal("backend 200 not in payload")
	}
	if btn.ParentID == nil || *btn.ParentID != "100" {
		t.Errorf("AX parent not mapped to backend id: %+v", btn.ParentID)
	}
	if btn.BBox == nil || btn.BBox.Y != 100 { // 400 - scrollY(300)
		t.Errorf("bbox not translated to viewport coords: %+v", btn.BBox)
	}
}
