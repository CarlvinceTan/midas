//go:build !darwin && !windows

package tui

func nativeShiftPressed() bool { return false }
