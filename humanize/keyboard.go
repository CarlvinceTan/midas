package humanize

import (
	"context"
	"math/rand/v2"
	"unicode"
)

// RawKeyboard is the low-level keyboard surface humanize dispatches against.
// Down/Up emit Input.dispatchKeyEvent; InsertText emits Input.insertText for
// shifted symbols where dispatching keys directly produces the wrong glyph.
type RawKeyboard interface {
	Down(ctx context.Context, key string) error
	Up(ctx context.Context, key string) error
	InsertText(ctx context.Context, text string) error
}

// Evaluator runs JavaScript in the page. Used to fire synthetic keydown/keyup
// for shifted symbols, since InsertText alone doesn't produce key events that
// listeners can hear.
type Evaluator interface {
	Evaluate(ctx context.Context, expression string, result any) error
}

// shiftSymbols are the US-QWERTY characters that require holding Shift.
var shiftSymbols = map[rune]bool{
	'@': true, '#': true, '!': true, '$': true, '%': true, '^': true,
	'&': true, '*': true, '(': true, ')': true, '_': true, '+': true,
	'{': true, '}': true, '|': true, ':': true, '"': true, '<': true,
	'>': true, '?': true, '~': true,
}

// Type emits text character-by-character with realistic per-key timing,
// thinking pauses, and shift handling. Capitals get wrapped with Shift
// down/up; shift-symbols are inserted via InsertText with a synthetic
// keydown/keyup pair so listeners observe the keystroke.
func Type(ctx context.Context, eval Evaluator, raw RawKeyboard, text string, cfg Config) error {
	runes := []rune(text)
	for i, ch := range runes {
		var err error
		switch {
		case unicode.IsUpper(ch) && unicode.IsLetter(ch):
			err = typeShifted(ctx, raw, string(ch), cfg)
		case shiftSymbols[ch]:
			err = typeShiftSymbol(ctx, eval, raw, string(ch), cfg)
		default:
			err = typeNormal(ctx, raw, string(ch), cfg)
		}
		if err != nil {
			return err
		}
		if i < len(runes)-1 {
			if err := interCharDelay(ctx, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeNormal(ctx context.Context, raw RawKeyboard, ch string, cfg Config) error {
	if err := raw.Down(ctx, ch); err != nil {
		return err
	}
	if err := SleepMs(ctx, RandRange(cfg.KeyHold)); err != nil {
		return err
	}
	return raw.Up(ctx, ch)
}

func typeShifted(ctx context.Context, raw RawKeyboard, ch string, cfg Config) error {
	if err := raw.Down(ctx, "Shift"); err != nil {
		return err
	}
	if err := SleepMs(ctx, RandRange(cfg.ShiftDownDelay)); err != nil {
		return err
	}
	if err := raw.Down(ctx, ch); err != nil {
		return err
	}
	if err := SleepMs(ctx, RandRange(cfg.KeyHold)); err != nil {
		return err
	}
	if err := raw.Up(ctx, ch); err != nil {
		return err
	}
	if err := SleepMs(ctx, RandRange(cfg.ShiftUpDelay)); err != nil {
		return err
	}
	return raw.Up(ctx, "Shift")
}

func typeShiftSymbol(ctx context.Context, eval Evaluator, raw RawKeyboard, ch string, cfg Config) error {
	if err := raw.Down(ctx, "Shift"); err != nil {
		return err
	}
	if err := SleepMs(ctx, RandRange(cfg.ShiftDownDelay)); err != nil {
		return err
	}
	if err := raw.InsertText(ctx, ch); err != nil {
		return err
	}
	if eval != nil {
		// Best-effort dispatch so listeners hear the keystroke. Mirrors the
		// Python wrapper; failures here aren't fatal — the text is already in.
		_ = eval.Evaluate(ctx, dispatchKeyEventScript(ch), nil)
	}
	if err := SleepMs(ctx, RandRange(cfg.ShiftUpDelay)); err != nil {
		return err
	}
	return raw.Up(ctx, "Shift")
}

func interCharDelay(ctx context.Context, cfg Config) error {
	if rand.Float64() < cfg.TypingPauseChance {
		return SleepMs(ctx, RandRange(cfg.TypingPauseRange))
	}
	delay := cfg.TypingDelay + (rand.Float64()-0.5)*2*cfg.TypingDelaySpread
	if delay < 10 {
		delay = 10
	}
	return SleepMs(ctx, delay)
}

func dispatchKeyEventScript(ch string) string {
	quoted := jsQuote(ch)
	return `(() => {
    const el = document.activeElement;
    if (el) {
        el.dispatchEvent(new KeyboardEvent('keydown', { key: ` + quoted + `, bubbles: true }));
        el.dispatchEvent(new KeyboardEvent('keyup', { key: ` + quoted + `, bubbles: true }));
    }
})()`
}

func jsQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}
