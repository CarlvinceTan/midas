//go:build darwin

package tui

import (
	"testing"

	"golang.org/x/sys/unix"
)

func readTermiosForTest(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TIOCGETA)
}

func TestDarwinNativeShiftProbeLoads(t *testing.T) {
	_ = nativeShiftPressed()
	if coreGraphicsFlagsState == nil {
		t.Fatal("CoreGraphics modifier-state function was not loaded")
	}
}
