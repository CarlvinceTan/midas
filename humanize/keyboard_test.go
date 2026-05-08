package humanize

import (
	"context"
	"testing"
)

type kbEvent struct {
	Op  string // "down", "up", "insert"
	Arg string
}

type fakeKeyboard struct {
	events []kbEvent
}

func (f *fakeKeyboard) Down(_ context.Context, key string) error {
	f.events = append(f.events, kbEvent{"down", key})
	return nil
}

func (f *fakeKeyboard) Up(_ context.Context, key string) error {
	f.events = append(f.events, kbEvent{"up", key})
	return nil
}

func (f *fakeKeyboard) InsertText(_ context.Context, text string) error {
	f.events = append(f.events, kbEvent{"insert", text})
	return nil
}

type fakeEvaluator struct {
	scripts []string
}

func (f *fakeEvaluator) Evaluate(_ context.Context, expression string, _ any) error {
	f.scripts = append(f.scripts, expression)
	return nil
}

func fastTypingConfig() Config {
	cfg := DefaultConfig()
	// Strip all sleeps so tests run instantly.
	cfg.TypingDelay = 0
	cfg.TypingDelaySpread = 0
	cfg.TypingPauseChance = 0
	cfg.TypingPauseRange = Range{0, 0}
	cfg.ShiftDownDelay = Range{0, 0}
	cfg.ShiftUpDelay = Range{0, 0}
	cfg.KeyHold = Range{0, 0}
	return cfg
}

func TestTypeNormalCharacters(t *testing.T) {
	kb := &fakeKeyboard{}
	if err := Type(context.Background(), nil, kb, "abc", fastTypingConfig()); err != nil {
		t.Fatalf("Type returned error: %v", err)
	}
	want := []kbEvent{
		{"down", "a"}, {"up", "a"},
		{"down", "b"}, {"up", "b"},
		{"down", "c"}, {"up", "c"},
	}
	if len(kb.events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(kb.events), len(want), kb.events)
	}
	for i, ev := range kb.events {
		if ev != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, ev, want[i])
		}
	}
}

func TestTypeWrapsCapitalsWithShift(t *testing.T) {
	kb := &fakeKeyboard{}
	if err := Type(context.Background(), nil, kb, "Ab", fastTypingConfig()); err != nil {
		t.Fatalf("Type returned error: %v", err)
	}
	want := []kbEvent{
		{"down", "Shift"}, {"down", "A"}, {"up", "A"}, {"up", "Shift"},
		{"down", "b"}, {"up", "b"},
	}
	if len(kb.events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(kb.events), len(want), kb.events)
	}
	for i, ev := range kb.events {
		if ev != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, ev, want[i])
		}
	}
}

func TestTypeShiftSymbolUsesInsertAndEvaluator(t *testing.T) {
	kb := &fakeKeyboard{}
	eval := &fakeEvaluator{}
	if err := Type(context.Background(), eval, kb, "@", fastTypingConfig()); err != nil {
		t.Fatalf("Type returned error: %v", err)
	}
	// Expect: Shift down → InsertText("@") → Shift up
	if len(kb.events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(kb.events), kb.events)
	}
	if kb.events[0] != (kbEvent{"down", "Shift"}) {
		t.Errorf("event[0] = %+v, want Shift down", kb.events[0])
	}
	if kb.events[1] != (kbEvent{"insert", "@"}) {
		t.Errorf("event[1] = %+v, want insert @", kb.events[1])
	}
	if kb.events[2] != (kbEvent{"up", "Shift"}) {
		t.Errorf("event[2] = %+v, want Shift up", kb.events[2])
	}
	if len(eval.scripts) != 1 {
		t.Errorf("evaluator called %d times; want 1", len(eval.scripts))
	}
}

func TestJsQuoteEscapesQuotesAndBackslashes(t *testing.T) {
	cases := map[string]string{
		`"`:    `"\""`,
		`\`:    `"\\"`,
		`a"b`:  `"a\"b"`,
		`<`:    `"<"`,
		``:     `""`,
		`abc`:  `"abc"`,
	}
	for in, want := range cases {
		if got := jsQuote(in); got != want {
			t.Errorf("jsQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
