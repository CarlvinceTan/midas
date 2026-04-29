package browser

import "testing"

func TestFrameRegistryRootSwapPreservesOwnership(t *testing.T) {
	t.Parallel()

	registry := NewFrameRegistry("target-1", "root-1")
	registry.OnFrameAttached("root-1", "", "session-1")
	registry.OnFrameNavigated(cdpFrame{ID: "root-1", URL: "about:blank"}, "session-1")

	registry.OnFrameAttached("root-2", "", "session-2")

	if got := registry.MainFrameID(); got != "root-2" {
		t.Fatalf("expected root-2, got %s", got)
	}
	if got := registry.GetOwnerSessionID("root-2"); got != "session-2" {
		t.Fatalf("expected session-2 owner, got %s", got)
	}
}

func TestFrameRegistryDetachRemovePrunesSubtree(t *testing.T) {
	t.Parallel()

	registry := NewFrameRegistry("target-1", "root")
	registry.OnFrameAttached("child", "root", "session-1")
	registry.OnFrameAttached("grandchild", "child", "session-2")

	registry.OnFrameDetached("child", "remove")

	if frames := registry.ListAllFrames(); len(frames) != 1 || frames[0] != "root" {
		t.Fatalf("unexpected frame set: %#v", frames)
	}
}
