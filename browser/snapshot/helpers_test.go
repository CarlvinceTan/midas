package snapshot

import "testing"

func TestParseXPathToSteps(t *testing.T) {
	t.Parallel()

	got := parseXPathToSteps(" //iframe[1]/div[2]//SPAN ")
	if len(got) != 3 {
		t.Fatalf("expected 3 steps, got %#v", got)
	}
	if got[0].Axis != axisDesc || got[0].Name != "iframe" {
		t.Fatalf("unexpected first step: %#v", got[0])
	}
	if got[2].Name != "span" {
		t.Fatalf("unexpected last step: %#v", got[2])
	}
}

func TestBuildXPathFromSteps(t *testing.T) {
	t.Parallel()

	got := buildXPathFromSteps([]xpathStep{
		{Axis: axisChild, Raw: "iframe[1]", Name: "iframe"},
		{Axis: axisDesc, Raw: "div[@id='main']", Name: "div"},
		{Axis: axisChild, Raw: "span", Name: "span"},
	})
	if got != "/iframe[1]//div[@id='main']/span" {
		t.Fatalf("unexpected xpath: %q", got)
	}
}

func TestDiffCombinedTrees(t *testing.T) {
	t.Parallel()

	prev := "[0-1] root\n  [0-2] old"
	next := "[0-1] root\n  [0-2] old\n  [0-3] new"
	if got := DiffTrees(prev, next); got != "[0-3] new" {
		t.Fatalf("unexpected diff: %q", got)
	}
}

func TestRemoveRedundantStaticTextChildren(t *testing.T) {
	t.Parallel()

	parent := &treeA11yNode{Role: "button", Name: "HelloWorld", NodeID: "root"}
	children := []*treeA11yNode{
		{Role: "StaticText", Name: "Hello", NodeID: "c1"},
		{Role: "StaticText", Name: "World", NodeID: "c2"},
		{Role: "button", Name: "Child", NodeID: "c3"},
	}
	pruned := removeRedundantStaticTextChildren(parent, children)
	if len(pruned) != 1 || pruned[0].NodeID != "c3" {
		t.Fatalf("unexpected pruned children: %#v", pruned)
	}
}
