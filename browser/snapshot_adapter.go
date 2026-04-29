package browser

import snappkg "github.com/carlvincetan/polymux/internal/midas/browser/snapshot"

type snapshotPageAdapter struct {
	page *Page
}

func newSnapshotPageAdapter(page *Page) snappkg.Page {
	return snapshotPageAdapter{page: page}
}

func (a snapshotPageAdapter) MainFrameID() string {
	return a.page.MainFrameID()
}

func (a snapshotPageAdapter) FrameIDs() []string {
	return a.page.listAllFrameIds()
}

func (a snapshotPageAdapter) FrameTree(rootID string) snappkg.FrameNode {
	return adaptSnapshotFrameNode(a.page.asProtocolFrameTree(rootID))
}

func (a snapshotPageAdapter) SessionForFrame(frameID string) snappkg.Session {
	return a.page.sessionForFrame(frameID)
}

func (a snapshotPageAdapter) Ordinal(frameID string) int {
	return a.page.getOrdinal(frameID)
}

func (a snapshotPageAdapter) OwnerXPath(frameID string) string {
	a.page.mu.RLock()
	defer a.page.mu.RUnlock()
	return a.page.registry.GetOwnerXPath(frameID)
}

func adaptSnapshotFrameNode(node frameNode) snappkg.FrameNode {
	out := snappkg.FrameNode{
		Frame: snappkg.FrameInfo{
			ID:       node.Frame.ID,
			ParentID: node.Frame.ParentID,
			URL:      node.Frame.URL,
			Name:     node.Frame.Name,
		},
	}
	for _, child := range node.ChildFrames {
		out.ChildFrames = append(out.ChildFrames, adaptSnapshotFrameNode(child))
	}
	return out
}
