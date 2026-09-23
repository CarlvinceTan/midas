//go:build darwin

package remote

import (
	"errors"
	"io"
	"os/exec"
	"sync"
)

type commandInhibitor struct {
	command *exec.Cmd
	once    sync.Once
}

func (i *commandInhibitor) Close() error {
	var result error
	i.once.Do(func() {
		if i.command.Process != nil {
			result = i.command.Process.Kill()
		}
		waitErr := i.command.Wait()
		var exitError *exec.ExitError
		if waitErr != nil && !errors.As(waitErr, &exitError) {
			result = errors.Join(result, waitErr)
		}
	})
	return result
}

// PreventSleep holds macOS awake for the lifetime of the returned closer.
func PreventSleep() (io.Closer, error) {
	command := exec.Command("caffeinate", "-dimsu")
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &commandInhibitor{command: command}, nil
}
