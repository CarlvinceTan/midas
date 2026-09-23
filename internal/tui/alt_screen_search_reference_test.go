package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

type altSearchReference struct {
	MatchCases     []altSearchMatchCase     `json:"matchCases"`
	IndexSteps     []altSearchIndexStep     `json:"indexSteps"`
	ComponentSteps []altSearchComponentStep `json:"componentSteps"`
}

type altSearchMatchCase struct {
	Name    string                 `json:"name"`
	Lines   []string               `json:"lines"`
	Query   string                 `json:"query"`
	Matches []AltScreenSearchMatch `json:"matches"`
	Keys    []string               `json:"keys"`
}

type altSearchIndexStep struct {
	Label   string                 `json:"label"`
	Changed bool                   `json:"changed"`
	Matches []AltScreenSearchMatch `json:"matches"`
}

type altSearchComponentStep struct {
	Label               string   `json:"label"`
	Width               int      `json:"width"`
	Lines               []string `json:"lines"`
	Query               string   `json:"query"`
	ResultIndex         int      `json:"resultIndex"`
	ResultCount         int      `json:"resultCount"`
	PreviousButtonStart int      `json:"previousButtonStart"`
	PreviousButtonEnd   int      `json:"previousButtonEnd"`
	NextButtonStart     int      `json:"nextButtonStart"`
	NextButtonEnd       int      `json:"nextButtonEnd"`
	DirectionAtPrevious *int     `json:"directionAtPrevious,omitempty"`
	DirectionAtNext     *int     `json:"directionAtNext,omitempty"`
	Queries             []string `json:"queries"`
}

func loadAltSearchReference(t *testing.T) altSearchReference {
	t.Helper()
	data, err := os.ReadFile("testdata/alt-screen-search-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference altSearchReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.MatchCases)+len(reference.IndexSteps)+len(reference.ComponentSteps) == 0 {
		t.Fatal("the reference fixture contains no cases")
	}
	return reference
}

func TestAltScreenSearchMatcherMatchesReference(t *testing.T) {
	reference := loadAltSearchReference(t)
	for _, test := range reference.MatchCases {
		t.Run(test.Name, func(t *testing.T) {
			matches := FindAltScreenSearchMatches(test.Lines, test.Query)
			keys := make([]string, len(matches))
			for index, match := range matches {
				keys[index] = GetAltScreenSearchMatchKey(match)
			}
			got, _ := json.Marshal(struct {
				Matches []AltScreenSearchMatch `json:"matches"`
				Keys    []string               `json:"keys"`
			}{matches, keys})
			want, _ := json.Marshal(struct {
				Matches []AltScreenSearchMatch `json:"matches"`
				Keys    []string               `json:"keys"`
			}{test.Matches, test.Keys})
			if !bytes.Equal(got, want) {
				t.Fatalf("search mismatch\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}

func TestAltScreenSearchIndexMatchesReference(t *testing.T) {
	reference := loadAltSearchReference(t)
	inputs := []struct {
		label string
		lines []string
		query string
	}{
		{"initial", []string{"Foo bar", "foo"}, "foo"},
		{"same", []string{"Foo bar", "foo"}, "foo"},
		{"normalized-same", []string{"Foo bar", "foo"}, "  foo  "},
		{"case-change", []string{"Foo bar", "foo"}, "FOO"},
		{"source-ansi-change", []string{"\x1b[31mFoo\x1b[0m bar", "foo"}, "FOO"},
		{"source-text-change", []string{"none", "foo"}, "FOO"},
	}
	index := &AltScreenSearchIndex{}
	actual := make([]altSearchIndexStep, 0, len(inputs))
	for _, input := range inputs {
		result := index.Search(input.lines, input.query)
		actual = append(actual, altSearchIndexStep{Label: input.label, Changed: result.Changed, Matches: result.Matches})
	}
	assertSearchJSONEqual(t, actual, reference.IndexSteps)
}

func TestAltScreenSearchComponentMatchesReference(t *testing.T) {
	reference := loadAltSearchReference(t)
	queries := []string{}
	component := NewAltScreenSearchComponent(func(query string) { queries = append(queries, query) }, func(text string, hovered bool) string {
		if hovered {
			return "<" + text + ">"
		}
		return text
	})
	component.SetFocused(true)
	actual := make([]altSearchComponentStep, 0, 8)
	snapshot := func(label string, width int) {
		lines := component.Render(width)
		var previous, next *int
		if direction, ok := component.NavigationDirectionAt(2, component.previousButtonStart); ok {
			value := direction
			previous = &value
		}
		if direction, ok := component.NavigationDirectionAt(2, component.nextButtonStart); ok {
			value := direction
			next = &value
		}
		queryCopy := append([]string(nil), queries...)
		if len(queryCopy) == 0 {
			queryCopy = []string{}
		}
		actual = append(actual, altSearchComponentStep{
			Label: label, Width: width, Lines: lines, Query: component.Query(),
			ResultIndex: component.resultIndex, ResultCount: component.resultCount,
			PreviousButtonStart: component.previousButtonStart, PreviousButtonEnd: component.previousButtonEnd,
			NextButtonStart: component.nextButtonStart, NextButtonEnd: component.nextButtonEnd,
			DirectionAtPrevious: previous, DirectionAtNext: next, Queries: queryCopy,
		})
	}
	snapshot("empty-width-1", 1)
	snapshot("empty-width-16", 16)
	snapshot("empty-width-32", 32)
	component.HandleInput("Foo")
	component.SetResult(1, 4)
	snapshot("query-results", 40)
	component.HandleInput("\x1b[D")
	component.HandleInput("\x7f")
	snapshot("edit", 40)
	component.HandleInput("\x1b[200~ a\r\nb\tc \x1b[201~")
	component.SetResult(-1, 0)
	snapshot("paste-no-matches", 24)
	component.SetHoveredNavigationDirection(-1, true)
	snapshot("hover-previous", 40)
	component.SetHoveredNavigationDirection(1, true)
	snapshot("hover-next", 40)
	assertSearchJSONEqual(t, actual, reference.ComponentSteps)
}

func assertSearchJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("search reference mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}
