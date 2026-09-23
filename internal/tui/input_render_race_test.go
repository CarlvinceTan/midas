package tui

import (
	"sync"
	"testing"
	"time"
)

// raceTerminal is a terminal double that is safe to use from several goroutines,
// which the reference fake is not: this test deliberately drives the real
// goroutines.
type raceTerminal struct {
	mu      sync.Mutex
	columns int
	rows    int
	writes  int
	onInput func(string)
}

func (t *raceTerminal) Start(onInput func(string), _ func()) { t.onInput = onInput }
func (t *raceTerminal) Stop()                                {}
func (t *raceTerminal) DrainInput(time.Duration, time.Duration) error {
	return nil
}
func (t *raceTerminal) Write(string) {
	t.mu.Lock()
	t.writes++
	t.mu.Unlock()
}
func (t *raceTerminal) Columns() int              { return t.columns }
func (t *raceTerminal) Rows() int                 { return t.rows }
func (t *raceTerminal) KittyProtocolActive() bool { return false }
func (t *raceTerminal) MoveBy(int)                {}
func (t *raceTerminal) HideCursor()               {}
func (t *raceTerminal) ShowCursor()               {}
func (t *raceTerminal) ClearLine()                {}
func (t *raceTerminal) ClearFromCursor()          {}
func (t *raceTerminal) ClearScreen()              {}
func (t *raceTerminal) SetTitle(string)           {}
func (t *raceTerminal) SetProgress(bool)          {}

// TestConcurrentInputAndRenderKeepsStateSingleThreaded is the regression test for
// the TUI's threading model: input arrives on the terminal reader while frames are
// painted on scheduler goroutines, and both must end up on the scheduler loop so a
// component is never edited while it is painted. Run under -race.
//
// It uses the default scheduler on purpose, because that is the one production
// uses; the deterministic test scheduler bypasses the loop.
func TestConcurrentInputAndRenderKeepsStateSingleThreaded(t *testing.T) {
	terminal := &raceTerminal{columns: 60, rows: 14}
	screen := NewTuiAltScreen(terminal, TuiAltScreenOptions{})
	editor := NewEditor(EditorOptions{})
	screen.AddChild(editor)
	screen.SetFocus(editor)
	screen.Start()
	defer screen.Stop(StopOptions{})

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			// Input arrives the way a real terminal delivers it.
			terminal.onInput("a")
			terminal.onInput("\x1b[B")
			terminal.onInput("\x7f")
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			screen.RequestRender(true)
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(150 * time.Millisecond)
	close(done)
	wg.Wait()

	terminal.mu.Lock()
	writes := terminal.writes
	terminal.mu.Unlock()
	if writes == 0 {
		t.Fatal("the probe rendered nothing")
	}
}
