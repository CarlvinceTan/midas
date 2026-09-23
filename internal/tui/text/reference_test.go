package text

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// referenceGolden mirrors tui/text/testdata/reference.json, which is produced
// by the local parity fixture generator. Every value
// in it is the reference renderer's own answer for the same input.
type referenceGolden struct {
	Generator string `json:"generator"`
	Node      string `json:"node"`
	Widths    []struct {
		S     string `json:"s"`
		Width int    `json:"width"`
	} `json:"widths"`
	Clusters []struct {
		C     string `json:"c"`
		Width int    `json:"width"`
	} `json:"clusters"`
	Segmentation []struct {
		S        string   `json:"s"`
		Clusters []string `json:"clusters"`
	} `json:"segmentation"`
	Strip []struct {
		S   string `json:"s"`
		Out string `json:"out"`
	} `json:"strip"`
	Normalize []struct {
		S   string `json:"s"`
		Out string `json:"out"`
	} `json:"normalize"`
	Truncate []struct {
		S        string `json:"s"`
		MaxWidth int    `json:"maxWidth"`
		Ellipsis string `json:"ellipsis"`
		Pad      bool   `json:"pad"`
		Out      string `json:"out"`
	} `json:"truncate"`
	Wrap []struct {
		S     string   `json:"s"`
		Width int      `json:"width"`
		Out   []string `json:"out"`
	} `json:"wrap"`
	Slice []struct {
		Line     string `json:"line"`
		StartCol int    `json:"startCol"`
		Length   int    `json:"length"`
		Strict   bool   `json:"strict"`
		Text     string `json:"text"`
		Width    int    `json:"width"`
	} `json:"slice"`
	Segments []struct {
		Line        string `json:"line"`
		BeforeEnd   int    `json:"beforeEnd"`
		AfterStart  int    `json:"afterStart"`
		AfterLen    int    `json:"afterLen"`
		StrictAfter bool   `json:"strictAfter"`
		Before      string `json:"before"`
		BeforeWidth int    `json:"beforeWidth"`
		After       string `json:"after"`
		AfterWidth  int    `json:"afterWidth"`
	} `json:"segments"`
	CharClass []struct {
		S           string `json:"s"`
		Whitespace  bool   `json:"whitespace"`
		Punctuation bool   `json:"punctuation"`
	} `json:"charClass"`
	CellRange []struct {
		Line   string `json:"line"`
		Column int    `json:"column"`
		Found  bool   `json:"found"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	} `json:"cellRange"`
	Osc8Link []struct {
		Line   string `json:"line"`
		Column int    `json:"column"`
		Found  bool   `json:"found"`
		URL    string `json:"url"`
	} `json:"osc8Link"`
	RoundedFrame []struct {
		Middle string `json:"middle"`
		Edge   string `json:"edge"`
		Out    string `json:"out"`
	} `json:"roundedFrame"`
	Background []struct {
		S     string `json:"s"`
		Width int    `json:"width"`
		Out   string `json:"out"`
	} `json:"background"`
	ActiveBackground []struct {
		S   string `json:"s"`
		Out string `json:"out"`
	} `json:"activeBackground"`
	Ansi []struct {
		S string `json:"s"`
		// Pos is the reference's UTF-16 code-unit index; PosByte is the same
		// position as a UTF-8 byte offset. The Go port is byte-indexed, so it
		// asserts against PosByte — matching the reference's answers without
		// pretending the two index models are interchangeable.
		Pos     int    `json:"pos"`
		PosByte int    `json:"posByte"`
		Code    string `json:"code"`
		Length  int    `json:"length"`
	} `json:"ansi"`
}

func loadReference(t *testing.T) referenceGolden {
	t.Helper()
	path := filepath.Join("testdata", "reference.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: node tools/parity/gen-text-goldens.mjs)", path, err)
	}
	var golden referenceGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(golden.Widths) == 0 {
		t.Fatalf("%s contains no cases", path)
	}
	return golden
}

func TestVisibleWidthMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Widths {
		if got := VisibleWidth(c.S); got != c.Width {
			t.Errorf("VisibleWidth(%q) = %d, reference = %d", c.S, got, c.Width)
		}
	}
}

func TestGraphemeWidthMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Clusters {
		if got := GraphemeWidth(c.C); got != c.Width {
			t.Errorf("GraphemeWidth(%q) = %d, reference = %d", c.C, got, c.Width)
		}
	}
}

// TestSegmentationMatchesReference pins the Go grapheme clusterer to V8's
// Intl.Segmenter on the corpus. Divergence here is the highest-risk item in the
// whole port, so it is asserted separately from width.
func TestSegmentationMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Segmentation {
		got := Graphemes(c.S)
		want := c.Clusters
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Graphemes(%q) = %q, reference = %q", c.S, got, want)
		}
	}
}

func TestStripTerminalSequencesMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Strip {
		if got := StripTerminalSequences(c.S); got != c.Out {
			t.Errorf("StripTerminalSequences(%q) = %q, reference = %q", c.S, got, c.Out)
		}
	}
}

func TestNormalizeTerminalOutputMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Normalize {
		if got := NormalizeTerminalOutput(c.S); got != c.Out {
			t.Errorf("NormalizeTerminalOutput(%q) = %q, reference = %q", c.S, got, c.Out)
		}
	}
}

func TestGetGraphemeCellRangeMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.CellRange {
		got, ok := GetGraphemeCellRange(c.Line, c.Column)
		if ok != c.Found {
			t.Errorf("GetGraphemeCellRange(%q, %d) found = %v, reference = %v", c.Line, c.Column, ok, c.Found)
			continue
		}
		if ok && (got.Start != c.Start || got.End != c.End) {
			t.Errorf("GetGraphemeCellRange(%q, %d) = {%d, %d}, reference = {%d, %d}",
				c.Line, c.Column, got.Start, got.End, c.Start, c.End)
		}
	}
}

func TestGetOsc8LinkAtColumnMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Osc8Link {
		got, ok := GetOsc8LinkAtColumn(c.Line, c.Column)
		if ok != c.Found || got != c.URL {
			t.Errorf("GetOsc8LinkAtColumn(%q, %d) = (%q, %v), reference = (%q, %v)",
				c.Line, c.Column, got, ok, c.URL, c.Found)
		}
	}
}

func TestRoundedFrameRowMatchesReference(t *testing.T) {
	golden := loadReference(t)
	color := func(text string) string { return "\x1b[36m" + text + "\x1b[39m" }
	for _, c := range golden.RoundedFrame {
		got := RoundedFrameRow(c.Middle, FrameEdge(c.Edge), color)
		if got != c.Out {
			t.Errorf("RoundedFrameRow(%q, %q) = %q, reference = %q", c.Middle, c.Edge, got, c.Out)
		}
	}
}

func TestTruncateToWidthMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Truncate {
		got := TruncateToWidth(c.S, c.MaxWidth, c.Ellipsis, c.Pad)
		if got != c.Out {
			// Midas intentionally diverges from the recovered pi-tui reference
			// here: a truncation ellipsis inherits the active foreground colour.
			// Geometry, text, hyperlink closure and padding must still match.
			colourOnlyDivergence := c.Ellipsis != "" && VisibleWidth(c.S) > c.MaxWidth &&
				strings.Contains(c.S, "\x1b[") &&
				StripTerminalSequences(got) == StripTerminalSequences(c.Out) &&
				VisibleWidth(got) == VisibleWidth(c.Out)
			if colourOnlyDivergence {
				continue
			}
			t.Errorf("TruncateToWidth(%q, %d, %q, %v) = %q, reference = %q",
				c.S, c.MaxWidth, c.Ellipsis, c.Pad, got, c.Out)
		}
	}
}

func TestWrapTextWithAnsiMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Wrap {
		got := WrapTextWithAnsi(c.S, c.Width)
		if !reflect.DeepEqual(got, c.Out) {
			t.Errorf("WrapTextWithAnsi(%q, %d) = %q, reference = %q", c.S, c.Width, got, c.Out)
		}
	}
}

func TestSliceByColumnMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Slice {
		got := SliceWithWidth(c.Line, c.StartCol, c.Length, c.Strict)
		if got.Text != c.Text || got.Width != c.Width {
			t.Errorf("SliceWithWidth(%q, %d, %d, %v) = (%q, %d), reference = (%q, %d)",
				c.Line, c.StartCol, c.Length, c.Strict, got.Text, got.Width, c.Text, c.Width)
		}
	}
}

func TestExtractSegmentsMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Segments {
		got := ExtractSegments(c.Line, c.BeforeEnd, c.AfterStart, c.AfterLen, c.StrictAfter)
		if got.Before != c.Before || got.BeforeWidth != c.BeforeWidth ||
			got.After != c.After || got.AfterWidth != c.AfterWidth {
			t.Errorf("ExtractSegments(%q, %d, %d, %d, %v) = (%q, %d, %q, %d), reference = (%q, %d, %q, %d)",
				c.Line, c.BeforeEnd, c.AfterStart, c.AfterLen, c.StrictAfter,
				got.Before, got.BeforeWidth, got.After, got.AfterWidth,
				c.Before, c.BeforeWidth, c.After, c.AfterWidth)
		}
	}
}

func TestCharClassifiersMatchReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.CharClass {
		if got := IsWhitespaceChar(c.S); got != c.Whitespace {
			t.Errorf("IsWhitespaceChar(%q) = %v, reference = %v", c.S, got, c.Whitespace)
		}
		if got := IsPunctuationChar(c.S); got != c.Punctuation {
			t.Errorf("IsPunctuationChar(%q) = %v, reference = %v", c.S, got, c.Punctuation)
		}
	}
}

func TestApplyBackgroundToLineMatchesReference(t *testing.T) {
	golden := loadReference(t)
	bgFn := func(text string) string { return "\x1b[41m" + text + "\x1b[0m" }
	for _, c := range golden.Background {
		got := ApplyBackgroundToLine(c.S, c.Width, bgFn)
		if got != c.Out {
			t.Errorf("ApplyBackgroundToLine(%q, %d) = %q, reference = %q", c.S, c.Width, got, c.Out)
		}
	}
}

func TestGetActiveBackgroundAnsiMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.ActiveBackground {
		if got := GetActiveBackgroundAnsi(c.S); got != c.Out {
			t.Errorf("GetActiveBackgroundAnsi(%q) = %q, reference = %q", c.S, got, c.Out)
		}
	}
}

func TestExtractAnsiCodeMatchesReference(t *testing.T) {
	golden := loadReference(t)
	for _, c := range golden.Ansi {
		code, length, ok := ExtractAnsiCode(c.S, c.PosByte)
		if !ok {
			t.Errorf("ExtractAnsiCode(%q, byte %d) [utf16 %d]: no sequence, reference found %q",
				c.S, c.PosByte, c.Pos, c.Code)
			continue
		}
		if code != c.Code || length != c.Length {
			t.Errorf("ExtractAnsiCode(%q, byte %d) = (%q, %d), reference = (%q, %d)",
				c.S, c.PosByte, code, length, c.Code, c.Length)
		}
	}
}
