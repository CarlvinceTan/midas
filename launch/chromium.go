package launch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PolymuxOrg/midas/debug"
)

type LaunchLocalOptions struct {
	ChromePath          string
	ChromeFlags         []string
	Headless            *bool
	UserDataDir         string
	PreserveUserDataDir bool
	Port                int
	ConnectTimeoutMs    int
	EnableCrashCleanup  *bool
	HandleSignals       *bool
	HasTouch            bool
}

type LaunchedChrome struct {
	Cmd                 *exec.Cmd
	Port                int
	UserDataDir         string
	CreatedTempProfile  bool
	PreserveUserDataDir bool

	closeOnce     sync.Once
	closeErr      error
	supervisor    *shutdownSupervisor
	handleSignals bool
}

func LaunchLocalChrome(ctx context.Context, opts LaunchLocalOptions) (*LaunchResult, error) {
	connectTimeout := 15 * time.Second
	if opts.ConnectTimeoutMs > 0 {
		connectTimeout = time.Duration(opts.ConnectTimeoutMs) * time.Millisecond
	}
	deadline := time.Now().Add(connectTimeout)

	port := opts.Port
	if port == 0 {
		var err error
		port, err = getFreePort()
		if err != nil {
			return nil, err
		}
	}

	chromePath := strings.TrimSpace(opts.ChromePath)
	if chromePath == "" {
		return nil, errors.New("launch: ChromePath must be set to the Chromium or Chrome executable path")
	}

	headless := false
	if opts.Headless != nil {
		headless = *opts.Headless
	}

	flags := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-dev-shm-usage",
		"--site-per-process",
	}
	if headless {
		flags = append(flags, "--headless=new")
	}
	if opts.HasTouch {
		flags = append(flags, "--touch-events=enabled")
	}
	// Ubuntu 23.10+ disables unprivileged user namespaces under AppArmor, so
	// Chromium aborts at startup with "No usable sandbox!". macOS keeps its
	// own sandbox; only Linux needs the opt-out here.
	if runtime.GOOS == "linux" {
		flags = append(flags, "--no-sandbox")
	}

	userDataDir := opts.UserDataDir
	createdTempProfile := false
	if userDataDir == "" {
		tempDir, err := os.MkdirTemp("", "polymux-chrome-profile-*")
		if err != nil {
			return nil, err
		}
		userDataDir = tempDir
		createdTempProfile = true
	}
	flags = append(flags, "--user-data-dir="+userDataDir)
	flags = append(flags, opts.ChromeFlags...)

	debug.Printf("launching Chromium path=%s port=%d headless=%v userDataDir=%s connectTimeout=%v",
		chromePath, port, headless, userDataDir, connectTimeout)

	cmd := exec.CommandContext(ctx, chromePath, flags...)
	if err := cmd.Start(); err != nil {
		if createdTempProfile && !opts.PreserveUserDataDir {
			_ = os.RemoveAll(userDataDir)
		}
		return nil, err
	}
	if cmd.Process != nil {
		debug.Printf("Chromium process started pid=%d", cmd.Process.Pid)
	}

	handleSignals := true
	if opts.HandleSignals != nil {
		handleSignals = *opts.HandleSignals
	}

	chrome := &LaunchedChrome{
		Cmd:                 cmd,
		Port:                port,
		UserDataDir:         userDataDir,
		CreatedTempProfile:  createdTempProfile,
		PreserveUserDataDir: opts.PreserveUserDataDir,
		handleSignals:       handleSignals,
	}

	enableCrashCleanup := true
	if opts.EnableCrashCleanup != nil {
		enableCrashCleanup = *opts.EnableCrashCleanup
	}
	if enableCrashCleanup {
		supervisor, err := startShutdownSupervisor(chrome)
		if err != nil {
			_ = chrome.Close()
			return nil, err
		}
		chrome.supervisor = supervisor
	}
	if chrome.handleSignals {
		registerSignalCleanup(chrome)
	}

	debug.Printf("waiting for /json/version until %s", deadline.Format(time.RFC3339))
	wsURL, err := waitForWebSocketDebuggerURL(ctx, port, deadline)
	if err != nil {
		debug.Printf("/json/version failed: %v", err)
		_ = chrome.Close()
		return nil, err
	}
	debug.Printf("received WebSocket debugger URL: %s", wsURL)

	debug.Printf("probing CDP WebSocket until %s", deadline.Format(time.RFC3339))
	if err := waitForWebSocketReady(ctx, wsURL, deadline); err != nil {
		debug.Printf("CDP WebSocket probe failed: %v", err)
		_ = chrome.Close()
		return nil, err
	}
	debug.Printf("CDP WebSocket accepts connections")

	return &LaunchResult{WS: wsURL, Resource: chrome, Chrome: chrome}, nil
}

func (c *LaunchedChrome) Close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		unregisterSignalCleanup(c)
		if c.supervisor != nil {
			_ = c.supervisor.Stop()
		}
		c.closeErr = cleanupLocalBrowser(c)
	})

	return c.closeErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func getFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("failed to resolve tcp port")
	}
	return addr.Port, nil
}

// ChromePathFromFlagsOrEnv returns flagValue if set, else MIDAS_CHROMIUM_PATH or CHROMIUM_PATH.
func ChromePathFromFlagsOrEnv(flagValue string) (string, error) {
	if s := strings.TrimSpace(flagValue); s != "" {
		return s, nil
	}
	for _, key := range []string{"MIDAS_CHROMIUM_PATH", "CHROMIUM_PATH"} {
		if s := strings.TrimSpace(os.Getenv(key)); s != "" {
			return s, nil
		}
	}
	return "", errors.New("chromium path required: set --chrome-path or CHROMIUM_PATH (or MIDAS_CHROMIUM_PATH)")
}

func cleanupLocalBrowser(chrome *LaunchedChrome) error {
	if chrome == nil {
		return nil
	}

	var firstErr error
	if err := politeTerminateProcess(chrome.Cmd, 7*time.Second); err != nil {
		firstErr = err
	}
	if chrome.CreatedTempProfile && !chrome.PreserveUserDataDir && chrome.UserDataDir != "" {
		if err := os.RemoveAll(chrome.UserDataDir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func politeTerminateProcess(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return nil
	}

	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !isProcessGoneError(err) {
		return err
	}

	select {
	case <-waitDone:
		return nil
	case <-time.After(timeout):
	}

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	<-waitDone
	return nil
}

func isProcessGoneError(err error) bool {
	var syscallErr *os.SyscallError
	return errors.As(err, &syscallErr) && errors.Is(syscallErr.Err, syscall.ESRCH)
}
