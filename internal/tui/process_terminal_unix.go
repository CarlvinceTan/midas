//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tui

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func startTerminalInputReader(file *os.File, onData func([]byte)) (func(), error) {
	pipe := []int{0, 0}
	if err := unix.Pipe(pipe); err != nil {
		return nil, err
	}
	unix.CloseOnExec(pipe[0])
	unix.CloseOnExec(pipe[1])
	inputFD := int(file.Fd())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unix.Close(pipe[0])
		poll := []unix.PollFd{{Fd: int32(inputFD), Events: unix.POLLIN}, {Fd: int32(pipe[0]), Events: unix.POLLIN}}
		buffer := make([]byte, 32*1024)
		for {
			_, err := unix.Poll(poll, -1)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return
			}
			if poll[1].Revents&unix.POLLIN != 0 {
				return
			}
			if poll[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
				return
			}
			if poll[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
				count, readErr := unix.Read(inputFD, buffer)
				if count > 0 {
					data := append([]byte(nil), buffer[:count]...)
					onData(data)
				}
				if readErr != nil || count == 0 {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = unix.Write(pipe[1], []byte{1})
			_ = unix.Close(pipe[1])
			<-done
		})
	}, nil
}

func startTerminalResizeWatcher(_ *os.File, onResize func()) func() {
	channel := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(channel, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-channel:
				onResize()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { signal.Stop(channel); close(done) }) }
}

func refreshTerminalDimensions()                  { _ = syscall.Kill(os.Getpid(), syscall.SIGWINCH) }
func enableTerminalVT(_, _ *os.File) func() error { return nil }
