package tui

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type coreReference struct {
	Mouse []struct {
		Name     string          `json:"name"`
		Event    MouseEvent      `json:"event"`
		Result   serializedMouse `json:"result"`
		Received MouseEvent      `json:"received"`
	} `json:"mouse"`
	Containers []struct {
		Name  string   `json:"name"`
		Width int      `json:"width"`
		Out   []string `json:"out"`
	} `json:"containers"`
	Boxes []struct {
		Name             string   `json:"name"`
		Width            int      `json:"width"`
		Out              []string `json:"out"`
		First            []string `json:"first"`
		ChildRenderCalls int      `json:"childRenderCalls"`
		BGCalls          int      `json:"bgCalls"`
	} `json:"boxes"`
	Spacer []struct {
		Lines int      `json:"lines"`
		Out   []string `json:"out"`
	} `json:"spacer"`
	Allocation []struct {
		Name    string `json:"name"`
		Entries []struct {
			Basis   *int `json:"basis"`
			Grow    *int `json:"grow"`
			Shrink  *int `json:"shrink"`
			MinSize *int `json:"minSize"`
			MaxSize *int `json:"maxSize"`
		} `json:"entries"`
		Intrinsic []int `json:"intrinsic"`
		Available *int  `json:"available"`
		Gap       int   `json:"gap"`
		Out       []int `json:"out"`
	} `json:"allocation"`
	VStacks []stackReference `json:"vstacks"`
	HStacks []struct {
		Name         string           `json:"name"`
		Width        int              `json:"width"`
		Out          []string         `json:"out"`
		RenderWidths map[string][]int `json:"renderWidths"`
	} `json:"hstacks"`
	Composite []struct {
		Base         string `json:"base"`
		Overlay      string `json:"overlay"`
		StartCol     int    `json:"startCol"`
		OverlayWidth int    `json:"overlayWidth"`
		TotalWidth   int    `json:"totalWidth"`
		Out          string `json:"out"`
	} `json:"composite"`
	ScrollTraces []struct {
		Name   string                 `json:"name"`
		States []scrollStateReference `json:"states"`
	} `json:"scrollTraces"`
	Layouts             []layoutCaseReference `json:"layouts"`
	TerminalColorInputs []struct {
		Data        string              `json:"data"`
		IsOSC11     bool                `json:"isOsc11"`
		ColorFound  bool                `json:"colorFound"`
		Color       RGBColor            `json:"color"`
		SchemeFound bool                `json:"schemeFound"`
		Scheme      TerminalColorScheme `json:"scheme"`
	} `json:"terminalColorInputs"`
	KeyInputs []struct {
		Data    string `json:"data"`
		Release bool   `json:"release"`
		Debug   bool   `json:"debug"`
	} `json:"keyInputs"`
	Overlays []struct {
		Name         string        `json:"name"`
		Out          []string      `json:"out"`
		BoundsFound  bool          `json:"boundsFound"`
		Bounds       OverlayBounds `json:"bounds"`
		FocusID      string        `json:"focusId"`
		Focused      bool          `json:"focused"`
		RenderWidths []int         `json:"renderWidths"`
	} `json:"overlays"`
	FocusTraces []struct {
		Name   string                `json:"name"`
		States []focusStateReference `json:"states"`
	} `json:"focusTraces"`
	CursorAndResets []struct {
		Name        string         `json:"name"`
		CursorFound bool           `json:"cursorFound"`
		Cursor      CursorPosition `json:"cursor"`
		Lines       []string       `json:"lines"`
		ResetLines  []string       `json:"resetLines"`
	} `json:"cursorAndResets"`
	BaseInputTraces []struct {
		Name               string                `json:"name"`
		ListenerLog        []string              `json:"listenerLog"`
		Inputs             []string              `json:"inputs"`
		Invalidations      int                   `json:"invalidations"`
		DebugCount         int                   `json:"debugCount"`
		Schemes            []TerminalColorScheme `json:"schemes"`
		CellDimensions     CellDimensions        `json:"cellDimensions"`
		BackgroundFound    bool                  `json:"backgroundFound"`
		Background         RGBColor              `json:"background"`
		QueriedSchemeFound bool                  `json:"queriedSchemeFound"`
		QueriedScheme      TerminalColorScheme   `json:"queriedScheme"`
		TerminalLog        []string              `json:"terminalLog"`
	} `json:"baseInputTraces"`
	Lifecycle []struct {
		Name    string   `json:"name"`
		Log     []string `json:"log"`
		Renders int      `json:"renders"`
	} `json:"lifecycle"`
}

type stackReference struct {
	Name  string   `json:"name"`
	Width int      `json:"width"`
	Out   []string `json:"out"`
}

type serializedMouse struct {
	Found         bool   `json:"found"`
	Handled       bool   `json:"handled"`
	Capture       bool   `json:"capture"`
	Focus         bool   `json:"focus"`
	Render        *bool  `json:"render"`
	TargetID      string `json:"targetId"`
	OriginX       int    `json:"originX"`
	OriginY       int    `json:"originY"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FocusTargetID string `json:"focusTargetId"`
}

type scrollStateReference struct {
	Label            string        `json:"label"`
	ScrollTop        int           `json:"scrollTop"`
	FollowingEnd     bool          `json:"followingEnd"`
	ViewportHeight   int           `json:"viewportHeight"`
	Scrollbar        ScrollbarMode `json:"scrollbar"`
	ScrollbarVisible bool          `json:"scrollbarVisible"`
	ScrollbarActive  bool          `json:"scrollbarActive"`
	ContentWidth1    int           `json:"contentWidth1"`
	ContentWidth5    int           `json:"contentWidth5"`
	RenderRequests   int           `json:"renderRequests"`
	Unconsumed       *int          `json:"unconsumed"`
}

type serializedLayoutBox struct {
	ID           string                `json:"id"`
	Rect         LayoutRect            `json:"rect"`
	Clip         LayoutRect            `json:"clip"`
	LineOffset   int                   `json:"lineOffset"`
	Lines        []string              `json:"lines"`
	ScrollViewID string                `json:"scrollViewId"`
	Layer        int                   `json:"layer"`
	Children     []serializedLayoutBox `json:"children"`
}

type layoutCaseReference struct {
	Name                string              `json:"name"`
	Width               int                 `json:"width"`
	Height              int                 `json:"height"`
	Lines               []string            `json:"lines"`
	PrimaryScrollViewID string              `json:"primaryScrollViewId"`
	Root                serializedLayoutBox `json:"root"`
	Hits                []string            `json:"hits"`
	Geometry            *ScrollbarGeometry  `json:"geometry"`
	ScrollViewsAt       []string            `json:"scrollViewsAt"`
	ScrollTop           *int                `json:"scrollTop"`
	RenderWidths        []int               `json:"renderWidths"`
}

type focusStateReference struct {
	Label      string          `json:"label"`
	FocusID    string          `json:"focusId"`
	Focused    map[string]bool `json:"focused"`
	HasOverlay bool            `json:"hasOverlay"`
	Hidden     map[string]bool `json:"hidden"`
}

func loadCoreReference(t *testing.T) coreReference {
	t.Helper()
	data, err := os.ReadFile("testdata/core-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference coreReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Mouse)+len(reference.Overlays)+len(reference.Layouts)+len(reference.KeyInputs) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return reference
}

type probeComponent struct {
	id            string
	renderFn      func(int) []string
	mouseFn       func(MouseEvent) *MouseResult
	renderWidths  []int
	mouseEvents   []MouseEvent
	invalidations int
}

func fixedProbe(id string, lines []string, mouseFn ...func(MouseEvent) *MouseResult) *probeComponent {
	probe := &probeComponent{id: id}
	probe.renderFn = func(int) []string { return append([]string(nil), lines...) }
	if len(mouseFn) > 0 {
		probe.mouseFn = mouseFn[0]
	}
	return probe
}

func widthProbe(id string, rows int) *probeComponent {
	probe := &probeComponent{id: id}
	probe.renderFn = func(width int) []string {
		lines := make([]string, rows)
		for index := range lines {
			lines[index] = id + ":" + itoa(width) + ":" + itoa(index)
		}
		return lines
	}
	return probe
}

func (p *probeComponent) Render(width int) []string {
	p.renderWidths = append(p.renderWidths, width)
	if p.renderFn == nil {
		return []string{}
	}
	return p.renderFn(width)
}

func (p *probeComponent) Invalidate() { p.invalidations++ }

func (p *probeComponent) HandleMouse(event MouseEvent) *MouseResult {
	p.mouseEvents = append(p.mouseEvents, event)
	if p.mouseFn == nil {
		return nil
	}
	return p.mouseFn(event)
}

type inputContainer struct {
	Container
	id string
}

func newInputContainer(id string) *inputContainer {
	container := &inputContainer{Container: Container{Children: []Component{}}, id: id}
	container.SetMouseFocusOwner(container)
	return container
}

func (*inputContainer) HandleInput(string) {}

type namedVStack struct {
	*VStack
	id string
}

type namedHStack struct {
	*HStack
	id string
}

type namedScrollView struct {
	*ScrollView
	id string
}

type focusProbe struct {
	*probeComponent
	FocusState
	inputs       []string
	wantsRelease bool
}

func newFocusProbe(id string, lines []string) *focusProbe {
	probe := fixedProbe(id, lines)
	probe.renderWidths = []int{}
	return &focusProbe{probeComponent: probe}
}

func (p *focusProbe) HandleInput(data string) { p.inputs = append(p.inputs, data) }

func (p *focusProbe) WantsKeyRelease() bool { return p.wantsRelease }

func componentID(component Component) string {
	switch value := component.(type) {
	case *probeComponent:
		return value.id
	case *inputContainer:
		return value.id
	case *namedVStack:
		return value.id
	case *namedHStack:
		return value.id
	case *namedScrollView:
		return value.id
	case *focusProbe:
		return value.id
	default:
		return ""
	}
}

func serializeMouse(result *MouseResult) serializedMouse {
	if result == nil {
		return serializedMouse{}
	}
	serialized := serializedMouse{
		Found:         true,
		Handled:       result.Handled,
		Capture:       result.Capture,
		Focus:         result.Focus,
		Render:        result.Render,
		FocusTargetID: componentID(result.FocusTarget),
	}
	if result.Target != nil {
		serialized.TargetID = componentID(result.Target.Component)
		serialized.OriginX = result.Target.OriginX
		serialized.OriginY = result.Target.OriginY
		serialized.Width = result.Target.Width
		serialized.Height = result.Target.Height
	}
	return serialized
}

func TestMouseDispatchMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Mouse {
		t.Run(test.Name, func(t *testing.T) {
			var target Component
			var received func() []MouseEvent
			switch test.Name {
			case "undefined":
				probe := fixedProbe("direct-undefined", []string{"x"})
				target, received = probe, func() []MouseEvent { return probe.mouseEvents }
			case "render-only":
				probe := fixedProbe("direct-render-only", []string{"x"}, func(MouseEvent) *MouseResult {
					value := true
					return &MouseResult{Render: &value}
				})
				target, received = probe, func() []MouseEvent { return probe.mouseEvents }
			case "handled":
				probe := fixedProbe("direct-handled", []string{"x"}, func(MouseEvent) *MouseResult {
					value := false
					return &MouseResult{Handled: true, Render: &value}
				})
				target, received = probe, func() []MouseEvent { return probe.mouseEvents }
			case "capture-implies-handled":
				probe := fixedProbe("direct-capture-implies-handled", []string{"x"}, func(MouseEvent) *MouseResult {
					return &MouseResult{Capture: true}
				})
				target, received = probe, func() []MouseEvent { return probe.mouseEvents }
			case "focus-implies-handled":
				probe := fixedProbe("direct-focus-implies-handled", []string{"x"}, func(MouseEvent) *MouseResult {
					return &MouseResult{Focus: true}
				})
				target, received = probe, func() []MouseEvent { return probe.mouseEvents }
			case "container-child-transform":
				first := fixedProbe("first", []string{"a", "b"})
				second := fixedProbe("second", []string{"c"}, func(event MouseEvent) *MouseResult {
					value := event.X >= 0
					return &MouseResult{Handled: true, Focus: true, Render: &value}
				})
				container := &Container{Children: []Component{first, second}}
				container.Render(8)
				target, received = container, func() []MouseEvent { return second.mouseEvents }
			case "container-focus-delegation":
				child := fixedProbe("focus-child", []string{"a"}, func(MouseEvent) *MouseResult {
					return &MouseResult{Focus: true}
				})
				container := newInputContainer("input-container")
				container.AddChild(child)
				container.Render(6)
				target, received = container, func() []MouseEvent { return child.mouseEvents }
			case "box-content-transform":
				child := fixedProbe("box-child", []string{"a", "b"}, func(MouseEvent) *MouseResult {
					return &MouseResult{Capture: true}
				})
				box := NewBox(2, 1, nil)
				box.AddChild(child)
				box.Render(10)
				target, received = box, func() []MouseEvent { return child.mouseEvents }
			default:
				t.Fatalf("unknown mouse case %q", test.Name)
			}

			got := serializeMouse(DispatchMouseEvent(target, test.Event))
			if !reflect.DeepEqual(got, test.Result) {
				t.Errorf("dispatch result = %#v, reference = %#v", got, test.Result)
			}
			events := received()
			if len(events) != 1 || !reflect.DeepEqual(events[0], test.Received) {
				t.Errorf("received events = %#v, reference = %#v", events, test.Received)
			}
		})
	}
}

func TestContainerMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Containers {
		t.Run(test.Name, func(t *testing.T) {
			container := &Container{Children: []Component{}}
			switch test.Name {
			case "concatenates-in-order":
				container.AddChild(fixedProbe("a", []string{"a", "b"}))
				container.AddChild(fixedProbe("b", []string{}))
				container.AddChild(fixedProbe("c", []string{"c"}))
			case "remove-first-identity-match":
				duplicate := fixedProbe("duplicate", []string{"d"})
				container.AddChild(duplicate)
				container.AddChild(fixedProbe("other", []string{"o"}))
				container.AddChild(duplicate)
				container.RemoveChild(duplicate)
			default:
				t.Fatalf("unknown container case %q", test.Name)
			}
			if got := container.Render(test.Width); !reflect.DeepEqual(got, test.Out) {
				t.Errorf("Render(%d) = %q, reference = %q", test.Width, got, test.Out)
			}
		})
	}
}

func TestBoxMatchesVendoredReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Boxes {
		t.Run(test.Name, func(t *testing.T) {
			var box *Box
			var child *probeComponent
			bgCalls := 0
			switch test.Name {
			case "empty":
				box = NewDefaultBox()
			case "empty-child":
				box = NewBox(1, 2, nil)
				box.AddChild(fixedProbe("empty-child", []string{}))
			case "patched-content-markers":
				box = NewBox(1, 1, nil)
				box.AddChild(fixedProbe("plain", []string{"hi", "world"}))
			case "does-not-truncate":
				box = NewBox(0, 0, nil)
				box.AddChild(fixedProbe("overwide", []string{"over-wide-value"}))
			case "background-and-wide-text":
				box = NewBox(2, 1, func(value string) string { return "\x1b[44m" + value + "\x1b[49m" })
				box.AddChild(fixedProbe("styled", []string{"\x1b[31mred\x1b[0m", "你"}))
			case "cache-sample":
				box = NewBox(1, 0, func(value string) string {
					bgCalls++
					return "<" + value + ">"
				})
				child = fixedProbe("cache-child", []string{"cached"})
				box.AddChild(child)
				first := box.Render(test.Width)
				if !reflect.DeepEqual(first, test.First) {
					t.Errorf("first render = %q, reference = %q", first, test.First)
				}
			default:
				t.Fatalf("unknown Box case %q", test.Name)
			}

			got := box.Render(test.Width)
			if !reflect.DeepEqual(got, test.Out) {
				t.Errorf("Render(%d) = %q, reference = %q", test.Width, got, test.Out)
			}
			if test.Name == "cache-sample" {
				if len(child.renderWidths) != test.ChildRenderCalls || bgCalls != test.BGCalls {
					t.Errorf("cache calls = child %d, bg %d; reference = child %d, bg %d",
						len(child.renderWidths), bgCalls, test.ChildRenderCalls, test.BGCalls)
				}
			}
		})
	}
}

func TestSpacerMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Spacer {
		if got := NewSpacer(test.Lines).Render(20); !reflect.DeepEqual(got, test.Out) {
			t.Errorf("NewSpacer(%d).Render(20) = %q, reference = %q", test.Lines, got, test.Out)
		}
	}
}

func TestAllocateStackSizesMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Allocation {
		t.Run(test.Name, func(t *testing.T) {
			entries := make([]StackLayoutEntry, len(test.Entries))
			for index, entry := range test.Entries {
				entries[index] = StackLayoutEntry{
					Basis: entry.Basis, Grow: entry.Grow, Shrink: entry.Shrink,
					MinSize: entry.MinSize, MaxSize: entry.MaxSize,
				}
			}
			got := AllocateStackSizes(entries, test.Intrinsic, test.Available, test.Gap)
			if !reflect.DeepEqual(got, test.Out) {
				t.Errorf("AllocateStackSizes(...) = %v, reference = %v", got, test.Out)
			}
		})
	}
}

func TestVStackMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.VStacks {
		t.Run(test.Name, func(t *testing.T) {
			var stack *VStack
			switch test.Name {
			case "basic-gap":
				stack = NewVStackWithOptions(StackOptions{Gap: 1},
					NewStackChild(fixedProbe("a", []string{"a", "aa"}), StackEntryOptions{}),
					NewStackChild(fixedProbe("b", []string{"b"}), StackEntryOptions{}),
				)
			case "basis-crop-pad":
				one, three := 1, 3
				stack = NewVStackWithOptions(StackOptions{},
					NewStackChild(fixedProbe("a", []string{"a0", "a1", "a2"}), StackEntryOptions{Basis: &one}),
					NewStackChild(fixedProbe("b", []string{"b0"}), StackEntryOptions{Basis: &three}),
				)
			case "visibility-root-width":
				stack = NewVStackWithOptions(StackOptions{},
					NewStackChild(fixedProbe("small", []string{"small"}), StackEntryOptions{Visible: func(viewport LayoutViewport) bool { return viewport.Width < 10 }}),
					NewStackChild(fixedProbe("large", []string{"large"}), StackEntryOptions{Visible: func(viewport LayoutViewport) bool { return viewport.Width >= 10 }}),
				)
			default:
				t.Fatalf("unknown VStack case %q", test.Name)
			}
			if got := stack.Render(test.Width); !reflect.DeepEqual(got, test.Out) {
				t.Errorf("Render(%d) = %q, reference = %q", test.Width, got, test.Out)
			}
		})
	}
}

func TestHStackMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.HStacks {
		t.Run(test.Name, func(t *testing.T) {
			var stack *HStack
			probes := map[string]*probeComponent{}
			switch test.Name {
			case "basic-gap":
				one := 1
				probes["a"], probes["b"] = widthProbe("a", 2), widthProbe("b", 1)
				stack = NewHStackWithOptions(StackOptions{Gap: 1},
					NewStackChild(probes["a"], StackEntryOptions{Grow: &one}),
					NewStackChild(probes["b"], StackEntryOptions{Grow: &one}),
				)
			case "center-align", "end-align":
				align := AlignCenter
				if test.Name == "end-align" {
					align = AlignEnd
				}
				gap := 0
				if test.Name == "center-align" {
					gap = 1
				}
				stack = NewHStackWithOptions(StackOptions{Gap: gap, Align: align},
					NewStackChild(fixedProbe("a", []string{"a0", "a1", "a2"}), StackEntryOptions{}),
					NewStackChild(fixedProbe("b", []string{"b0"}), StackEntryOptions{}),
				)
			case "zero-width-child":
				zero, six := 0, 6
				probes["zero"], probes["full"] = widthProbe("zero", 1), widthProbe("full", 1)
				stack = NewHStackWithOptions(StackOptions{},
					NewStackChild(probes["zero"], StackEntryOptions{Basis: &zero, Shrink: &zero}),
					NewStackChild(probes["full"], StackEntryOptions{Basis: &six, Shrink: &zero}),
				)
			case "wide-and-ansi":
				four, zero := 4, 0
				stack = NewHStackWithOptions(StackOptions{},
					NewStackChild(fixedProbe("wide", []string{"你好"}), StackEntryOptions{Basis: &four, Shrink: &zero}),
					NewStackChild(fixedProbe("ansi", []string{"\x1b[31mred\x1b[0m"}), StackEntryOptions{Basis: &four, Shrink: &zero}),
				)
			default:
				t.Fatalf("unknown HStack case %q", test.Name)
			}

			if got := stack.Render(test.Width); !reflect.DeepEqual(got, test.Out) {
				t.Errorf("Render(%d) = %q, reference = %q", test.Width, got, test.Out)
			}
			for id, expected := range test.RenderWidths {
				if got := probes[id].renderWidths; !reflect.DeepEqual(got, expected) {
					t.Errorf("%s render widths = %v, reference = %v", id, got, expected)
				}
			}
		})
	}
}

func TestCompositeTuiLineMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Composite {
		got := CompositeTuiLine(test.Base, test.Overlay, test.StartCol, test.OverlayWidth, test.TotalWidth)
		if got != test.Out {
			t.Errorf("CompositeTuiLine(%q, %q, %d, %d, %d) = %q, reference = %q",
				test.Base, test.Overlay, test.StartCol, test.OverlayWidth, test.TotalWidth, got, test.Out)
		}
	}
}

func snapshotScrollState(label string, view *ScrollView, renderRequests int, unconsumed ...int) scrollStateReference {
	state := scrollStateReference{
		Label:            label,
		ScrollTop:        view.ScrollTop(),
		FollowingEnd:     view.IsFollowingEnd(),
		ViewportHeight:   view.ViewportHeight(),
		Scrollbar:        view.Scrollbar(),
		ScrollbarVisible: view.IsScrollbarVisible(),
		ScrollbarActive:  view.IsScrollbarActive(),
		ContentWidth1:    view.ContentWidth(1),
		ContentWidth5:    view.ContentWidth(5),
		RenderRequests:   renderRequests,
	}
	if len(unconsumed) > 0 {
		value := unconsumed[0]
		state.Unconsumed = &value
	}
	return state
}

func TestScrollViewStateMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.ScrollTraces {
		t.Run(test.Name, func(t *testing.T) {
			renders := 0
			requestRender := func() { renders++ }
			states := make([]scrollStateReference, 0)
			switch test.Name {
			case "follow-end":
				view := NewScrollView(fixedProbe("follow-child", []string{}), ScrollViewOptions{FollowEnd: true})
				states = append(states, snapshotScrollState("initial", view, renders))
				view.UpdateLayout(10, 3, requestRender)
				states = append(states, snapshotScrollState("layout-10-3", view, renders))
				unconsumed := view.ScrollBy(-2)
				states = append(states, snapshotScrollState("scroll-up-2", view, renders, unconsumed))
				view.UpdateLayout(12, 3, requestRender)
				states = append(states, snapshotScrollState("grow-12-3", view, renders))
				view.ScrollToEnd()
				states = append(states, snapshotScrollState("to-end", view, renders))
				view.UpdateLayout(15, 3, requestRender)
				states = append(states, snapshotScrollState("grow-15-3", view, renders))
			case "suppressed-follow":
				view := NewScrollView(fixedProbe("suppressed-child", []string{}), ScrollViewOptions{FollowEnd: true})
				view.UpdateLayout(10, 3, requestRender)
				states = append(states, snapshotScrollState("at-end", view, renders))
				view.ScrollTo(7, ScrollToOptions{DisableFollow: true})
				states = append(states, snapshotScrollState("suppress-at-end", view, renders))
				view.UpdateLayout(12, 3, requestRender)
				states = append(states, snapshotScrollState("growth-stays-put", view, renders))
				view.ScrollTo(9)
				states = append(states, snapshotScrollState("explicit-end-restores-follow", view, renders))
			case "bounds-and-shrink":
				view := NewScrollView(fixedProbe("bounds-child", []string{}), ScrollViewOptions{})
				view.UpdateLayout(5, 2, requestRender)
				states = append(states, snapshotScrollState("initial", view, renders))
				unconsumed := view.ScrollBy(-4)
				states = append(states, snapshotScrollState("past-start", view, renders, unconsumed))
				unconsumed = view.ScrollBy(10)
				states = append(states, snapshotScrollState("past-end", view, renders, unconsumed))
				view.UpdateLayout(1, 2, requestRender)
				states = append(states, snapshotScrollState("content-shrinks", view, renders))
			case "scrollbar-modes":
				delay := time.Minute
				view := NewScrollView(fixedProbe("auto-child", []string{}), ScrollViewOptions{
					Scrollbar: ScrollbarAuto, ScrollbarHideDelay: &delay,
				})
				view.UpdateLayout(8, 3, requestRender)
				states = append(states, snapshotScrollState("initial", view, renders))
				view.ScrollBy(1)
				states = append(states, snapshotScrollState("activity", view, renders))
				view.SetScrollbarActive(true)
				states = append(states, snapshotScrollState("active", view, renders))
				view.SetScrollbar(ScrollbarAlways)
				states = append(states, snapshotScrollState("always", view, renders))
				view.SetScrollbar(ScrollbarHidden)
				states = append(states, snapshotScrollState("hidden", view, renders))
			default:
				t.Fatalf("unknown ScrollView trace %q", test.Name)
			}
			if !reflect.DeepEqual(states, test.States) {
				t.Errorf("states = %#v\nreference = %#v", states, test.States)
			}
		})
	}
}

func TestScrollViewRejectsChildMutation(t *testing.T) {
	view := NewScrollView(fixedProbe("child", []string{}), ScrollViewOptions{})
	assertPanicMessage(t, "ScrollView has exactly one child", func() { view.AddChild(fixedProbe("other", []string{})) })
	assertPanicMessage(t, "ScrollView child cannot be removed", func() { view.RemoveChild(view.child) })
	assertPanicMessage(t, "ScrollView child cannot be cleared", view.Clear)
}

func assertPanicMessage(t *testing.T, expected string, action func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != expected {
			t.Fatalf("panic = %#v, want %q", recovered, expected)
		}
	}()
	action()
}

func serializeLayoutBoxForTest(box *LayoutBox, scrollNames map[*ScrollView]string) serializedLayoutBox {
	lines := []string(nil)
	if box.HasLines {
		lines = append([]string(nil), box.Lines...)
		if lines == nil {
			lines = []string{}
		}
	}
	children := make([]serializedLayoutBox, len(box.Children))
	for index, child := range box.Children {
		children[index] = serializeLayoutBoxForTest(child, scrollNames)
	}
	return serializedLayoutBox{
		ID:           componentID(box.Component),
		Rect:         box.Rect,
		Clip:         box.Clip,
		LineOffset:   box.LineOffset,
		Lines:        lines,
		ScrollViewID: scrollNames[box.ScrollView],
		Layer:        box.Layer,
		Children:     children,
	}
}

func TestRenderLayoutFrameMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Layouts {
		t.Run(test.Name, func(t *testing.T) {
			var root Component
			requestedWidth, requestedHeight := test.Width, test.Height
			scrollNames := map[*ScrollView]string{}
			var hitX, hitY int
			checkHits := test.Hits != nil
			checkScrollViews := test.ScrollViewsAt != nil
			var geometryView *ScrollView
			var renderProbe *probeComponent
			switch test.Name {
			case "leaf-cursor-clipping":
				root = fixedProbe("cursor-leaf", []string{"zero", "one", "two", "three" + CursorMarker})
				hitX, hitY = 0, 1
			case "vstack-allocation":
				one := 1
				root = &namedVStack{
					VStack: NewVStackWithOptions(StackOptions{Gap: 1},
						NewStackChild(fixedProbe("v-a", []string{"a0", "a1", "a2"}), StackEntryOptions{Basis: &one}),
						NewStackChild(fixedProbe("v-b", []string{"b0"}), StackEntryOptions{Grow: &one}),
					),
					id: "v-root",
				}
				hitX, hitY = 0, 2
			case "hstack-alignment":
				three, zero, one := 3, 0, 1
				root = &namedHStack{
					HStack: NewHStackWithOptions(StackOptions{Gap: 1, Align: AlignCenter},
						NewStackChild(fixedProbe("h-a", []string{"a0", "a1", "a2"}), StackEntryOptions{Basis: &three, Shrink: &zero}),
						NewStackChild(fixedProbe("h-b", []string{"b0"}), StackEntryOptions{Grow: &one}),
					),
					id: "h-root",
				}
				hitX, hitY = 4, 1
			case "scrollbar-always":
				view := NewScrollView(fixedProbe("scroll-child", []string{"row0", "row1", "row2", "row3", "row4", "row5"}), ScrollViewOptions{
					Scrollbar: ScrollbarAlways, Primary: true,
				})
				root = &namedScrollView{ScrollView: view, id: "scroll-always"}
				scrollNames[view] = "scroll-always"
				geometryView = view
				hitX, hitY = 1, 1
			case "scroll-offset":
				view := NewScrollView(fixedProbe("offset-child", []string{"row0", "row1", "row2", "row3", "row4"}), ScrollViewOptions{})
				root = &namedScrollView{ScrollView: view, id: "scroll-offset"}
				scrollNames[view] = "scroll-offset"
				RenderLayoutFrame(root, 6, 2, func() {})
				view.ScrollBy(2)
				hitX, hitY = 0, 0
			case "strip-leading-osc133":
				root = fixedProbe("zone-leaf", []string{"\x1b]133;A\x07\x1b]133;B\x1b\\visible"})
			case "render-cache":
				renderProbe = fixedProbe("shared", []string{"same"})
				root = &namedVStack{VStack: NewVStack(renderProbe, renderProbe), id: "cache-root"}
			case "viewport-clamp":
				root = fixedProbe("clamp-leaf", []string{"over"})
				requestedWidth, requestedHeight = -2, 0
			case "kitty-image-crop":
				RegisterKittyImageMetadata(KittyImageMetadata{ImageID: 42, Columns: 4, Rows: 3, WidthPx: 40, HeightPx: 30})
				root = fixedProbe("image-leaf", []string{"\x1b_Ga=T,i=42,c=4,r=3;AAAA\x1b\\"})
			default:
				t.Fatalf("unknown layout case %q", test.Name)
			}

			frame := RenderLayoutFrame(root, requestedWidth, requestedHeight, func() {})
			got := layoutCaseReference{
				Name: test.Name, Width: frame.Width, Height: frame.Height, Lines: frame.Lines,
				PrimaryScrollViewID: scrollNames[frame.PrimaryScrollView],
				Root:                serializeLayoutBoxForTest(frame.Root, scrollNames),
			}
			if checkHits {
				for _, box := range GetLayoutBoxesAt(frame, hitX, hitY) {
					got.Hits = append(got.Hits, componentID(box.Component))
				}
			}
			if geometryView != nil {
				box, ok := GetScrollViewBox(frame, geometryView)
				if !ok {
					t.Fatal("scroll view box not found")
				}
				if geometry, ok := GetScrollbarGeometry(box, false); ok {
					got.Geometry = &geometry
				}
			}
			if checkScrollViews {
				for _, view := range GetScrollViewsAt(frame, hitX, hitY) {
					got.ScrollViewsAt = append(got.ScrollViewsAt, scrollNames[view])
				}
			}
			if test.ScrollTop != nil {
				value := frame.PrimaryScrollView.ScrollTop()
				got.ScrollTop = &value
			}
			if test.RenderWidths != nil {
				got.RenderWidths = append([]int(nil), renderProbe.renderWidths...)
			}

			if !reflect.DeepEqual(got, test) {
				t.Errorf("layout = %#v\nreference = %#v", got, test)
			}
		})
	}
}

func TestTerminalColorParsingMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.TerminalColorInputs {
		if got := IsOSC11BackgroundColorResponse(test.Data); got != test.IsOSC11 {
			t.Errorf("IsOSC11BackgroundColorResponse(%q) = %v, reference = %v", test.Data, got, test.IsOSC11)
		}
		color, found := ParseOSC11BackgroundColor(test.Data)
		if found != test.ColorFound || color != test.Color {
			t.Errorf("ParseOSC11BackgroundColor(%q) = (%#v, %v), reference = (%#v, %v)",
				test.Data, color, found, test.Color, test.ColorFound)
		}
		scheme, found := ParseTerminalColorSchemeReport(test.Data)
		if found != test.SchemeFound || scheme != test.Scheme {
			t.Errorf("ParseTerminalColorSchemeReport(%q) = (%q, %v), reference = (%q, %v)",
				test.Data, scheme, found, test.Scheme, test.SchemeFound)
		}
	}
}

func TestCoreKeyRecognitionMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.KeyInputs {
		if got := IsKeyRelease(test.Data); got != test.Release {
			t.Errorf("IsKeyRelease(%q) = %v, reference = %v", test.Data, got, test.Release)
		}
		if got := MatchesDebugKey(test.Data); got != test.Debug {
			t.Errorf("MatchesDebugKey(%q) = %v, reference = %v", test.Data, got, test.Debug)
		}
	}
}

type fakeTerminal struct {
	columns  int
	rows     int
	kitty    bool
	log      []string
	onInput  func(string)
	onResize func()
}

func newFakeTerminal(columns, rows int) *fakeTerminal {
	return &fakeTerminal{columns: columns, rows: rows}
}

func (f *fakeTerminal) Start(onInput func(string), onResize func()) {
	f.onInput, f.onResize = onInput, onResize
	f.log = append(f.log, "start")
}
func (f *fakeTerminal) Stop()                                       { f.log = append(f.log, "stop") }
func (*fakeTerminal) DrainInput(time.Duration, time.Duration) error { return nil }
func (f *fakeTerminal) Write(data string)                           { f.log = append(f.log, "write:"+data) }
func (f *fakeTerminal) Columns() int                                { return f.columns }
func (f *fakeTerminal) Rows() int                                   { return f.rows }
func (f *fakeTerminal) KittyProtocolActive() bool                   { return f.kitty }
func (f *fakeTerminal) MoveBy(lines int)                            { f.log = append(f.log, "move:"+itoa(lines)) }
func (f *fakeTerminal) HideCursor()                                 { f.log = append(f.log, "hideCursor") }
func (f *fakeTerminal) ShowCursor()                                 { f.log = append(f.log, "showCursor") }
func (f *fakeTerminal) ClearLine()                                  { f.log = append(f.log, "clearLine") }
func (f *fakeTerminal) ClearFromCursor()                            { f.log = append(f.log, "clearFromCursor") }
func (f *fakeTerminal) ClearScreen()                                { f.log = append(f.log, "clearScreen") }
func (f *fakeTerminal) SetTitle(title string)                       { f.log = append(f.log, "title:"+title) }
func (f *fakeTerminal) SetProgress(active bool) {
	if active {
		f.log = append(f.log, "progress:true")
	} else {
		f.log = append(f.log, "progress:false")
	}
}

type fakeScheduledCallback struct {
	at        time.Duration
	callback  func()
	cancelled bool
}

type fakeScheduler struct {
	now      time.Duration
	deferred []func()
	timers   []*fakeScheduledCallback
}

func (f *fakeScheduler) Now() time.Duration    { return f.now }
func (f *fakeScheduler) Defer(callback func()) { f.deferred = append(f.deferred, callback) }
func (f *fakeScheduler) AfterFunc(delay time.Duration, callback func()) CancelFunc {
	scheduled := &fakeScheduledCallback{at: f.now + delay, callback: callback}
	f.timers = append(f.timers, scheduled)
	return func() { scheduled.cancelled = true }
}

func (f *fakeScheduler) runDeferred() {
	for len(f.deferred) > 0 {
		callbacks := f.deferred
		f.deferred = nil
		for _, callback := range callbacks {
			callback()
		}
	}
}

func (f *fakeScheduler) advanceTo(target time.Duration) {
	for {
		var next *fakeScheduledCallback
		for _, timer := range f.timers {
			if timer.cancelled || timer.at > target {
				continue
			}
			if next == nil || timer.at < next.at {
				next = timer
			}
		}
		if next == nil {
			f.now = target
			return
		}
		f.now = next.at
		next.cancelled = true
		next.callback()
		f.runDeferred()
	}
}

func newTestBase(terminal *fakeTerminal) (*TuiBase, *fakeScheduler) {
	scheduler := &fakeScheduler{}
	return NewTuiBase(terminal, ModeRegular, BaseOptions{Scheduler: scheduler}), scheduler
}

func TestOverlayCompositionMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Overlays {
		t.Run(test.Name, func(t *testing.T) {
			termWidth, termHeight := 20, 10
			baseLines := []string{"base"}
			var overlayLines []string
			options := OverlayOptions{}
			hidden := false
			switch test.Name {
			case "default-center":
				overlayLines = []string{"one", "two"}
			case "top-left-margin":
				overlayLines = []string{"x"}
				width := Cells(8)
				options = OverlayOptions{Width: &width, Anchor: AnchorTopLeft, Margin: &OverlayMargin{Top: 1, Left: 2, Right: 3, Bottom: 1}}
			case "percent-and-max-height":
				termWidth, termHeight = 30, 10
				overlayLines = []string{"0", "1", "2", "3", "4", "5", "6", "7"}
				width, maxHeight, row, col := Percent("50%"), Percent("50%"), Percent("25%"), Percent("100%")
				options = OverlayOptions{Width: &width, MaxHeight: &maxHeight, Row: &row, Col: &col}
			case "invalid-percent-centers":
				overlayLines = []string{"x", "y"}
				width, row, col := Percent("bad"), Percent("bad"), Percent("bad")
				options = OverlayOptions{Width: &width, Row: &row, Col: &col}
			case "offsets-clamp":
				overlayLines = []string{"x", "y", "z"}
				width, offset := Cells(6), 50
				options = OverlayOptions{Width: &width, Anchor: AnchorBottomRight, OffsetX: &offset, OffsetY: &offset, Margin: UniformMargin(-3)}
			case "empty":
				overlayLines = []string{}
			case "long-base-bottom":
				termWidth, termHeight = 12, 5
				baseLines = make([]string, 12)
				for index := range baseLines {
					baseLines[index] = "base" + itoa(index)
				}
				overlayLines = []string{"bottom"}
				width := Cells(8)
				options = OverlayOptions{Width: &width, Anchor: AnchorBottomLeft}
			case "defensive-truncate":
				termWidth, termHeight = 10, 4
				overlayLines = []string{"over-wide-overlay-line"}
				width := Cells(5)
				options = OverlayOptions{Width: &width, Anchor: AnchorTopLeft}
			case "hidden":
				overlayLines, hidden = []string{"hidden"}, true
			case "dynamic-invisible":
				overlayLines = []string{"hidden"}
				options.Visible = func(width, _ int) bool { return width < 10 }
			default:
				t.Fatalf("unknown overlay case %q", test.Name)
			}

			terminal := newFakeTerminal(termWidth, termHeight)
			base, _ := newTestBase(terminal)
			overlay := newFocusProbe(test.Name+"-overlay", overlayLines)
			handle := base.ShowOverlay(overlay, options)
			if hidden {
				handle.SetHidden(true)
			}
			out := base.CompositeOverlays(baseLines, termWidth, termHeight)
			bounds, found := handle.Bounds()
			if !reflect.DeepEqual(out, test.Out) || found != test.BoundsFound || bounds != test.Bounds ||
				componentID(base.FocusedComponent()) != test.FocusID || overlay.IsFocused() != test.Focused ||
				!reflect.DeepEqual(overlay.renderWidths, test.RenderWidths) {
				t.Errorf("overlay mismatch:\nout=%q\nbounds=(%#v,%v) focus=%q/%v widths=%v\nreference out=%q bounds=(%#v,%v) focus=%q/%v widths=%v",
					out, bounds, found, componentID(base.FocusedComponent()), overlay.IsFocused(), overlay.renderWidths,
					test.Out, test.Bounds, test.BoundsFound, test.FocusID, test.Focused, test.RenderWidths)
			}
		})
	}
}

func snapshotFocusState(label string, base *TuiBase, probes map[string]*focusProbe, handles map[string]*OverlayHandle) focusStateReference {
	focused := make(map[string]bool, len(probes))
	for id, probe := range probes {
		focused[id] = probe.IsFocused()
	}
	hidden := make(map[string]bool, len(handles))
	for id, handle := range handles {
		hidden[id] = handle.IsHidden()
	}
	return focusStateReference{
		Label: label, FocusID: componentID(base.FocusedComponent()), Focused: focused,
		HasOverlay: base.HasOverlay(), Hidden: hidden,
	}
}

func TestOverlayFocusStateMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.FocusTraces {
		t.Run(test.Name, func(t *testing.T) {
			base, _ := newTestBase(newFakeTerminal(20, 10))
			states := make([]focusStateReference, 0)
			switch test.Name {
			case "stack-and-noncapturing":
				root, one, two := newFocusProbe("base", []string{"base"}), newFocusProbe("one", []string{"one"}), newFocusProbe("two", []string{"two"})
				probes := map[string]*focusProbe{"base": root, "one": one, "two": two}
				base.AddChild(root)
				base.SetFocus(root)
				states = append(states, snapshotFocusState("base", base, probes, map[string]*OverlayHandle{}))
				oneHandle := base.ShowOverlay(one, OverlayOptions{})
				states = append(states, snapshotFocusState("show-one", base, probes, map[string]*OverlayHandle{"one": oneHandle}))
				twoHandle := base.ShowOverlay(two, OverlayOptions{NonCapturing: true})
				states = append(states, snapshotFocusState("show-noncapturing-two", base, probes, map[string]*OverlayHandle{"one": oneHandle, "two": twoHandle}))
				twoHandle.Focus()
				states = append(states, snapshotFocusState("explicit-focus-two", base, probes, map[string]*OverlayHandle{"one": oneHandle, "two": twoHandle}))
				twoHandle.Hide()
				states = append(states, snapshotFocusState("hide-two", base, probes, map[string]*OverlayHandle{"one": oneHandle}))
				base.HideOverlay()
				states = append(states, snapshotFocusState("hide-last", base, probes, map[string]*OverlayHandle{}))
			case "blocked-restore":
				root, blocker, overlay := newFocusProbe("base", []string{"base"}), newFocusProbe("blocker", []string{"blocker"}), newFocusProbe("overlay", []string{"overlay"})
				probes := map[string]*focusProbe{"base": root, "blocker": blocker, "overlay": overlay}
				base.AddChild(root)
				base.AddChild(blocker)
				base.SetFocus(root)
				handle := base.ShowOverlay(overlay, OverlayOptions{})
				states = append(states, snapshotFocusState("overlay", base, probes, map[string]*OverlayHandle{}))
				base.SetFocus(blocker)
				states = append(states, snapshotFocusState("blocked", base, probes, map[string]*OverlayHandle{}))
				base.SetFocus(nil)
				states = append(states, snapshotFocusState("nil-restores", base, probes, map[string]*OverlayHandle{}))
				base.SetFocus(blocker)
				handle.Unfocus(root)
				states = append(states, snapshotFocusState("explicit-target-pending", base, probes, map[string]*OverlayHandle{}))
				base.SetFocus(nil)
				states = append(states, snapshotFocusState("explicit-target-resolves", base, probes, map[string]*OverlayHandle{}))
			case "dynamic-visibility":
				visible := true
				root, overlay := newFocusProbe("base", []string{"base"}), newFocusProbe("overlay", []string{"overlay"})
				probes := map[string]*focusProbe{"base": root, "overlay": overlay}
				base.AddChild(root)
				base.SetFocus(root)
				handle := base.ShowOverlay(overlay, OverlayOptions{Visible: func(_, _ int) bool { return visible }})
				states = append(states, snapshotFocusState("visible", base, probes, map[string]*OverlayHandle{"overlay": handle}))
				visible = false
				base.handleTerminalInput("x")
				states = append(states, snapshotFocusState("input-repairs-hidden", base, probes, map[string]*OverlayHandle{"overlay": handle}))
				visible = true
				base.handleTerminalInput("y")
				states = append(states, snapshotFocusState("input-restores-visible", base, probes, map[string]*OverlayHandle{"overlay": handle}))
			default:
				t.Fatalf("unknown focus trace %q", test.Name)
			}
			if !reflect.DeepEqual(states, test.States) {
				t.Errorf("states = %#v\nreference = %#v", states, test.States)
			}
		})
	}
}

func TestCursorAndLineResetsMatchReference(t *testing.T) {
	reference := loadCoreReference(t)
	base, _ := newTestBase(newFakeTerminal(20, 10))
	for _, test := range reference.CursorAndResets {
		lines := []string{"old" + CursorMarker, "middle", "\x1b[31mnew" + CursorMarker + "\x1b[0m"}
		cursor, found := base.ExtractCursorPosition(lines, 2)
		resetLines := base.ApplyLineResets([]string{"a\tb", "\x1b_Ga=T;AAAA\x1b\\"})
		if found != test.CursorFound || cursor != test.Cursor || !reflect.DeepEqual(lines, test.Lines) || !reflect.DeepEqual(resetLines, test.ResetLines) {
			t.Errorf("cursor/reset = (%#v,%v,%q,%q), reference = (%#v,%v,%q,%q)",
				cursor, found, lines, resetLines, test.Cursor, test.CursorFound, test.Lines, test.ResetLines)
		}
	}
}

func stringResult(value string) *string { return &value }

func TestTuiBaseInputPriorityMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.BaseInputTraces {
		t.Run(test.Name, func(t *testing.T) {
			SetCellDimensions(CellDimensions{WidthPx: 9, HeightPx: 18})
			terminal := newFakeTerminal(20, 10)
			base, _ := newTestBase(terminal)
			focused := newFocusProbe("focused", []string{"focused"})
			base.AddChild(focused)
			base.SetFocus(focused)
			listenerLog := make([]string, 0)
			base.AddInputListener(func(data string) InputListenerResult {
				listenerLog = append(listenerLog, "one:"+data)
				if data == "empty" {
					return InputListenerResult{Data: stringResult("")}
				}
				return InputListenerResult{Data: stringResult(data + "-one")}
			})
			base.AddInputListener(func(data string) InputListenerResult {
				listenerLog = append(listenerLog, "two:"+data)
				if strings.HasPrefix(data, "consume") {
					return InputListenerResult{Consume: true}
				}
				return InputListenerResult{Data: stringResult(data + "-two")}
			})
			debugCount := 0
			base.OnDebug = func() { debugCount++ }
			schemes := make([]TerminalColorScheme, 0)
			base.OnTerminalColorSchemeChange(func(scheme TerminalColorScheme) { schemes = append(schemes, scheme) })

			base.handleTerminalInput("x")
			base.handleTerminalInput("empty")
			base.handleTerminalInput("consume")
			base.handleTerminalInput("\x1b[97;1:3u")
			focused.wantsRelease = true
			base.handleTerminalInput("\x1b[97;1:3u")
			base.handleTerminalInput("\x1b[100;6u")
			base.handleTerminalInput("\x1b[?997;2n")
			base.handleTerminalInput("\x1b[6;18;9t")

			backgroundResult := base.QueryTerminalBackgroundColor(time.Second)
			base.handleTerminalInput("\x1b]11;rgb:ffff/0000/8000\x07")
			background := <-backgroundResult
			schemeResult := base.QueryTerminalColorScheme(time.Second)
			base.handleTerminalInput("\x1b[?997;1n")
			queriedScheme := <-schemeResult

			if !reflect.DeepEqual(listenerLog, test.ListenerLog) ||
				!reflect.DeepEqual(focused.inputs, test.Inputs) ||
				focused.invalidations != test.Invalidations || debugCount != test.DebugCount ||
				!reflect.DeepEqual(schemes, test.Schemes) || GetCellDimensions() != test.CellDimensions ||
				background.OK != test.BackgroundFound || background.Color != test.Background ||
				queriedScheme.OK != test.QueriedSchemeFound || queriedScheme.Scheme != test.QueriedScheme ||
				!reflect.DeepEqual(terminal.log, test.TerminalLog) {
				t.Errorf("base input mismatch:\nlisteners=%q inputs=%q invalidations=%d debug=%d schemes=%v dims=%#v bg=%#v scheme=%#v terminal=%q\nreference=%#v",
					listenerLog, focused.inputs, focused.invalidations, debugCount, schemes, GetCellDimensions(), background, queriedScheme, terminal.log, test)
			}
		})
	}
}

func TestTuiBaseLifecycleMatchesReference(t *testing.T) {
	reference := loadCoreReference(t)
	for _, test := range reference.Lifecycle {
		t.Run(test.Name, func(t *testing.T) {
			SetCapabilities(TerminalCapabilities{Images: ImageKitty, TrueColor: true, Hyperlinks: true})
			defer SetCapabilities(TerminalCapabilities{})
			terminal := newFakeTerminal(20, 10)
			scheduler := &fakeScheduler{}
			renders := 0
			hooks := TuiHooks{
				BeforeTerminalStart: func() { terminal.log = append(terminal.log, "hook:beforeStart") },
				AfterTerminalStart:  func() { terminal.log = append(terminal.log, "hook:afterStart") },
				BeforeTerminalStop: func(options StopOptions) {
					terminal.log = append(terminal.log, "hook:beforeStop:"+boolString(options.PreserveScreen))
				},
				AfterTerminalStop: func(options StopOptions) {
					terminal.log = append(terminal.log, "hook:afterStop:"+boolString(options.PreserveScreen))
				},
				ResetRenderState: func() { terminal.log = append(terminal.log, "hook:reset") },
				Render: func() {
					terminal.log = append(terminal.log, "hook:render")
					renders++
				},
			}
			base := NewTuiBase(terminal, ModeRegular, BaseOptions{Scheduler: scheduler, Hooks: hooks})
			base.SetTerminalColorSchemeNotifications(true)
			base.Start()
			base.RenderNow(true)
			base.Stop(StopOptions{PreserveScreen: true})
			if !reflect.DeepEqual(terminal.log, test.Log) || renders != test.Renders {
				t.Errorf("lifecycle = (%q, %d), reference = (%q, %d)", terminal.log, renders, test.Log, test.Renders)
			}
		})
	}
}

func TestRenderSchedulerCoalescesAndPreempts(t *testing.T) {
	terminal := newFakeTerminal(20, 10)
	scheduler := &fakeScheduler{}
	renders, resets := 0, 0
	base := NewTuiBase(terminal, ModeRegular, BaseOptions{
		Scheduler: scheduler,
		Hooks: TuiHooks{
			Render:           func() { renders++ },
			ResetRenderState: func() { resets++ },
		},
	})

	base.RequestRender(false)
	base.RequestRender(false)
	if len(scheduler.deferred) != 1 {
		t.Fatalf("coalesced deferred callbacks = %d, want 1", len(scheduler.deferred))
	}
	scheduler.runDeferred()
	if renders != 0 {
		t.Fatalf("rendered before throttle: %d", renders)
	}
	scheduler.advanceTo(15 * time.Millisecond)
	if renders != 0 {
		t.Fatalf("rendered at 15ms: %d", renders)
	}
	scheduler.advanceTo(16 * time.Millisecond)
	if renders != 1 {
		t.Fatalf("renders at 16ms = %d, want 1", renders)
	}

	base.RequestRender(false)
	scheduler.runDeferred()
	scheduler.advanceTo(20 * time.Millisecond)
	base.RequestRender(true)
	scheduler.runDeferred()
	if renders != 2 || resets != 1 {
		t.Fatalf("forced render = renders %d resets %d, want 2/1", renders, resets)
	}
	scheduler.advanceTo(32 * time.Millisecond)
	if renders != 2 {
		t.Fatalf("cancelled throttled frame still rendered: %d", renders)
	}
}

func TestKeyboardInputPreemptsThrottledRender(t *testing.T) {
	terminal := newFakeTerminal(20, 10)
	scheduler := &fakeScheduler{}
	renders := 0
	base := NewTuiBase(terminal, ModeRegular, BaseOptions{
		Scheduler: scheduler,
		Hooks:     TuiHooks{Render: func() { renders++ }},
	})
	focused := newFocusProbe("focused", []string{"x"})
	base.SetFocus(focused)
	base.RenderNow(false)
	base.RequestRender(false)
	scheduler.runDeferred()
	scheduler.advanceTo(5 * time.Millisecond)
	base.handleTerminalInput("a")
	scheduler.runDeferred()
	if renders != 2 || !reflect.DeepEqual(focused.inputs, []string{"a"}) {
		t.Fatalf("keyboard preemption = renders %d inputs %q", renders, focused.inputs)
	}
	scheduler.advanceTo(16 * time.Millisecond)
	if renders != 2 {
		t.Fatalf("cancelled timer rendered after input: %d", renders)
	}
}

func TestBackgroundQueryTimeoutPreservesReplyOrdering(t *testing.T) {
	terminal := newFakeTerminal(20, 10)
	base, scheduler := newTestBase(terminal)
	first := base.QueryTerminalBackgroundColor(10 * time.Millisecond)
	scheduler.advanceTo(10 * time.Millisecond)
	if result := <-first; result.OK {
		t.Fatalf("timed out query unexpectedly succeeded: %#v", result)
	}
	second := base.QueryTerminalBackgroundColor(time.Second)
	// A late reply belongs to the timed-out first query and must not resolve the
	// second query out of order.
	base.handleTerminalInput("\x1b]11;#010203\x07")
	select {
	case result := <-second:
		t.Fatalf("late first reply resolved second query: %#v", result)
	default:
	}
	base.handleTerminalInput("\x1b]11;#040506\x07")
	result := <-second
	if !result.OK || result.Color != (RGBColor{R: 4, G: 5, B: 6}) {
		t.Fatalf("second query result = %#v", result)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestRetargetMouseEvent(t *testing.T) {
	event := MouseEvent{ScreenX: 20, ScreenY: 9, X: 99, Y: 99, Width: 1, Height: 1}
	target := MouseDispatchTarget{OriginX: 12, OriginY: 5, Width: 8, Height: 3}
	got := RetargetMouseEvent(event, target)
	if got.X != 8 || got.Y != 4 || got.Width != 8 || got.Height != 3 {
		t.Fatalf("RetargetMouseEvent() = %#v", got)
	}
}

// itoa avoids formatting dependencies in the tiny deterministic probe.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}
