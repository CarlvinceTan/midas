package e2e

import (
	"regexp"
	"strings"
	"testing"

	"github.com/PolymuxOrg/midas/browser"
)

// The snapshot a11y tree + XPathMap is the surface midas exposes to the
// polymux agent (and the seam argus replaces). These tests pin its contract:
// the formatted tree references nodes by [encodedID], and XPathMap resolves
// each encodedID to an xpath that round-trips back into a click/fill.

var refLineRe = regexp.MustCompile(`\[([0-9]+-[0-9]+)\]\s+(\w+)(?::\s*(.*))?$`)

// findRef returns the first encodedID whose tree line role/name contains want.
func findRef(t *testing.T, tree, want string) string {
	t.Helper()
	for _, line := range strings.Split(tree, "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		if m := refLineRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no ref line containing %q in tree:\n%s", want, tree)
	return ""
}

func TestSnapshotTreeShapeAndMaps(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/form.html")

	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.FormattedTree == "" {
		t.Fatal("FormattedTree is empty")
	}
	// Every encodedID marker in the tree must have an XPathMap entry.
	ids := regexp.MustCompile(`\[([0-9]+-[0-9]+)\]`).FindAllStringSubmatch(snap.FormattedTree, -1)
	if len(ids) == 0 {
		t.Fatalf("tree has no [frame-node] id markers:\n%s", snap.FormattedTree)
	}
	missing := 0
	for _, m := range ids {
		if _, ok := snap.XPathMap[m[1]]; !ok {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d tree ids have no XPathMap entry", missing, len(ids))
	}
	// Main-frame ids use ordinal 0.
	for id := range snap.XPathMap {
		if !strings.HasPrefix(id, "0-") {
			t.Errorf("expected main-frame ids to start with 0-, got %q", id)
			break
		}
	}
}

func TestSnapshotRefRoundTripsToClick(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/form.html")

	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ref := findRef(t, snap.FormattedTree, "Sign up")
	xpath, ok := snap.XPathMap[ref]
	if !ok {
		t.Fatalf("ref %q not in XPathMap", ref)
	}

	// Round-trip: the xpath from the snapshot must resolve back to the live
	// element and be actionable — this is the agent's snapshot->act loop.
	if err := page.Locator("xpath=" + xpath).Click(testCtx(t)); err != nil {
		t.Fatalf("click via snapshot xpath %q: %v", xpath, err)
	}
	status := evalString(t, page, "document.getElementById('status').textContent")
	if !strings.HasPrefix(status, "submitted:") {
		t.Errorf("round-trip click did not submit form, status = %q", status)
	}
}

func TestSnapshotRefRoundTripsToFill(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/form.html")

	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ref := findRef(t, snap.FormattedTree, "username")
	xpath, ok := snap.XPathMap[ref]
	if !ok {
		t.Fatalf("ref %q not in XPathMap", ref)
	}
	if err := page.Locator("xpath="+xpath).Fill(testCtx(t), "filled-by-ref"); err != nil {
		t.Fatalf("fill via snapshot xpath %q: %v", xpath, err)
	}
	got := evalString(t, page, "document.getElementById('username').value")
	if got != "filled-by-ref" {
		t.Errorf("username value = %q, want filled-by-ref", got)
	}
}

func TestSnapshotURLMapCapturesLinks(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/links.html")

	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.URLMap) == 0 {
		t.Fatal("URLMap is empty on a page full of links")
	}
	found := false
	for _, href := range snap.URLMap {
		if strings.Contains(href, "/button.html") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("URLMap missing the /button.html link: %v", snap.URLMap)
	}
}

func TestSnapshotReflectsFilledInputValue(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/form.html")

	if err := page.Locator("#username").Fill(testCtx(t), "snapshot-value"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	snap, err := page.Snapshot(testCtx(t))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// The agent needs to see current input values to avoid re-filling.
	if !strings.Contains(snap.FormattedTree, "snapshot-value") {
		t.Errorf("snapshot tree does not reflect the filled value:\n%s", snap.FormattedTree)
	}
}

func TestResolveXPathForLocation(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/button.html")

	box, err := page.Locator("button").BoundingBox(testCtx(t))
	if err != nil {
		t.Fatalf("bounding box: %v", err)
	}
	cx := int(box.X + box.Width/2)
	cy := int(box.Y + box.Height/2)

	loc, err := page.ResolveXPathForLocation(testCtx(t), cx, cy)
	if err != nil {
		t.Fatalf("resolve xpath for location: %v", err)
	}
	if loc == nil || loc.AbsoluteXPath == "" {
		t.Fatalf("no xpath resolved for the button centroid: %+v", loc)
	}
	if !strings.Contains(strings.ToLower(loc.AbsoluteXPath), "button") {
		t.Errorf("resolved xpath %q does not point at the button", loc.AbsoluteXPath)
	}
}

func TestSnapshotPiercesShadowDOM(t *testing.T) {
	page := newPage(t)
	gotoPath(t, page, "/shadow.html")

	pierce := true
	snap, err := page.SnapshotWithOptions(testCtx(t), browser.SnapshotOptions{PierceShadow: &pierce})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(snap.FormattedTree, "Shadow button") {
		t.Errorf("shadow-piercing snapshot missing the shadow button:\n%s", snap.FormattedTree)
	}
}
