//go:build !unix

package e2e

import "os/exec"

// startInGroup is a no-op where the platform has no process groups.
func startInGroup(*exec.Cmd) {}

// stopGroup kills the child itself.
func stopGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_ = command.Wait()
}
