package remote

import (
	"sync"
	"testing"
	"time"
)

type fakeTerminal struct {
	mu       sync.Mutex
	onInput  func(string)
	onResize func()
	writes   []string
	cols     int
	rows     int
}

func (f *fakeTerminal) Start(input func(string), resize func()) {
	f.onInput, f.onResize = input, resize
}
func (f *fakeTerminal) Stop() {}
func (f *fakeTerminal) DrainInput(time.Duration, time.Duration) error {
	return nil
}
func (f *fakeTerminal) Write(value string) {
	f.mu.Lock()
	f.writes = append(f.writes, value)
	f.mu.Unlock()
}
func (f *fakeTerminal) Columns() int              { return f.cols }
func (f *fakeTerminal) Rows() int                 { return f.rows }
func (f *fakeTerminal) KittyProtocolActive() bool { return false }
func (f *fakeTerminal) MoveBy(int)                {}
func (f *fakeTerminal) HideCursor()               {}
func (f *fakeTerminal) ShowCursor()               {}
func (f *fakeTerminal) ClearLine()                {}
func (f *fakeTerminal) ClearFromCursor()          {}
func (f *fakeTerminal) ClearScreen()              {}
func (f *fakeTerminal) SetTitle(string)           {}
func (f *fakeTerminal) SetProgress(bool)          {}

func TestTerminalMirrorsCurrentTUIAndRestoresLocalDimensions(t *testing.T) {
	local := &fakeTerminal{cols: 120, rows: 40}
	terminal := NewTerminal(local)
	inputs := make(chan string, 1)
	resizes := 0
	renders := 0
	terminal.Start(func(value string) { inputs <- value }, func() { resizes++ })
	terminal.SetRequestRender(func() { renders++ })
	client := terminal.Attach(64, 22)
	if got := <-client.Output(); got != "\x1b[2J\x1b[H" {
		t.Fatalf("initial clear = %q", got)
	}
	if terminal.Columns() != 64 || terminal.Rows() != 22 || resizes != 1 || renders != 1 {
		t.Fatalf("attached geometry = %dx%d resizes=%d renders=%d", terminal.Columns(), terminal.Rows(), resizes, renders)
	}
	terminal.Write("frame")
	if got := <-client.Output(); got != "frame" {
		t.Fatalf("remote frame = %q", got)
	}
	local.mu.Lock()
	if len(local.writes) != 1 || local.writes[0] != "frame" {
		t.Fatalf("local writes = %#v", local.writes)
	}
	local.mu.Unlock()
	client.Input("remote input")
	if got := <-inputs; got != "remote input" {
		t.Fatalf("input = %q", got)
	}
	client.Resize(90, 31)
	if terminal.Columns() != 90 || terminal.Rows() != 31 || resizes != 2 || renders != 2 {
		t.Fatalf("resized geometry = %dx%d resizes=%d renders=%d", terminal.Columns(), terminal.Rows(), resizes, renders)
	}
	client.Close()
	if terminal.Columns() != 120 || terminal.Rows() != 40 || resizes != 3 || renders != 3 {
		t.Fatalf("restored geometry = %dx%d resizes=%d renders=%d", terminal.Columns(), terminal.Rows(), resizes, renders)
	}
}

func TestTerminalDisconnectsStalledBrowserWithoutBlockingLocalRender(t *testing.T) {
	local := &fakeTerminal{cols: 80, rows: 24}
	terminal := NewTerminal(local)
	terminal.Start(func(string) {}, func() {})
	client := terminal.Attach(80, 24)
	<-client.Output()
	for index := 0; index < clientQueue+1; index++ {
		terminal.Write("x")
	}
	select {
	case _, open := <-client.Output():
		for open {
			_, open = <-client.Output()
		}
	case <-time.After(time.Second):
		t.Fatal("stalled browser was not disconnected")
	}
}
