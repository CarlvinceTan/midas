//go:build unix

package main

import (
	"os"
	"syscall"
)

// reloadSignals are the signals that make the environment re-read its config.
// SIGHUP is the conventional one for "reload your configuration".
func reloadSignals() []os.Signal { return []os.Signal{syscall.SIGHUP} }
