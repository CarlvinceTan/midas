//go:build unix

package tools

import "syscall"

// makeFIFO creates a named pipe, used to prove the read tool refuses one.
func makeFIFO(path string) error { return syscall.Mkfifo(path, 0o600) }
