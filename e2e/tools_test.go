package e2e

import (
	"strings"
	"testing"

	"github.com/PolymuxOrg/midas/tools"
)

// The tools.BoundService is the surface polymux's agent drives. These tests
// exercise each tool end-to-end against a real page and assert the Result
// shapes the orchestrator depends on. Previously this package had no tests.

// toolService binds a fresh tools.BoundService to the shared browser context
// and navigates its active page to the given fixture path.
func toolService(t *testing.T, path string) *tools.BoundService {
	t.Helper()
	h := requireHarness(t)
	// Drive a dedicated page so tools operate on a known target.
	page := newPage(t)
	gotoPath(t, page, path)
	svc := tools.NewService(h.bctx)
	return svc
}

func execTool(t *testing.T, svc *tools.BoundService, name string, input map[string]any) tools.Result {
	t.Helper()
	res, err := svc.Execute(testCtx(t), name, input)
	if err != nil {
		t.Fatalf("tool %q failed: %v", name, err)
	}
	return res
}

func TestToolSpecsCoverExpectedTools(t *testing.T) {
	h := requireHarness(t)
	svc := tools.NewService(h.bctx)
	specs := svc.Specs()
	have := map[string]bool{}
	for _, s := range specs {
		have[s.Name] = true
		if s.Description == "" {
			t.Errorf("tool %q has empty description", s.Name)
		}
	}
	for _, want := range []string{
		"go_to", "nav_back", "click", "type", "fill_form", "scroll",
		"drag_and_drop", "click_and_hold", "keys", "wait", "think",
		"extract", "aria_tree", "screenshot",
	} {
		if !have[want] {
			t.Errorf("tool registry missing %q", want)
		}
	}
}

func TestToolUnknownNameErrors(t *testing.T) {
	h := requireHarness(t)
	svc := tools.NewService(h.bctx)
	if _, err := svc.Execute(testCtx(t), "no_such_tool", nil); err == nil {
		t.Fatal("executing an unknown tool should error")
	}
}

func TestToolGoToAndExtractSnapshot(t *testing.T) {
	svc := toolService(t, "/button.html")

	res := execTool(t, svc, "extract", map[string]any{"property": "snapshot"})
	val, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("extract snapshot Value is not a map: %T", res.Value)
	}
	for _, key := range []string{"url", "formatted_tree", "xpath_map", "url_map"} {
		if _, ok := val[key]; !ok {
			t.Errorf("extract snapshot result missing %q key", key)
		}
	}
	tree, _ := val["formatted_tree"].(string)
	if !strings.Contains(tree, "Click target") {
		t.Errorf("snapshot tree missing button text:\n%s", tree)
	}
}

func TestToolClickAndExtractText(t *testing.T) {
	svc := toolService(t, "/button.html")

	execTool(t, svc, "click", map[string]any{"selector": "button"})

	res := execTool(t, svc, "extract", map[string]any{"selector": "button", "property": "text"})
	val := res.Value.(map[string]any)
	if val["value"] != "Click target" {
		t.Errorf("extracted text = %v, want 'Click target'", val["value"])
	}
}

func TestToolTypeAndExtractValue(t *testing.T) {
	svc := toolService(t, "/button.html")

	execTool(t, svc, "type", map[string]any{"selector": "#name", "text": "via tool"})

	res := execTool(t, svc, "extract", map[string]any{"selector": "#name", "property": "value"})
	val := res.Value.(map[string]any)
	if val["value"] != "via tool" {
		t.Errorf("extracted value = %v, want 'via tool'", val["value"])
	}
}

func TestToolFillForm(t *testing.T) {
	svc := toolService(t, "/form.html")

	execTool(t, svc, "fill_form", map[string]any{
		"fields": []any{
			map[string]any{"selector": "#username", "value": "formuser"},
			map[string]any{"selector": "#email", "value": "form@example.com"},
		},
	})

	res := execTool(t, svc, "extract", map[string]any{"selector": "#username", "property": "value"})
	if res.Value.(map[string]any)["value"] != "formuser" {
		t.Errorf("username not filled by fill_form: %v", res.Value)
	}
}

func TestToolAriaTree(t *testing.T) {
	svc := toolService(t, "/form.html")

	res := execTool(t, svc, "aria_tree", map[string]any{})
	val, ok := res.Value.(map[string]any)
	if !ok {
		t.Fatalf("aria_tree Value is not a map: %T", res.Value)
	}
	if _, ok := val["nodes"]; !ok {
		t.Error("aria_tree result missing 'nodes' key")
	}
}

func TestToolScreenshotReturnsImage(t *testing.T) {
	svc := toolService(t, "/button.html")

	res := execTool(t, svc, "screenshot", map[string]any{})
	// The screenshot tool returns base64 PNG data in its Value; just assert
	// it produced something non-trivial.
	if res.Message == "" && res.Value == nil {
		t.Fatal("screenshot tool returned an empty result")
	}
}

func TestToolWaitForSelector(t *testing.T) {
	svc := toolService(t, "/button.html")

	res := execTool(t, svc, "wait", map[string]any{"selector": "button", "timeout_ms": 5000})
	if res.Message == "" {
		t.Error("wait tool returned empty message")
	}
}

func TestToolScrollByDelta(t *testing.T) {
	svc := toolService(t, "/scrollable.html")

	execTool(t, svc, "scroll", map[string]any{"delta_y": 1000})
	// No assertion on exact scroll position; success is no error + a result.
}

func TestToolThinkIsNoOp(t *testing.T) {
	h := requireHarness(t)
	svc := tools.NewService(h.bctx)
	// think should not require a page and should always succeed.
	if _, err := svc.Execute(testCtx(t), "think", map[string]any{"note": "reasoning"}); err != nil {
		t.Fatalf("think tool failed: %v", err)
	}
}

func TestToolClickMissingSelectorErrors(t *testing.T) {
	svc := toolService(t, "/button.html")
	if _, err := svc.Execute(testCtx(t), "click", map[string]any{}); err == nil {
		t.Fatal("click without a selector should error")
	}
}
