package tui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

type mainScreenReference struct {
	Scenarios []mainScreenScenario `json:"scenarios"`
}

type mainScreenScenario struct {
	Name  string           `json:"name"`
	Steps []mainScreenStep `json:"steps"`
}

type mainScreenStep struct {
	Label        string                    `json:"label"`
	Log          []string                  `json:"log,omitempty"`
	WriteSummary []mainScreenWriteSummary  `json:"writeSummary,omitempty"`
	FullRedraws  int                       `json:"fullRedraws"`
	State        *TuiMainScreenRenderState `json:"state,omitempty"`
}

type mainScreenWriteSummary struct {
	Event       string `json:"event,omitempty"`
	UTF16Length int    `json:"utf16Length,omitempty"`
	ByteLength  int    `json:"byteLength,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	Suffix      string `json:"suffix,omitempty"`
}

type mutableMainComponent struct {
	lines []string
}

func (m *mutableMainComponent) Render(int) []string { return append([]string(nil), m.lines...) }
func (*mutableMainComponent) Invalidate()           {}

type mainScreenRig struct {
	terminal  *fakeTerminal
	component *mutableMainComponent
	tui       *TuiMainScreen
}

func newMainScreenRig(lines []string, columns, rows int, showHardwareCursor bool, termux ...bool) *mainScreenRig {
	terminal := newFakeTerminal(columns, rows)
	component := &mutableMainComponent{lines: append([]string(nil), lines...)}
	isTermux := false
	if len(termux) > 0 {
		isTermux = termux[0]
	}
	tui := NewTuiMainScreen(terminal, TuiMainScreenOptions{
		ShowHardwareCursor: showHardwareCursor,
		Scheduler:          &fakeScheduler{},
		IsTermux:           func() bool { return isTermux },
	})
	tui.AddChild(component)
	return &mainScreenRig{terminal: terminal, component: component, tui: tui}
}

func (r *mainScreenRig) step(label string) mainScreenStep {
	log := append([]string(nil), r.terminal.log...)
	r.terminal.log = nil
	state := r.tui.CaptureRenderState()
	return mainScreenStep{Label: label, Log: log, FullRedraws: r.tui.FullRedraws(), State: &state}
}

func loadMainScreenReference(t *testing.T) mainScreenReference {
	t.Helper()
	data, err := os.ReadFile("testdata/main-screen-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference mainScreenReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Scenarios) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return reference
}

func TestMainScreenMatchesReference(t *testing.T) {
	reference := loadMainScreenReference(t)
	actual := buildMainScreenScenarios()
	if len(actual) != len(reference.Scenarios) {
		t.Fatalf("scenario count = %d, reference = %d", len(actual), len(reference.Scenarios))
	}
	for index, want := range reference.Scenarios {
		got := actual[index]
		t.Run(want.Name, func(t *testing.T) {
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			wantJSON, _ := json.MarshalIndent(want, "", "  ")
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("main-screen trace mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestMainScreenOverwideDifferentialStops(t *testing.T) {
	rig := newMainScreenRig([]string{"ok"}, 2, 5, false)
	rig.tui.logDirectory = t.TempDir()
	rig.tui.RenderNow(false)
	rig.terminal.log = nil
	rig.component.lines = []string{"wide"}

	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		rig.tui.RenderNow(false)
	}()
	if panicValue == nil {
		t.Fatal("overwide differential render did not panic")
	}
	message, ok := panicValue.(string)
	if !ok || !strings.Contains(message, "Rendered line 0 exceeds terminal width (4 > 2).") {
		t.Fatalf("unexpected panic: %#v", panicValue)
	}
	wantLog := []string{"write: ", "write:\x1b[1B", "write:\r\n", "showCursor", "stop"}
	if got, want := rig.terminal.log, wantLog; !reflectStringSlicesEqual(got, want) {
		t.Fatalf("terminal log = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(rig.tui.logDirectory, "midas-tui-crash.log")); err != nil {
		t.Fatalf("crash log was not written: %v", err)
	}
}

func reflectStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func buildMainScreenScenarios() []mainScreenScenario {
	scenarios := make([]mainScreenScenario, 0, 18)

	{
		rig := newMainScreenRig([]string{"one", "two"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 5)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("first"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("unchanged"))
		rig.component.lines = []string{"one", "TWO"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("replace-last"))
		rig.component.lines = []string{"one", "TWO", "three"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("append"))
		rig.component.lines = []string{"one"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("delete-suffix"))
		scenarios = append(scenarios, mainScreenScenario{Name: "basic-diff", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"left" + CursorMarker, "right"}, 20, 5, true)
		steps := make([]mainScreenStep, 0, 3)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("cursor-first"))
		rig.component.lines = []string{"left", "wide界" + CursorMarker}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("cursor-second"))
		rig.component.lines = []string{"left", "right"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("cursor-hidden"))
		scenarios = append(scenarios, mainScreenScenario{Name: "hardware-cursor", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"width-sensitive"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 3)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.columns = 24
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("width-change"))
		rig.terminal.rows = 7
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("height-change"))
		scenarios = append(scenarios, mainScreenScenario{Name: "resize", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"zero", "one", "two", "three", "four", "five"}, 20, 3, false, true)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.terminal.rows = 5
		rig.component.lines[5] = "FIVE"
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("termux-height-change"))
		scenarios = append(scenarios, mainScreenScenario{Name: "termux-resize", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"zero", "one", "two", "three"}, 20, 5, false)
		rig.tui.SetClearOnShrink(true)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.component.lines = []string{"zero", "one"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("clear-on-shrink"))
		scenarios = append(scenarios, mainScreenScenario{Name: "clear-on-shrink", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"zero", "one", "two", "three"}, 20, 5, false)
		rig.tui.SetClearOnShrink(true)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		handle := rig.tui.ShowOverlay(&mutableMainComponent{lines: []string{"hidden"}}, OverlayOptions{})
		handle.SetHidden(true)
		rig.component.lines = []string{"zero", "one"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("hidden-overlay-entry"))
		scenarios = append(scenarios, mainScreenScenario{Name: "shrink-with-overlay-entry", Steps: steps})
	}

	{
		lines := make([]string, 8)
		for index := range lines {
			lines[index] = "line-" + itoa(index)
		}
		rig := newMainScreenRig(lines, 20, 3, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.component.lines[0] = "changed-above-viewport"
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("change-above-viewport"))
		scenarios = append(scenarios, mainScreenScenario{Name: "viewport-fallback", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("first-empty"))
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("second-empty"))
		scenarios = append(scenarios, mainScreenScenario{Name: "empty-renders", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"zero", "one", "two", "three", "four"}, 20, 3, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.component.lines = append(rig.component.lines, "five", "six", "seven")
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("append-beyond-viewport"))
		scenarios = append(scenarios, mainScreenScenario{Name: "append-beyond-viewport", Steps: steps})
	}

	{
		image := "\x1b_Ga=T,i=17,r=3;AAAA\x1b\\"
		rig := newMainScreenRig([]string{"zero", "one", "two", "three", "four"}, 20, 3, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.component.lines = []string{"zero", "one", "two", "three", image, "", ""}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("partial-image-fallback"))
		scenarios = append(scenarios, mainScreenScenario{Name: "kitty-partial-viewport", Steps: steps})
	}

	{
		image := func(id int) string { return "\x1b_Ga=T,i=" + itoa(id) + ",r=3;AAAA\x1b\\" }
		rig := newMainScreenRig([]string{image(7), "", "", "tail"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 3)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial-image"))
		rig.component.lines = []string{image(8), "", "", "tail"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("replace-image"))
		rig.terminal.columns = 21
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("clear-image-on-width-change"))
		scenarios = append(scenarios, mainScreenScenario{Name: "kitty-reserved-rows", Steps: steps})
	}

	{
		complex := "\x1b_Ga=T,i=0x10,i=4294967295,i=0,i=1.5,r=2;AAAA\x1b\\"
		rig := newMainScreenRig([]string{complex, ""}, 20, 3, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("parse-ids"))
		rig.terminal.columns = 21
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("delete-valid-ids"))
		scenarios = append(scenarios, mainScreenScenario{Name: "kitty-header-numbers", Steps: steps})
	}

	{
		malformed := "\x1b_Ga=T,i=7,i=7,i=no,i=-1,i=4294967296,r=wat;AAAA\x1b\\"
		rig := newMainScreenRig([]string{malformed}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("parse-malformed"))
		rig.terminal.columns = 21
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("delete-once"))
		scenarios = append(scenarios, mainScreenScenario{Name: "kitty-header-malformed", Steps: steps})
	}

	{
		image := "\x1b_Ga=T,i=9,r=2;AAAA\x1b\\"
		rig := newMainScreenRig([]string{image, "", "tail"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 4)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		captured := rig.tui.CaptureRenderState()
		rig.component.lines = []string{"temporary"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("temporary"))
		rig.tui.RestoreRenderState(captured)
		steps = append(steps, rig.step("restored"))
		rig.component.lines = []string{image, "", "tail"}
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("render-after-restore"))
		scenarios = append(scenarios, mainScreenScenario{Name: "capture-restore", Steps: steps})
	}

	{
		rig := newMainScreenRig([]string{"force"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("initial"))
		rig.tui.RenderNow(true)
		steps = append(steps, rig.step("forced"))
		scenarios = append(scenarios, mainScreenScenario{Name: "forced-reset", Steps: steps})
	}

	for _, preserveScreen := range []bool{true, false} {
		rig := newMainScreenRig([]string{"one", "two"}, 20, 5, false)
		steps := make([]mainScreenStep, 0, 2)
		rig.tui.RenderNow(false)
		steps = append(steps, rig.step("render"))
		rig.tui.Stop(StopOptions{PreserveScreen: preserveScreen})
		steps = append(steps, rig.step("stop"))
		name := "stop-clean"
		if preserveScreen {
			name = "stop-preserve"
		}
		scenarios = append(scenarios, mainScreenScenario{Name: name, Steps: steps})
	}

	{
		prefixLength := 1024*1024 - 9
		rig := newMainScreenRig([]string{string(makeFilledBytes('a', prefixLength)) + "😀tail"}, prefixLength+20, 5, false)
		rig.tui.RenderNow(false)
		log := append([]string(nil), rig.terminal.log...)
		rig.terminal.log = nil
		scenarios = append(scenarios, mainScreenScenario{
			Name: "bounded-writes",
			Steps: []mainScreenStep{{
				Label:        "astral-boundary",
				WriteSummary: summarizeMainScreenLog(log),
				FullRedraws:  rig.tui.FullRedraws(),
			}},
		})
	}

	return scenarios
}

func makeFilledBytes(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func summarizeMainScreenLog(log []string) []mainScreenWriteSummary {
	result := make([]mainScreenWriteSummary, 0, len(log))
	for _, entry := range log {
		if len(entry) < len("write:") || entry[:len("write:")] != "write:" {
			result = append(result, mainScreenWriteSummary{Event: entry})
			continue
		}
		data := entry[len("write:"):]
		digest := sha256.Sum256([]byte(data))
		units := utf16.Encode([]rune(data))
		prefixEnd := min(16, len(units))
		suffixStart := max(0, len(units)-16)
		result = append(result, mainScreenWriteSummary{
			UTF16Length: len(units),
			ByteLength:  len([]byte(data)),
			SHA256:      hex.EncodeToString(digest[:]),
			Prefix:      string(utf16.Decode(units[:prefixEnd])),
			Suffix:      string(utf16.Decode(units[suffixStart:])),
		})
	}
	return result
}
