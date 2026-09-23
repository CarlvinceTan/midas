//go:build linux

package tui

import "golang.org/x/sys/unix"

func readTermiosForTest(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TCGETS) }
