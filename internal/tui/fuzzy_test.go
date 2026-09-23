package tui

import (
	"reflect"
	"testing"
)

func TestFuzzyMatchReferenceBehavior(t *testing.T) {
	tests := []struct {
		query, text string
		matches     bool
	}{
		{"", "anything", true},
		{"oai", "OpenAI GPT", true},
		{"openrout g", "OpenRouter GPT", true},
		{"gpt4", "GPT 4.1", true},
		{"4gpt", "GPT 4.1", true},
		{"zzz", "OpenAI GPT", false},
	}
	for _, test := range tests {
		if got := FuzzyMatch(test.query, test.text).Matches; got != test.matches {
			t.Errorf("FuzzyMatch(%q, %q) = %v", test.query, test.text, got)
		}
	}
	items := []string{"Google Gemini", "OpenAI GPT", "Anthropic Claude"}
	if got := FuzzyFilter(items, "op g", func(value string) string { return value }); !reflect.DeepEqual(got, []string{"OpenAI GPT"}) {
		t.Fatalf("multi-token filter = %#v", got)
	}
}

func TestFuzzyFilterWithTyposMatchesSingleCommandMistakes(t *testing.T) {
	commands := []string{"model", "sessions", "stats", "exit"}
	tests := []struct {
		query string
		want  string
	}{
		{query: "exiit", want: "exit"},
		{query: "moedl", want: "model"},
		{query: "statd", want: "stats"},
		{query: "sesions", want: "sessions"},
	}
	for _, test := range tests {
		got := FuzzyFilterWithTypos(commands, test.query, func(value string) string { return value })
		if !reflect.DeepEqual(got, []string{test.want}) {
			t.Errorf("FuzzyFilterWithTypos(%q) = %#v, want %q", test.query, got, test.want)
		}
	}
}

func TestFuzzyFilterWithTyposRemainsConservative(t *testing.T) {
	commands := []string{"model", "stats", "exit"}
	if got := FuzzyFilterWithTypos(commands, "zzit", func(value string) string { return value }); len(got) != 0 {
		t.Fatalf("two-edit query unexpectedly matched %#v", got)
	}
	if got := FuzzyMatch("exiit", "exit"); got.Matches {
		t.Fatal("regular fuzzy matching unexpectedly enabled typo fallback")
	}
}
