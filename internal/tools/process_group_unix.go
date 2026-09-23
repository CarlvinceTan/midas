//go:build unix

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

// applyProcessGroup puts the command in its own process group, so cancelling it can
// signal the whole tree rather than just the shell.
func applyProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the command's process group. A group that is already gone
// is not an error: the command may have exited between the check and the signal.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
