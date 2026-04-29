package browser

type frameInfo struct {
	parentID         string
	children         map[string]struct{}
	lastSeen         cdpFrame
	ownerSessionID   string
	ownerBackendNode int
	ownerXPath       string
}

type FrameRegistry struct {
	ownerTargetID   string
	rootFrameID     string
	frames          map[string]*frameInfo
	framesBySession map[string]map[string]struct{}
}

func NewFrameRegistry(ownerTargetID, mainFrameID string) *FrameRegistry {
	r := &FrameRegistry{
		ownerTargetID:   ownerTargetID,
		rootFrameID:     mainFrameID,
		frames:          make(map[string]*frameInfo),
		framesBySession: make(map[string]map[string]struct{}),
	}
	r.ensureNode(mainFrameID)
	return r
}

func (r *FrameRegistry) OnFrameAttached(frameID, parentID, sessionID string) {
	if parentID == "" && frameID != r.rootFrameID {
		r.renameNodeID(r.rootFrameID, frameID)
		r.rootFrameID = frameID
		r.setOwnerSessionID(frameID, sessionID)
		return
	}

	r.ensureNode(frameID)
	if parentID != "" {
		r.ensureNode(parentID)
	}

	info := r.frames[frameID]
	info.parentID = parentID
	if parentID != "" {
		r.frames[parentID].children[frameID] = struct{}{}
	}
	r.setOwnerSessionID(frameID, sessionID)
}

func (r *FrameRegistry) OnFrameNavigated(frame cdpFrame, sessionID string) {
	r.ensureNode(frame.ID)
	info := r.frames[frame.ID]
	info.lastSeen = frame
	r.setOwnerSessionID(frame.ID, sessionID)
	if frame.ParentID == "" && frame.ID != r.rootFrameID {
		r.renameNodeID(r.rootFrameID, frame.ID)
		r.rootFrameID = frame.ID
	}
}

func (r *FrameRegistry) OnNavigatedWithinDocument(frameID, url, sessionID string) {
	r.ensureNode(frameID)
	info := r.frames[frameID]
	frame := info.lastSeen
	frame.URL = url
	info.lastSeen = frame
	r.setOwnerSessionID(frameID, sessionID)
}

func (r *FrameRegistry) OnFrameDetached(frameID, reason string) {
	if reason == "swap" {
		return
	}
	var removeSubtree func(string)
	removeSubtree = func(id string) {
		info := r.frames[id]
		if info == nil {
			return
		}
		for childID := range info.children {
			removeSubtree(childID)
		}
		if info.parentID != "" {
			if parent := r.frames[info.parentID]; parent != nil {
				delete(parent.children, id)
			}
		}
		if info.ownerSessionID != "" {
			if bag := r.framesBySession[info.ownerSessionID]; bag != nil {
				delete(bag, id)
				if len(bag) == 0 {
					delete(r.framesBySession, info.ownerSessionID)
				}
			}
		}
		delete(r.frames, id)
	}
	removeSubtree(frameID)

	if _, ok := r.frames[r.rootFrameID]; !ok {
		for id := range r.frames {
			r.rootFrameID = id
			return
		}
	}
}

func (r *FrameRegistry) AdoptChildSession(childSessionID, childMainFrameID string) {
	r.setOwnerSessionID(childMainFrameID, childSessionID)
}

func (r *FrameRegistry) SeedFromFrameTree(sessionID string, tree frameNode) {
	var walk func(frameNode, string)
	walk = func(node frameNode, parentID string) {
		r.ensureNode(node.Frame.ID)
		info := r.frames[node.Frame.ID]
		info.parentID = parentID
		info.lastSeen = node.Frame
		if parentID != "" {
			r.frames[parentID].children[node.Frame.ID] = struct{}{}
		}
		if info.ownerSessionID == "" {
			r.setOwnerSessionID(node.Frame.ID, sessionID)
		}
		for _, child := range node.ChildFrames {
			walk(child, node.Frame.ID)
		}
	}
	walk(tree, "")
}

func (r *FrameRegistry) SetOwnerBackendNodeID(childFrameID string, backendNodeID int) {
	r.ensureNode(childFrameID)
	r.frames[childFrameID].ownerBackendNode = backendNodeID
}

func (r *FrameRegistry) SetOwnerXPath(childFrameID, xpath string) {
	r.ensureNode(childFrameID)
	r.frames[childFrameID].ownerXPath = xpath
}

func (r *FrameRegistry) MainFrameID() string {
	return r.rootFrameID
}

func (r *FrameRegistry) GetOwnerSessionID(frameID string) string {
	if info := r.frames[frameID]; info != nil {
		return info.ownerSessionID
	}
	return ""
}

func (r *FrameRegistry) GetOwnerBackendNodeID(frameID string) int {
	if info := r.frames[frameID]; info != nil {
		return info.ownerBackendNode
	}
	return 0
}

func (r *FrameRegistry) GetOwnerXPath(frameID string) string {
	if info := r.frames[frameID]; info != nil {
		return info.ownerXPath
	}
	return ""
}

func (r *FrameRegistry) GetParent(frameID string) string {
	if info := r.frames[frameID]; info != nil {
		return info.parentID
	}
	return ""
}

func (r *FrameRegistry) ListAllFrames() []string {
	var out []string
	var walk func(string)
	walk = func(id string) {
		out = append(out, id)
		info := r.frames[id]
		if info == nil {
			return
		}
		for childID := range info.children {
			walk(childID)
		}
	}
	if _, ok := r.frames[r.rootFrameID]; ok {
		walk(r.rootFrameID)
	}
	return out
}

func (r *FrameRegistry) ChildFrames(frameID string) []string {
	info := r.frames[frameID]
	if info == nil {
		return nil
	}
	out := make([]string, 0, len(info.children))
	for childID := range info.children {
		out = append(out, childID)
	}
	return out
}

func (r *FrameRegistry) Frame(frameID string) cdpFrame {
	if info := r.frames[frameID]; info != nil {
		return info.lastSeen
	}
	return shellFrame(frameID)
}

func (r *FrameRegistry) AsFrameTree(rootID string) frameNode {
	var build func(string) frameNode
	build = func(id string) frameNode {
		info := r.frames[id]
		if info == nil {
			return frameNode{Frame: shellFrame(id)}
		}
		node := frameNode{Frame: info.lastSeen}
		for childID := range info.children {
			node.ChildFrames = append(node.ChildFrames, build(childID))
		}
		return node
	}
	return build(rootID)
}

func (r *FrameRegistry) SessionsForFrame(frameID string) []string {
	if sid := r.GetOwnerSessionID(frameID); sid != "" {
		return []string{sid}
	}
	return nil
}

func (r *FrameRegistry) FramesForSession(sessionID string) []string {
	var out []string
	for id := range r.framesBySession[sessionID] {
		out = append(out, id)
	}
	return out
}

func (r *FrameRegistry) ensureNode(frameID string) {
	if _, ok := r.frames[frameID]; ok {
		return
	}
	r.frames[frameID] = &frameInfo{
		children: make(map[string]struct{}),
		lastSeen: shellFrame(frameID),
	}
}

func (r *FrameRegistry) renameNodeID(oldID, newID string) {
	if oldID == newID {
		return
	}
	r.ensureNode(oldID)
	info := r.frames[oldID]
	delete(r.frames, oldID)
	r.frames[newID] = info
	info.lastSeen.ID = newID

	if info.parentID != "" {
		if parent := r.frames[info.parentID]; parent != nil {
			delete(parent.children, oldID)
			parent.children[newID] = struct{}{}
		}
	}
	for childID := range info.children {
		if child := r.frames[childID]; child != nil {
			child.parentID = newID
		}
	}
	if info.ownerSessionID != "" {
		if bag := r.framesBySession[info.ownerSessionID]; bag != nil {
			delete(bag, oldID)
			bag[newID] = struct{}{}
		}
	}
}

func (r *FrameRegistry) setOwnerSessionID(frameID, sessionID string) {
	r.ensureNode(frameID)
	info := r.frames[frameID]
	if info.ownerSessionID == sessionID {
		return
	}
	if info.ownerSessionID != "" {
		if bag := r.framesBySession[info.ownerSessionID]; bag != nil {
			delete(bag, frameID)
			if len(bag) == 0 {
				delete(r.framesBySession, info.ownerSessionID)
			}
		}
	}
	info.ownerSessionID = sessionID
	if sessionID == "" {
		return
	}
	bag := r.framesBySession[sessionID]
	if bag == nil {
		bag = make(map[string]struct{})
		r.framesBySession[sessionID] = bag
	}
	bag[frameID] = struct{}{}
}

func shellFrame(id string) cdpFrame {
	return cdpFrame{
		ID:  id,
		URL: "",
	}
}
