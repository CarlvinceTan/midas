//go:build windows

package tui

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func startTerminalInputReader(file *os.File, onData func([]byte)) (func(), error) {
	var duplicatedHandle windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, windows.Handle(file.Fd()), process, &duplicatedHandle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	reader := os.NewFile(uintptr(duplicatedHandle), file.Name())
	if reader == nil {
		_ = windows.CloseHandle(duplicatedHandle)
		return nil, windows.ERROR_INVALID_HANDLE
	}
	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32*1024)
		for !stopped.Load() {
			count, err := reader.Read(buffer)
			if count > 0 && !stopped.Load() {
				onData(append([]byte(nil), buffer[:count]...))
			}
			if err != nil {
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			stopped.Store(true)
			_ = windows.CancelIoEx(duplicatedHandle, nil)
			_ = reader.Close()
			<-done
		})
	}, nil
}

func startTerminalResizeWatcher(file *os.File, onResize func()) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		previousColumns, previousRows, _ := term.GetSize(int(file.Fd()))
		for {
			select {
			case <-ticker.C:
				columns, rows, err := term.GetSize(int(file.Fd()))
				if err == nil && (columns != previousColumns || rows != previousRows) {
					previousColumns, previousRows = columns, rows
					onResize()
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func refreshTerminalDimensions() {}

func enableTerminalVT(stdin, stdout *os.File) func() error {
	inputHandle := windows.Handle(stdin.Fd())
	var inputMode uint32
	if windows.GetConsoleMode(inputHandle, &inputMode) == nil {
		_ = windows.SetConsoleMode(inputHandle, inputMode|0x0200)
	}
	outputHandle := windows.Handle(stdout.Fd())
	var outputMode uint32
	if windows.GetConsoleMode(outputHandle, &outputMode) != nil {
		return nil
	}
	if windows.SetConsoleMode(outputHandle, outputMode|0x0001|0x0004) != nil {
		return nil
	}
	return func() error { return windows.SetConsoleMode(outputHandle, outputMode) }
}

func nativeShiftPressed() bool {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return false
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	getAsyncKeyState := user32.NewProc("GetAsyncKeyState")
	for _, virtualKey := range []uintptr{0x10, 0xa0, 0xa1} {
		state, _, _ := getAsyncKeyState.Call(virtualKey)
		if state&0x8000 != 0 {
			return true
		}
	}
	return false
}
