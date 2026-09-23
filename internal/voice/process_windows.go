//go:build windows

package voice

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"unsafe"
)

type voiceProcessOwnership struct{ job windows.Handle }

func prepareVoiceCommand(_ *exec.Cmd) {}
func ownVoiceProcess(process *os.Process) (voiceProcessOwnership, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return voiceProcessOwnership{}, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return voiceProcessOwnership{}, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return voiceProcessOwnership{}, err
	}
	defer windows.CloseHandle(handle)
	if err = windows.AssignProcessToJobObject(job, handle); err != nil {
		windows.CloseHandle(job)
		return voiceProcessOwnership{}, err
	}
	return voiceProcessOwnership{job}, nil
}
func (o voiceProcessOwnership) kill(process *os.Process) error {
	err := windows.TerminateJobObject(o.job, 1)
	if err != nil {
		return process.Kill()
	}
	return nil
}
func (o voiceProcessOwnership) close() {
	if o.job != 0 {
		_ = windows.CloseHandle(o.job)
	}
}
