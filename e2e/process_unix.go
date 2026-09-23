//go:build unix

package e2e

import (
	"errors"
	"os/exec"
	"syscall"
)

// startInGroup puts a child in its own process group, which matters for a browser:
// the binary on PATH is usually a wrapper that forks the real browser, so killing
// the wrapper alone leaves the browser running and holding its profile open.
func startInGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stopGroup ends the child and everything it started. A group that is already gone
// is not an error.
func stopGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}
