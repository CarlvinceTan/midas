//go:build !windows

package voice

import (
	"os"
	"os/exec"
	"syscall"
)

type voiceProcessOwnership struct{ pid int }

func prepareVoiceCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
func ownVoiceProcess(process *os.Process) (voiceProcessOwnership, error) {
	return voiceProcessOwnership{process.Pid}, nil
}
func (o voiceProcessOwnership) kill(process *os.Process) error {
	if err := syscall.Kill(-o.pid, syscall.SIGTERM); err != nil {
		return process.Kill()
	}
	return nil
}
func (o voiceProcessOwnership) close() {}
