package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

type altScreenReference struct {
	Scenarios []altScreenScenario `json:"scenarios"`
}

type altScreenScenario struct {
	Name  string          `json:"name"`
	Steps []altScreenStep `json:"steps"`
}

type altScreenStep struct {
	Label string         `json:"label"`
	Log   []string       `json:"log"`
	State altScreenState `json:"state"`
}

type altScreenState struct {
	PreviousScreen         []string              `json:"previousScreen"`
	LastDocument           []string              `json:"lastDocument"`
	PreviousScreenWidth    int                   `json:"previousScreenWidth"`
	PreviousScreenHeight   int                   `json:"previousScreenHeight"`
	ViewportTop            int                   `json:"viewportTop"`
	FollowingOutput        bool                  `json:"followingOutput"`
	FullRedraws            int                   `json:"fullRedraws"`
	AltScreenActive        bool                  `json:"altScreenActive"`
	HasCurrentLayout       bool                  `json:"hasCurrentLayout"`
	UploadedKittyImageIDs  []int                 `json:"uploadedKittyImageIds"`
	ImageProtocol          string                `json:"imageProtocol"`
	CapabilityImages       string                `json:"capabilityImages"`
	Invalidations          int                   `json:"invalidations"`
	Inputs                 []string              `json:"inputs"`
	OverlayInputs          []string              `json:"overlayInputs"`
	Search                 *altScreenSearchState `json:"search"`
	MouseEvents            []altMouseEventState  `json:"mouseEvents"`
	PrimaryScrollbarActive bool                  `json:"primaryScrollbarActive"`
	HasScrollbarDrag       bool                  `json:"hasScrollbarDrag"`
	HasScrollbarHover      bool                  `json:"hasScrollbarHover"`
	Selection              *altSelectionState    `json:"selection"`
	SelectionText          *string               `json:"selectionText"`
	SelectionGranularity   string                `json:"selectionGranularity"`
	SelectionPressActive   bool                  `json:"selectionPressActive"`
	SelectionDragged       bool                  `json:"selectionDragged"`
	FlashLines             []string              `json:"flashLines"`
	OpenedURLs             []string              `json:"openedUrls"`
	CopiedTexts            []string              `json:"copiedTexts"`
}

type altSelectionPointState struct {
	Row      int  `json:"row"`
	Col      int  `json:"col"`
	Boundary bool `json:"boundary"`
	Scroll   bool `json:"scroll"`
}

type altSelectionState struct {
	Start altSelectionPointState `json:"start"`
	End   altSelectionPointState `json:"end"`
}

type altMouseEventState struct {
	Type       MouseEventType `json:"type"`
	Button     MouseButton    `json:"button"`
	X          int            `json:"x"`
	Y          int            `json:"y"`
	ScreenX    int            `json:"screenX"`
	ScreenY    int            `json:"screenY"`
	Width      int            `json:"width"`
	Height     int            `json:"height"`
	Shift      bool           `json:"shift"`
	Alt        bool           `json:"alt"`
	Ctrl       bool           `json:"ctrl"`
	WheelDelta *int           `json:"wheelDelta,omitempty"`
	ClickCount *int           `json:"clickCount,omitempty"`
}

type altScreenSearchState struct {
	Query          string                 `json:"query"`
	Matches        []AltScreenSearchMatch `json:"matches"`
	SelectedIndex  int                    `json:"selectedIndex"`
	SelectedKey    *string                `json:"selectedKey"`
	AnchorRow      int                    `json:"anchorRow"`
	SelectionMode  string                 `json:"selectionMode"`
	ResultIndex    int                    `json:"resultIndex"`
	ResultCount    int                    `json:"resultCount"`
	OverlayFocused bool                   `json:"overlayFocused"`
	OverlayBounds  *OverlayBounds         `json:"overlayBounds"`
}

type mutableAltComponent struct {
	lines         []string
	invalidations int
	capability    bool
	inputs        []string
	mouseHandling bool
	mouseEvents   []altMouseEventState
}

func (m *mutableAltComponent) Render(int) []string {
	if m.capability {
		if GetCapabilities().Images != ImageNone {
			return []string{"image"}
		}
		return []string{"fallback"}
	}
	return append([]string(nil), m.lines...)
}

func (m *mutableAltComponent) Invalidate() { m.invalidations++ }

func (m *mutableAltComponent) HandleInput(data string) { m.inputs = append(m.inputs, data) }

func (m *mutableAltComponent) HandleMouse(event MouseEvent) *MouseResult {
	if !m.mouseHandling {
		return nil
	}
	m.mouseEvents = append(m.mouseEvents, altMouseEventState(event))
	return &MouseResult{Handled: true, Focus: event.Type == MousePress, Capture: event.Type == MousePress}
}

type altScreenRig struct {
	terminal    *fakeTerminal
	component   *mutableAltComponent
	overlay     *mutableAltComponent
	tui         *TuiAltScreen
	openedURLs  []string
	copiedTexts []string
}

func newAltScreenRig(lines []string, columns, rows int, showHardwareCursor, mouse, multiplexer bool, component ...*mutableAltComponent) *altScreenRig {
	return newAltScreenRigOptions(lines, columns, rows, showHardwareCursor, mouse, multiplexer, TuiAltScreenOptions{}, component...)
}

func newAltScreenRigOptions(lines []string, columns, rows int, showHardwareCursor, mouse, multiplexer bool, options TuiAltScreenOptions, component ...*mutableAltComponent) *altScreenRig {
	terminal := newFakeTerminal(columns, rows)
	document := &mutableAltComponent{lines: append([]string(nil), lines...)}
	if len(component) > 0 {
		document = component[0]
	}
	options.ShowHardwareCursor = showHardwareCursor
	options.Scheduler = &fakeScheduler{}
	options.Mouse = boolPointer(mouse)
	options.IsMultiplexer = func() bool { return multiplexer }
	tui := NewTuiAltScreen(terminal, options)
	tui.AddChild(document)
	return &altScreenRig{terminal: terminal, component: document, tui: tui}
}

func boolPointer(value bool) *bool { return &value }

func (r *altScreenRig) step(label string) altScreenStep {
	log := make([]string, len(r.terminal.log))
	copy(log, r.terminal.log)
	r.terminal.log = []string{}
	previousScreen := make([]string, len(r.tui.previousScreen))
	copy(previousScreen, r.tui.previousScreen)
	lastDocument := make([]string, len(r.tui.lastDocument))
	copy(lastDocument, r.tui.lastDocument)
	ids := make([]int, len(r.tui.uploadedKittyImages))
	for index, cached := range r.tui.uploadedKittyImages {
		ids[index] = cached.imageID
	}
	capabilities := GetCapabilities()
	inputs := make([]string, len(r.component.inputs))
	copy(inputs, r.component.inputs)
	overlayInputs := []string{}
	if r.overlay != nil {
		overlayInputs = make([]string, len(r.overlay.inputs))
		copy(overlayInputs, r.overlay.inputs)
	}
	var searchState *altScreenSearchState
	if search := r.tui.activeSearch; search != nil {
		matches := make([]AltScreenSearchMatch, len(search.matches))
		copy(matches, search.matches)
		var selectedKey *string
		if search.hasSelectedKey {
			key := search.selectedKey
			selectedKey = &key
		}
		var bounds *OverlayBounds
		overlayFocused := false
		if search.overlay != nil {
			overlayFocused = search.overlay.IsFocused()
			if value, ok := search.overlay.Bounds(); ok {
				bounds = &value
			}
		}
		searchState = &altScreenSearchState{
			Query: search.query, Matches: matches, SelectedIndex: search.selectedIndex, SelectedKey: selectedKey,
			AnchorRow: search.anchorRow, SelectionMode: string(search.selectionMode),
			ResultIndex: search.component.resultIndex, ResultCount: search.component.resultCount,
			OverlayFocused: overlayFocused, OverlayBounds: bounds,
		}
	}
	mouseEvents := make([]altMouseEventState, len(r.component.mouseEvents))
	copy(mouseEvents, r.component.mouseEvents)
	var selectionState *altSelectionState
	if selection, ok := r.tui.selectionBounds(); ok {
		point := func(value altSelectionPoint) altSelectionPointState {
			return altSelectionPointState{Row: value.row, Col: value.col, Boundary: value.boundary, Scroll: value.scrollView == r.tui.implicitScrollView}
		}
		selectionState = &altSelectionState{Start: point(selection.start), End: point(selection.end)}
	}
	var selectionText *string
	if text, ok := r.tui.GetActiveSelectionText(); ok {
		selectionText = &text
	}
	flashLines := r.tui.flashes.Render(r.terminal.columns)
	openedURLs := append([]string(nil), r.openedURLs...)
	if len(openedURLs) == 0 {
		openedURLs = []string{}
	}
	copiedTexts := append([]string(nil), r.copiedTexts...)
	if len(copiedTexts) == 0 {
		copiedTexts = []string{}
	}
	return altScreenStep{
		Label: label,
		Log:   log,
		State: altScreenState{
			PreviousScreen:         previousScreen,
			LastDocument:           lastDocument,
			PreviousScreenWidth:    r.tui.previousScreenWidth,
			PreviousScreenHeight:   r.tui.previousScreenHeight,
			ViewportTop:            r.tui.ViewportTop(),
			FollowingOutput:        r.tui.IsFollowingOutput(),
			FullRedraws:            r.tui.FullRedraws(),
			AltScreenActive:        r.tui.altScreenActive,
			HasCurrentLayout:       r.tui.currentLayout != nil,
			UploadedKittyImageIDs:  ids,
			ImageProtocol:          string(r.tui.imageProtocol),
			CapabilityImages:       string(capabilities.Images),
			Invalidations:          r.component.invalidations,
			Inputs:                 inputs,
			OverlayInputs:          overlayInputs,
			Search:                 searchState,
			MouseEvents:            mouseEvents,
			PrimaryScrollbarActive: r.tui.implicitScrollView.IsScrollbarActive(),
			HasScrollbarDrag:       r.tui.scrollbarDrag != nil,
			HasScrollbarHover:      r.tui.scrollbarHover != nil,
			Selection:              selectionState,
			SelectionText:          selectionText,
			SelectionGranularity:   string(r.tui.selectionGranularity),
			SelectionPressActive:   r.tui.selectionPressActive,
			SelectionDragged:       r.tui.selectionDragged,
			FlashLines:             flashLines,
			OpenedURLs:             openedURLs,
			CopiedTexts:            copiedTexts,
		},
	}
}

func loadAltScreenReference(t *testing.T) altScreenReference {
	t.Helper()
	data, err := os.ReadFile("testdata/alt-screen-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference altScreenReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Scenarios) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return reference
}

func TestAltScreenPaintingCoreMatchesReference(t *testing.T) {
	reference := loadAltScreenReference(t)
	actual := buildAltScreenScenarios()
	if len(actual) != len(reference.Scenarios) {
		t.Fatalf("scenario count = %d, reference = %d", len(actual), len(reference.Scenarios))
	}
	for index, want := range reference.Scenarios {
		got := actual[index]
		t.Run(want.Name, func(t *testing.T) {
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			wantJSON, _ := json.MarshalIndent(want, "", "  ")
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("alt-screen trace mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

func setTestCapabilities(images ImageProtocol) {
	SetCapabilities(TerminalCapabilities{Images: images, TrueColor: true, Hyperlinks: true})
}

func testKittyLine(id int, payload ...string) string {
	data := "AAAA"
	if len(payload) > 0 {
		data = payload[0]
	}
	return "\x1b_Ga=T,f=100,q=2,C=1,c=2,r=1,i=" + itoa(id) + ";" + data + "\x1b\\"
}

func registerTestKittyImage(id int) {
	RegisterKittyImageMetadata(KittyImageMetadata{
		ImageID: id, Columns: 2, Rows: 1, WidthPx: 20, HeightPx: 10,
	})
}

func buildAltScreenScenarios() []altScreenScenario {
	scenarios := make([]altScreenScenario, 0, 22)

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"one", "two"}, 8, 3, false, false, false)
		steps := make([]altScreenStep, 0, 3)
		rig.tui.Start()
		steps = append(steps, rig.step("start"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("first-frame"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		steps = append(steps, rig.step("stop-preserve"))
		scenarios = append(scenarios, altScreenScenario{Name: "lifecycle-preserve", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"\x1b]133;A\x07abcdef" + CursorMarker, "xy"}, 4, 2, false, false, false)
		steps := make([]altScreenStep, 0, 2)
		rig.tui.Start()
		rig.terminal.log = nil
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("frame"))
		rig.tui.Stop(StopOptions{})
		steps = append(steps, rig.step("stop-with-document"))
		scenarios = append(scenarios, altScreenScenario{Name: "lifecycle-final-document", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"one", "two"}, 8, 3, true, false, false)
		steps := make([]altScreenStep, 0, 5)
		rig.tui.Start()
		rig.terminal.log = nil
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("first"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("unchanged"))
		rig.component.lines = []string{"ONE", "two" + CursorMarker}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("changed-with-cursor"))
		rig.terminal.columns = 4
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("width-change"))
		rig.terminal.rows = 2
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("height-change"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = nil
		scenarios = append(scenarios, altScreenScenario{Name: "differential-and-resize", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"zero", "one", "two", "three", "four", "five"}, 8, 3, false, false, false)
		steps := make([]altScreenStep, 0, 4)
		rig.tui.Start()
		rig.terminal.log = nil
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("follow-end"))
		rig.tui.ScrollBy(-2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("scroll-up"))
		rig.component.lines = append(rig.component.lines, "six")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("growth-while-detached"))
		rig.tui.ScrollToBottom()
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("return-to-bottom"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = nil
		scenarios = append(scenarios, altScreenScenario{Name: "implicit-follow-end", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"implicit"}, 8, 3, false, false, false)
		explicit := &mutableAltComponent{lines: []string{"explicit", "cursor" + CursorMarker}}
		steps := make([]altScreenStep, 0, 3)
		rig.tui.Start()
		rig.terminal.log = nil
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("implicit"))
		rig.tui.SetLayoutRoot(explicit)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("explicit"))
		rig.tui.SetLayoutRoot(nil)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("implicit-again"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = nil
		scenarios = append(scenarios, altScreenScenario{Name: "explicit-layout-root", Steps: steps})
	}

	{
		setTestCapabilities(ImageKitty)
		registerTestKittyImage(101)
		rig := newAltScreenRig([]string{testKittyLine(101)}, 8, 3, false, false, false)
		steps := make([]altScreenStep, 0, 6)
		rig.tui.Start()
		steps = append(steps, rig.step("start"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("first-transmission"))
		rig.tui.RenderNow(true)
		steps = append(steps, rig.step("placement-reuse"))
		registerTestKittyImage(101)
		rig.tui.RenderNow(true)
		steps = append(steps, rig.step("new-generation"))
		rig.component.lines = []string{"text"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("image-disappears"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		steps = append(steps, rig.step("stop"))
		scenarios = append(scenarios, altScreenScenario{Name: "kitty-reuse", Steps: steps})
	}

	{
		setTestCapabilities(ImageKitty)
		rig := newAltScreenRig([]string{}, 8, 1, false, false, false)
		steps := make([]altScreenStep, 0, 18)
		rig.tui.Start()
		rig.terminal.log = nil
		for id := 200; id < 218; id++ {
			registerTestKittyImage(id)
			rig.component.lines = []string{testKittyLine(id)}
			rig.tui.RenderNow(false)
			steps = append(steps, rig.step("image-"+itoa(id)))
		}
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = nil
		scenarios = append(scenarios, altScreenScenario{Name: "kitty-lru-count", Steps: steps})
	}

	{
		setTestCapabilities(ImageITerm2)
		component := &mutableAltComponent{capability: true}
		rig := newAltScreenRig(nil, 8, 3, false, false, false, component)
		steps := make([]altScreenStep, 0, 3)
		rig.tui.Start()
		steps = append(steps, rig.step("start-suppressed"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("fallback-frame"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		steps = append(steps, rig.step("stop-restored"))
		scenarios = append(scenarios, altScreenScenario{Name: "iterm2-suppression", Steps: steps})
	}

	for _, multiplexer := range []bool{false, true} {
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"mouse"}, 8, 3, false, true, multiplexer)
		steps := make([]altScreenStep, 0, 2)
		rig.tui.Start()
		steps = append(steps, rig.step("start"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		steps = append(steps, rig.step("stop"))
		name := "mouse-all-motion"
		if multiplexer {
			name = "mouse-multiplexer"
		}
		scenarios = append(scenarios, altScreenScenario{Name: name, Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		SetKeybindings(NewKeybindingsManager(TUIKeybindings, KeybindingsConfig{
			"tui.altScreen.halfPageUp": {"u"},
			"tui.altScreen.lineDown":   {"d"},
			"tui.altScreen.pageUp":     {"pageUp", "x"},
			"tui.altScreen.top":        {"home", "x"},
		}))
		bel := "\x1b]133;A\x07"
		st := "\x1b]133;A\x1b\\"
		rig := newAltScreenRig([]string{
			bel + "prompt-0", "one", "two", st + "prompt-3", "four", "five",
			"six", "seven", bel + "prompt-8", "nine", "ten", "eleven",
		}, 8, 6, false, false, false)
		steps := make([]altScreenStep, 0, 12)
		rig.tui.Start()
		rig.terminal.log = nil
		rig.tui.SetFocus(rig.component)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.onInput("\x1b[5~")
		steps = append(steps, rig.step("page-up"))
		rig.terminal.onInput("\x1b[5;1:3~")
		steps = append(steps, rig.step("release-consumed"))
		rig.terminal.onInput("\x1b[5;1:2~")
		steps = append(steps, rig.step("repeat-page-up"))
		rig.terminal.onInput("u")
		steps = append(steps, rig.step("custom-half-page-up"))
		rig.terminal.onInput("d")
		steps = append(steps, rig.step("custom-line-down"))
		rig.terminal.onInput("\x1b[1;5A")
		steps = append(steps, rig.step("previous-prompt"))
		rig.terminal.onInput("\x1b[1;5B")
		steps = append(steps, rig.step("next-prompt"))
		rig.terminal.onInput("x")
		steps = append(steps, rig.step("conflict-page-before-top"))
		rig.terminal.onInput("\x1b[F")
		steps = append(steps, rig.step("bottom"))
		rig.terminal.onInput("z")
		steps = append(steps, rig.step("unmatched-forwarded"))
		rig.overlay = &mutableAltComponent{lines: []string{"overlay"}}
		rig.tui.ShowOverlay(rig.overlay, OverlayOptions{})
		rig.terminal.onInput("\x1b[5~")
		steps = append(steps, rig.step("overlay-defers"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = nil
		scenarios = append(scenarios, altScreenScenario{Name: "keyboard-navigation", Steps: steps})
		SetKeybindings(NewKeybindingsManager(TUIKeybindings))
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{
			"zero", "foo one", "two", "three", "four", "FOO five",
			"six", "seven", "eight", "foo nine",
		}, 40, 6, false, false, false)
		steps := make([]altScreenStep, 0, 11)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.onInput("\x1b[102;6u")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("open"))
		rig.terminal.onInput("foo")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("query"))
		rig.terminal.onInput("\r")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("next"))
		rig.terminal.onInput("\r")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("next-wrap"))
		rig.terminal.onInput("\x1b[13;2u")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("previous-wrap"))
		rig.terminal.onInput("\x1b[5~")
		steps = append(steps, rig.step("page-up-while-focused"))
		rig.terminal.onInput("\x03")
		steps = append(steps, rig.step("ctrl-c-does-not-close"))
		rig.terminal.onInput("\x1b")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("close"))
		rig.terminal.onInput("\x1b[102;6u")
		rig.tui.RenderNow(false)
		rig.overlay = &mutableAltComponent{lines: []string{"other"}}
		rig.tui.ShowOverlay(rig.overlay, OverlayOptions{})
		rig.terminal.onInput("\r")
		steps = append(steps, rig.step("other-overlay-defers"))
		rig.terminal.onInput("\x1b[102;6u")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("toggle-global-close"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "search-keyboard", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		component := &mutableAltComponent{lines: []string{"zero", "one", "two"}, mouseHandling: true}
		rig := newAltScreenRig(nil, 12, 3, false, false, false, component)
		steps := make([]altScreenStep, 0, 8)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.onInput("\x1b[<0;3;2M")
		steps = append(steps, rig.step("press-capture"))
		rig.terminal.onInput("\x1b[<32;5;2M")
		rig.terminal.onInput("\x1b[<0;5;2m")
		steps = append(steps, rig.step("drag-release-no-click"))
		for count := 1; count <= 4; count++ {
			rig.terminal.onInput("\x1b[<0;3;2M")
			rig.terminal.onInput("\x1b[<0;3;2m")
			steps = append(steps, rig.step("click-"+itoa(count)))
		}
		rig.terminal.onInput("\x1b[O")
		steps = append(steps, rig.step("focus-out"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "mouse-component-dispatch", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, 12, 4, false, false, false)
		steps := make([]altScreenStep, 0, 5)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.onInput("\x1b[<64;2;2M")
		steps = append(steps, rig.step("sgr-up"))
		rig.terminal.onInput("\x1b[<72;2;2M")
		steps = append(steps, rig.step("alt-up-five"))
		rig.terminal.onInput("\x1b[M" + string([]byte{97, 34, 34}))
		steps = append(steps, rig.step("x10-down"))
		rig.terminal.onInput("\x1b[<66;2;2M")
		steps = append(steps, rig.step("invalid-wheel-consumed"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "mouse-wheel", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, 12, 4, false, false, false)
		rig.tui.implicitScrollView.SetScrollbar(ScrollbarAlways)
		steps := make([]altScreenStep, 0, 6)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.onInput("\x1b[<35;12;1M")
		steps = append(steps, rig.step("hover"))
		rig.terminal.onInput("\x1b[<0;12;1M")
		steps = append(steps, rig.step("track-press"))
		rig.terminal.onInput("\x1b[<32;12;4M")
		steps = append(steps, rig.step("drag-bottom"))
		rig.terminal.onInput("\x1b[<0;12;4m")
		steps = append(steps, rig.step("release"))
		rig.terminal.onInput("\x1b[<35;1;1M")
		steps = append(steps, rig.step("hover-leave"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "mouse-scrollbar", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{
			"zero", "foo one", "two", "three", "four", "foo five",
			"six", "seven", "eight", "foo nine",
		}, 40, 6, false, false, false)
		steps := make([]altScreenStep, 0, 6)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.terminal.onInput("\x1b[102;6u")
		rig.terminal.onInput("foo")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("query"))
		bounds, _ := rig.tui.activeSearch.overlay.Bounds()
		x := bounds.Col + rig.tui.activeSearch.component.previousButtonStart
		y := bounds.Row + 2
		rig.terminal.onInput(fmt.Sprintf("\x1b[<35;%d;%dM", x+1, y+1))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("hover-previous"))
		rig.terminal.onInput(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("press-previous"))
		bounds, _ = rig.tui.activeSearch.overlay.Bounds()
		x = bounds.Col + rig.tui.activeSearch.component.nextButtonStart
		y = bounds.Row + 2
		rig.terminal.onInput(fmt.Sprintf("\x1b[<1;%d;%dM", x+1, y+1))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("middle-does-not-navigate"))
		rig.terminal.onInput(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("press-next"))
		rig.terminal.onInput("\x1b[<35;1;6M")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("hover-leave"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "search-mouse-navigation", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		copyOnSelect := false
		rig := newAltScreenRigOptions([]string{
			"pad" + altContentStartMarker + "  alpha  " + altContentEndMarker + " tail",
			tuitext.DecorationMarker + "frame",
			"\x1b[31mbeta界\x1b[0m",
		}, 20, 3, false, false, false, TuiAltScreenOptions{CopyOnSelect: &copyOnSelect})
		steps := make([]altScreenStep, 0, 3)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.terminal.onInput("\x1b[<0;4;1M")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("press"))
		rig.terminal.onInput("\x1b[<32;6;3M")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("drag"))
		rig.terminal.onInput("\x1b[<0;6;3m")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("release"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "selection-markers-and-wide", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		copyOnSelect := false
		rig := newAltScreenRigOptions([]string{"path/to-kebab other"}, 24, 2, false, false, false, TuiAltScreenOptions{CopyOnSelect: &copyOnSelect})
		steps := make([]altScreenStep, 0, 4)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		for count := 1; count <= 4; count++ {
			rig.terminal.onInput("\x1b[<0;7;1M")
			rig.terminal.onInput("\x1b[<0;7;1m")
			rig.tui.RenderNow(false)
			steps = append(steps, rig.step("click-"+itoa(count)))
		}
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "selection-click-granularity", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"copy me"}, 20, 2, false, false, false)
		steps := make([]altScreenStep, 0, 1)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.terminal.onInput("\x1b[<0;1;1M")
		rig.terminal.onInput("\x1b[<32;7;1M")
		rig.terminal.onInput("\x1b[<0;7;1m")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("osc52-and-success-flash"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "selection-copy-fallback", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		var rig *altScreenRig
		options := TuiAltScreenOptions{CopySelection: func(text string) bool {
			rig.copiedTexts = append(rig.copiedTexts, text)
			return false
		}}
		rig = newAltScreenRigOptions([]string{"copy"}, 20, 2, false, false, false, options)
		steps := make([]altScreenStep, 0, 1)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.tui.copyTextToClipboard("Ω failed")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("injected-failure"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "selection-copy-injected", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		copyOnSelect := false
		var rig *altScreenRig
		options := TuiAltScreenOptions{CopyOnSelect: &copyOnSelect, OpenURL: func(url string) {
			rig.openedURLs = append(rig.openedURLs, url)
		}}
		link := "\x1b]8;;https://example.com\x07link\x1b]8;;\x07 plain"
		rig = newAltScreenRigOptions([]string{link}, 24, 2, false, false, false, options)
		steps := make([]altScreenStep, 0, 1)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.terminal.onInput("\x1b[<0;2;1M")
		rig.terminal.onInput("\x1b[<0;2;1m")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("open-link"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "selection-link", Steps: steps})
	}

	{
		setTestCapabilities(ImageNone)
		rig := newAltScreenRig([]string{"under"}, 12, 3, false, false, false)
		steps := make([]altScreenStep, 0, 1)
		rig.tui.Start()
		rig.terminal.log = []string{}
		rig.tui.RenderNow(false)
		rig.tui.FlashStyled("one", 10*time.Second, "\x1b[45m")
		rig.tui.FlashStyled("message-too-long", 10*time.Second, "\x1b[46m")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("stacked"))
		rig.tui.Stop(StopOptions{PreserveScreen: true})
		rig.terminal.log = []string{}
		scenarios = append(scenarios, altScreenScenario{Name: "flash-stacking", Steps: steps})
	}

	setTestCapabilities(ImageNone)
	return scenarios
}
