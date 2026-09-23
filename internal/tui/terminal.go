package tui

import "time"

// Terminal is the renderer-facing terminal contract.
type Terminal interface {
	Start(onInput func(string), onResize func())
	Stop()
	DrainInput(maxDuration, idleDuration time.Duration) error
	Write(data string)
	Columns() int
	Rows() int
	KittyProtocolActive() bool
	MoveBy(lines int)
	HideCursor()
	ShowCursor()
	ClearLine()
	ClearFromCursor()
	ClearScreen()
	SetTitle(title string)
	SetProgress(active bool)
}

// TuiMode identifies regular or alternate-screen rendering.
type TuiMode string

const (
	ModeRegular    TuiMode = "regular"
	ModeFullscreen TuiMode = "fullscreen"
)

// StopOptions control renderer teardown.
type StopOptions struct {
	PreserveScreen bool
}
