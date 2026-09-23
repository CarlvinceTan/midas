//go:build windows

package remote

import (
	"fmt"
	"io"
	"runtime"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	executionContinuous      = 0x80000000
	executionSystemRequired  = 0x00000001
	executionDisplayRequired = 0x00000002
)

var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

type windowsInhibitor struct {
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (i *windowsInhibitor) Close() error {
	i.once.Do(func() { close(i.done) })
	<-i.stopped
	return nil
}

// PreventSleep holds the execution-state request on one locked Windows thread
// until the returned closer is released.
func PreventSleep() (io.Closer, error) {
	started := make(chan error, 1)
	inhibitor := &windowsInhibitor{done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(inhibitor.stopped)
		result, _, callErr := setThreadExecutionState.Call(executionContinuous | executionSystemRequired | executionDisplayRequired)
		if result == 0 {
			if callErr == syscall.Errno(0) {
				callErr = syscall.EINVAL
			}
			started <- fmt.Errorf("SetThreadExecutionState: %w", callErr)
			return
		}
		started <- nil
		<-inhibitor.done
		_, _, _ = setThreadExecutionState.Call(executionContinuous)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return inhibitor, nil
}
