package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/carlvincetan/polymux/internal/midas/browser/dom"
)

const selectorUtilityWorldName = "__polymux_selector__"

type resolvedNode struct {
	frame         *Frame
	session       sessionLike
	frameID       string
	sessionID     string
	objectID      string
	nodeID        int
	backendNodeID int
}

type selectorResolveOptions struct {
	pierceShadow bool
	index        int
}

type ElementGeometry struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
	Scale  float64
}

type ElementState struct {
	Visible   bool
	Enabled   bool
	Editable  bool
	TagName   string
	InputType string
}

type resolvedElement struct {
	node     *resolvedNode
	state    ElementState
	geometry *ElementGeometry
}

func (e *resolvedElement) ObjectID() string {
	return e.node.objectID
}

func (e *resolvedElement) BackendNodeID() int {
	return e.node.backendNodeID
}

func (e *resolvedElement) Session() sessionLike {
	return e.node.session
}

func (e *resolvedElement) Frame() *Frame {
	return e.node.frame
}

func (e *resolvedElement) FrameID() string {
	return e.node.frameID
}

func (e *resolvedElement) State() ElementState {
	return e.state
}

func (e *resolvedElement) Geometry() *ElementGeometry {
	return e.geometry
}

func (e *resolvedElement) IsVisible() bool {
	return e.state.Visible && e.geometry != nil && e.geometry.Width > 0 && e.geometry.Height > 0
}

func (e *resolvedElement) IsEnabled() bool {
	return e.state.Enabled
}

func (e *resolvedElement) IsEditable() bool {
	return e.state.Editable
}

func (e *resolvedElement) IsInput() bool {
	return e.state.TagName == "input" || e.state.TagName == "textarea"
}

func (e *resolvedElement) IsSelect() bool {
	return e.state.TagName == "select"
}

func (e *resolvedElement) IsCheckable() bool {
	if e.state.TagName != "input" {
		return false
	}
	t := e.state.InputType
	return t == "checkbox" || t == "radio"
}

func (e *resolvedElement) Centroid() (float64, float64) {
	if e.geometry == nil {
		return 0, 0
	}
	return e.geometry.X + e.geometry.Width/2, e.geometry.Y + e.geometry.Height/2
}

func (p *Page) childFrameForOwnerNode(parentFrameID string, backendNodeID int) *Frame {
	if backendNodeID == 0 {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, childID := range p.registry.ChildFrames(parentFrameID) {
		if p.registry.GetOwnerBackendNodeID(childID) == backendNodeID {
			return &Frame{page: p, frameID: childID}
		}
	}
	return nil
}

func (p *Page) resolveSelectorTarget(ctx context.Context, root *Frame, selector string, pierceShadow bool) (*Frame, string, error) {
	parts := splitSelectorPath(selector)
	if len(parts) == 0 {
		return root, selector, nil
	}

	frame := root
	for len(parts) > 1 {
		next, err := frame.resolveChildFrame(ctx, parts[0], pierceShadow)
		if err != nil {
			return nil, "", err
		}
		frame = next
		parts = parts[1:]
	}
	return frame, parts[0], nil
}

func (f *Frame) resolveSelector(ctx context.Context, selector string, opts selectorResolveOptions) (*resolvedNode, func(), error) {
	targetFrame, finalSelector, err := f.page.resolveSelectorTarget(ctx, f, selector, opts.pierceShadow)
	if err != nil {
		return nil, nil, err
	}

	session := targetFrame.page.sessionForFrame(targetFrame.frameID)
	ctxID, err := targetFrame.page.ensureSelectorWorld(ctx, session, targetFrame.frameID)
	if err != nil {
		return nil, nil, err
	}

	params := map[string]any{
		"expression":    dom.BuildContextInvocation(dom.HelperResolve, finalSelector, opts.pierceShadow, opts.index),
		"returnByValue": false,
		"awaitPromise":  true,
		"contextId":     ctxID,
	}

	var res struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := session.Send(ctx, "Runtime.evaluate", params, &res); err != nil {
		return nil, nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, nil, errors.New(defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}
	if res.Result.ObjectID == "" {
		return nil, nil, fmt.Errorf("element not found")
	}

	var describe struct {
		Node struct {
			NodeID        int `json:"nodeId"`
			BackendNodeID int `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := session.Send(ctx, "DOM.describeNode", map[string]any{
		"objectId": res.Result.ObjectID,
	}, &describe); err != nil {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": res.Result.ObjectID,
		}, nil)
		return nil, nil, err
	}

	release := func() {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": res.Result.ObjectID,
		}, nil)
	}
	return &resolvedNode{
		frame:         targetFrame,
		session:       session,
		frameID:       targetFrame.frameID,
		sessionID:     session.ID(),
		objectID:      res.Result.ObjectID,
		nodeID:        describe.Node.NodeID,
		backendNodeID: describe.Node.BackendNodeID,
	}, release, nil
}

func (n *resolvedNode) callFunction(ctx context.Context, body string, result any) error {
	params := map[string]any{
		"objectId":            n.objectID,
		"functionDeclaration": fmt.Sprintf("function() {\n%s\n}", body),
		"returnByValue":       true,
		"awaitPromise":        true,
	}

	var res struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := n.session.Send(ctx, "Runtime.callFunctionOn", params, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return errors.New(defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}
	if result == nil {
		return nil
	}

	buf, err := json.Marshal(res.Result.Value)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, result)
}

func (p *Page) ensureSelectorWorld(ctx context.Context, session sessionLike, frameID string) (int64, error) {
	if session == nil || frameID == "" {
		return 0, errors.New("selector world requires session and frame")
	}
	if ctxID := p.execCtx.UtilityWorldID(session.ID(), frameID); ctxID != 0 {
		if err := p.ensureSelectorHelperInjected(ctx, session, ctxID); err != nil {
			return 0, err
		}
		return ctxID, nil
	}

	var res struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := session.Send(ctx, "Page.createIsolatedWorld", map[string]any{
		"frameId":   frameID,
		"worldName": selectorUtilityWorldName,
	}, &res); err != nil {
		return 0, err
	}
	if res.ExecutionContextID == 0 {
		waitCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ctxID, err := p.execCtx.WaitForUtilityWorld(waitCtx, session, frameID)
		if err != nil {
			return 0, err
		}
		res.ExecutionContextID = ctxID
	}
	p.execCtx.SetUtilityWorldID(session.ID(), frameID, res.ExecutionContextID)
	if err := p.ensureSelectorHelperInjected(ctx, session, res.ExecutionContextID); err != nil {
		return 0, err
	}
	return res.ExecutionContextID, nil
}

func (p *Page) ensureSelectorHelperInjected(ctx context.Context, session sessionLike, ctxID int64) error {
	if ctxID == 0 || session == nil {
		return errors.New("selector helper requires valid execution context")
	}

	p.mu.RLock()
	_, ok := p.selectorHelpers[session.ID()][ctxID]
	p.mu.RUnlock()
	if ok {
		return nil
	}

	var res struct {
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := session.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    dom.BootstrapScript(),
		"returnByValue": true,
		"awaitPromise":  true,
		"contextId":     ctxID,
	}, &res); err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return errors.New(defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}

	p.mu.Lock()
	if p.selectorHelpers[session.ID()] == nil {
		p.selectorHelpers[session.ID()] = make(map[int64]struct{})
	}
	p.selectorHelpers[session.ID()][ctxID] = struct{}{}
	p.mu.Unlock()
	return nil
}

type actionabilityResult struct {
	Found    bool                `json:"found"`
	Error    string              `json:"error"`
	State    *elementStateResult `json:"state"`
	Geometry *geometryResult     `json:"geometry"`
}

type elementStateResult struct {
	Visible   bool   `json:"visible"`
	Enabled   bool   `json:"enabled"`
	Editable  bool   `json:"editable"`
	TagName   string `json:"tagName"`
	InputType string `json:"inputType"`
}

type geometryResult struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

func (f *Frame) resolveElement(ctx context.Context, selector string, opts selectorResolveOptions) (*resolvedElement, func(), error) {
	targetFrame, finalSelector, err := f.page.resolveSelectorTarget(ctx, f, selector, opts.pierceShadow)
	if err != nil {
		return nil, nil, err
	}

	session := targetFrame.page.sessionForFrame(targetFrame.frameID)
	ctxID, err := targetFrame.page.ensureSelectorWorld(ctx, session, targetFrame.frameID)
	if err != nil {
		return nil, nil, err
	}

	params := map[string]any{
		"expression":    dom.BuildContextInvocation(dom.HelperResolve, finalSelector, opts.pierceShadow, opts.index),
		"returnByValue": false,
		"awaitPromise":  true,
		"contextId":     ctxID,
	}

	var res struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := session.Send(ctx, "Runtime.evaluate", params, &res); err != nil {
		return nil, nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, nil, errors.New(defaultString(res.ExceptionDetails.Exception.Description, res.ExceptionDetails.Text))
	}
	if res.Result.ObjectID == "" {
		return nil, nil, notFoundError(selector, opts.index, targetFrame.frameID)
	}

	var describe struct {
		Node struct {
			NodeID        int `json:"nodeId"`
			BackendNodeID int `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := session.Send(ctx, "DOM.describeNode", map[string]any{
		"objectId": res.Result.ObjectID,
	}, &describe); err != nil {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": res.Result.ObjectID,
		}, nil)
		return nil, nil, err
	}

	node := &resolvedNode{
		frame:         targetFrame,
		session:       session,
		frameID:       targetFrame.frameID,
		sessionID:     session.ID(),
		objectID:      res.Result.ObjectID,
		nodeID:        describe.Node.NodeID,
		backendNodeID: describe.Node.BackendNodeID,
	}

	stateResult, err := node.getState(ctx)
	if err != nil {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": res.Result.ObjectID,
		}, nil)
		return nil, nil, err
	}

	geometryResult, err := node.getGeometry(ctx)
	if err != nil {
		geometryResult = nil
	}

	release := func() {
		_ = session.Send(context.Background(), "Runtime.releaseObject", map[string]any{
			"objectId": res.Result.ObjectID,
		}, nil)
	}

	var geometry *ElementGeometry
	if geometryResult != nil {
		geometry = &ElementGeometry{
			X:      geometryResult.X,
			Y:      geometryResult.Y,
			Width:  geometryResult.Width,
			Height: geometryResult.Height,
			Scale:  geometryResult.Scale,
		}
	}

	state := ElementState{
		Visible:   stateResult.Visible,
		Enabled:   stateResult.Enabled,
		Editable:  stateResult.Editable,
		TagName:   stateResult.TagName,
		InputType: stateResult.InputType,
	}

	return &resolvedElement{
		node:     node,
		state:    state,
		geometry: geometry,
	}, release, nil
}

func (n *resolvedNode) getState(ctx context.Context) (*elementStateResult, error) {
	var result elementStateResult
	err := n.callFunction(ctx, `
		this.scrollIntoView({ block: "center", inline: "center" });
		const style = getComputedStyle(this);
		const rect = this.getBoundingClientRect();
		const tagName = this.tagName ? this.tagName.toLowerCase() : "";
		const inputType = tagName === "input" ? (this.type || "text").toLowerCase() : "";
		const isDisabled = this.disabled;
		const isReadOnly = this.readOnly;
		let editable = false;
		if (tagName === "input") {
			const type = inputType;
			const readOnlyTypes = ["checkbox", "radio", "submit", "button", "image", "file", "range", "color"];
			if (!readOnlyTypes.includes(type) && !isDisabled && !isReadOnly) editable = true;
		} else if (tagName === "textarea") {
			if (!isDisabled && !isReadOnly) editable = true;
		} else if (this.isContentEditable) {
			editable = true;
		}
		let enabled = !isDisabled;
		const formTags = ["input", "textarea", "select", "button", "option", "optgroup"];
		if (!formTags.includes(tagName)) enabled = true;
		return {
			visible: this.isConnected && style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0,
			enabled: enabled,
			editable: editable,
			tagName: tagName,
			inputType: inputType
		};
	`, &result)
	return &result, err
}

func (n *resolvedNode) getGeometry(ctx context.Context) (*geometryResult, error) {
	var result geometryResult
	err := n.callFunction(ctx, `
		this.scrollIntoView({ block: "center", inline: "center" });
		const rect = this.getBoundingClientRect();
		if (rect.width <= 0 || rect.height <= 0) return null;
		return { x: rect.x, y: rect.y, width: rect.width, height: rect.height, scale: 1 };
	`, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}
