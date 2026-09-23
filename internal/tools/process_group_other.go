//go:build !unix

package tools

import "os/exec"

// applyProcessGroup is a no-op where the platform has no process groups: the
// command is still killed on cancellation, its children are not.
func applyProcessGroup(*exec.Cmd) {}

// killProcessGroup kills the command itself.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
