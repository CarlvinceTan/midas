//go:build !unix

package main

import "os"

// reloadSignals is empty where the platform has no SIGHUP: the environment can
// still be reloaded over the API.
func reloadSignals() []os.Signal { return nil }
