package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type processTerminalReferenceFixture struct {
	Scenarios []struct {
		Name            string   `json:"name"`
		Output          string   `json:"output"`
		Input           []string `json:"input"`
		Kitty           bool     `json:"kitty"`
		ModifyOtherKeys bool     `json:"modifyOtherKeys"`
	} `json:"scenarios"`
	ParsedNegotiation []struct {
		Input  string `json:"input"`
		Result *struct {
			Type  string `json:"type"`
			Flags int    `json:"flags"`
		} `json:"result"`
	} `json:"parsedNegotiation"`
	EscapeTimeouts []struct {
		Name   string            `json:"name"`
		Env    map[string]string `json:"env"`
		Result float64           `json:"result"`
	} `json:"escapeTimeouts"`
	NormalizedShiftEnter []struct {
		Name    string `json:"name"`
		Input   string `json:"input"`
		Detect  bool   `json:"detect"`
		Pressed bool   `json:"pressed"`
		Result  string `json:"result"`
	} `json:"normalizedShiftEnter"`
}

func loadProcessTerminalReference(t *testing.T) processTerminalReferenceFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "process-terminal-reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture processTerminalReferenceFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Scenarios)+len(fixture.ParsedNegotiation)+len(fixture.EscapeTimeouts) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return fixture
}

func TestProcessTerminalScenariosMatchReference(t *testing.T) {
	fixture := loadProcessTerminalReference(t)
	for _, scenario := range fixture.Scenarios {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			terminal, output := temporaryProcessTerminal(t)
			var inputMu sync.Mutex
			input := make([]string, 0)
			terminal.onInput = func(sequence string) { inputMu.Lock(); defer inputMu.Unlock(); input = append(input, sequence) }
			switch scenario.Name {
			case "output-methods":
				terminal.MoveBy(2)
				terminal.MoveBy(-1)
				terminal.MoveBy(0)
				terminal.HideCursor()
				terminal.ShowCursor()
				terminal.ClearLine()
				terminal.ClearFromCursor()
				terminal.ClearScreen()
				terminal.SetTitle("title")
				terminal.SetProgress(true)
				terminal.SetProgress(true)
				terminal.SetProgress(false)
				terminal.Write("payload")
			case "kitty-split":
				terminal.processInputSequence("\x1b[?1;2c")
				terminal.processInputSequence("a")
				terminal.processInputSequence("\x1b[")
				terminal.processInputSequence("?7u")
			case "kitty-zero":
				terminal.processInputSequence("\x1b[?0u")
			case "invalid-split":
				terminal.processInputSequence("\x1b[")
				terminal.processInputSequence("x")
			case "prefix-timeout":
				terminal.processInputSequence("\x1b[")
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					inputMu.Lock()
					count := len(input)
					inputMu.Unlock()
					if count != 0 {
						break
					}
					time.Sleep(time.Millisecond)
				}
			case "stop-inactive":
				terminal.Stop()
			case "stop-progress":
				terminal.SetProgress(true)
				terminal.Stop()
			default:
				t.Fatalf("unknown scenario %q", scenario.Name)
			}
			if got := readTerminalOutput(t, output); got != scenario.Output {
				t.Fatalf("output mismatch:\n got %q\nwant %q", got, scenario.Output)
			}
			inputMu.Lock()
			actualInput := make([]string, len(input))
			copy(actualInput, input)
			inputMu.Unlock()
			if !reflect.DeepEqual(actualInput, scenario.Input) {
				t.Fatalf("input mismatch:\n got %#v\nwant %#v", actualInput, scenario.Input)
			}
			if terminal.KittyProtocolActive() != scenario.Kitty {
				t.Fatalf("kitty active = %v, want %v", terminal.KittyProtocolActive(), scenario.Kitty)
			}
			terminal.stateMu.Lock()
			modifyOtherKeys := terminal.modifyOtherKeysActive
			terminal.stateMu.Unlock()
			if modifyOtherKeys != scenario.ModifyOtherKeys {
				t.Fatalf("modifyOtherKeys active = %v, want %v", modifyOtherKeys, scenario.ModifyOtherKeys)
			}
			SetKittyProtocolActive(false)
		})
	}
}

func TestProcessTerminalHelpersMatchReference(t *testing.T) {
	fixture := loadProcessTerminalReference(t)
	for _, testCase := range fixture.ParsedNegotiation {
		kind, flags := parseKeyboardNegotiation(testCase.Input)
		actualType := ""
		switch kind {
		case negotiationKitty:
			actualType = "kitty-flags"
		case negotiationDeviceAttributes:
			actualType = "device-attributes"
		}
		if testCase.Result == nil {
			if kind != negotiationNone {
				t.Errorf("parse %q = %q, want no result", testCase.Input, actualType)
			}
		} else if actualType != testCase.Result.Type || flags != testCase.Result.Flags {
			t.Errorf("parse %q = (%q, %d), want (%q, %d)", testCase.Input, actualType, flags, testCase.Result.Type, testCase.Result.Flags)
		}
	}
	for _, testCase := range fixture.EscapeTimeouts {
		t.Run("escape-timeout/"+testCase.Name, func(t *testing.T) {
			t.Setenv("MIDAS_TUI_ESC_TIMEOUT", "")
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_TTY", "")
			for key, value := range testCase.Env {
				t.Setenv(key, value)
			}
			want := time.Duration(testCase.Result * float64(time.Millisecond))
			if got := resolveEscapeTimeout(); got != want {
				t.Fatalf("timeout = %s, want %s", got, want)
			}
		})
	}
	for _, testCase := range fixture.NormalizedShiftEnter {
		if got := normalizeNativeShiftEnterInput(testCase.Input, testCase.Detect, testCase.Pressed); got != testCase.Result {
			t.Errorf("normalize %s = %q, want %q", testCase.Name, got, testCase.Result)
		}
	}
}

// TestSplitArrowSequenceWithALongerEscapeTimeout: a terminal that delivers an
// arrow key in pieces must not turn it into text. The configured escape timeout is
// the bound that covers the split, which is what makes it effective over a slow
// link as well as locally.
func TestSplitArrowSequenceWithALongerEscapeTimeout(t *testing.T) {
	t.Setenv("MIDAS_TUI_ESC_TIMEOUT", "400")
	terminal, _ := temporaryProcessTerminal(t)
	var inputMu sync.Mutex
	input := make([]string, 0)
	terminal.onInput = func(sequence string) { inputMu.Lock(); defer inputMu.Unlock(); input = append(input, sequence) }
	// The bytes of one arrow key, arriving one at a time with gaps that exceed the
	// default sequence timeout.
	terminal.stdinBuffer = newStdinBuffer(defaultInputSequenceTimeout, resolveEscapeTimeout(),
		terminal.onInput, func(string) {})
	for _, piece := range []string{"\x1b", "[", "B"} {
		terminal.stdinBuffer.Process([]byte(piece))
		time.Sleep(120 * time.Millisecond)
	}
	inputMu.Lock()
	got := append([]string(nil), input...)
	inputMu.Unlock()
	if len(got) != 1 || got[0] != "\x1b[B" {
		t.Fatalf("split arrow key produced %#v, want one down sequence", got)
	}
}
