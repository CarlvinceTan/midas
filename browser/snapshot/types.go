package snapshot

import "context"

type Result struct {
	FormattedTree string
	XPathMap      map[string]string
	URLMap        map[string]string
	PerFrame      []PerFrame
}

type Options struct {
	FocusSelector  string
	PierceShadow   *bool
	IncludeIframes *bool
	Experimental   bool
}

type PerFrame struct {
	FrameID       string
	FormattedTree string
	XPathMap      map[string]string
	URLMap        map[string]string
}

type ResolvedLocation struct {
	FrameID       string
	BackendNodeID int
	AbsoluteXPath string
}

type Session interface {
	ID() string
	Send(ctx context.Context, method string, params any, result any) error
}

type FrameInfo struct {
	ID       string
	ParentID string
	URL      string
	Name     string
}

type FrameNode struct {
	Frame       FrameInfo
	ChildFrames []FrameNode
}

type Page interface {
	MainFrameID() string
	FrameIDs() []string
	FrameTree(rootID string) FrameNode
	SessionForFrame(frameID string) Session
	Ordinal(frameID string) int
	OwnerXPath(frameID string) string
}
