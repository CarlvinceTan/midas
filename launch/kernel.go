package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"
)

type LaunchKernelOptions struct {
	Command          []string
	Env              map[string]string
	BaseURL          string
	Headers          map[string]string
	WSURL            string
	ConnectTimeoutMs int
	ShutdownCommand  []string
}

type LaunchedKernel struct {
	Cmd             *exec.Cmd
	ShutdownCommand []string

	closeOnce sync.Once
	closeErr  error
}

func LaunchKernel(ctx context.Context, opts LaunchKernelOptions) (*LaunchResult, error) {
	connectTimeout := 15 * time.Second
	if opts.ConnectTimeoutMs > 0 {
		connectTimeout = time.Duration(opts.ConnectTimeoutMs) * time.Millisecond
	}
	deadline := time.Now().Add(connectTimeout)

	kernel := &LaunchedKernel{
		ShutdownCommand: append([]string(nil), opts.ShutdownCommand...),
	}

	if len(opts.Command) > 0 {
		cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
		if len(opts.Env) > 0 {
			cmd.Env = os.Environ()
			for key, value := range opts.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		kernel.Cmd = cmd
	}

	wsURL := opts.WSURL
	var err error
	if wsURL == "" {
		if opts.BaseURL == "" {
			_ = kernel.Close()
			return nil, errors.New("kernel base URL or websocket URL is required")
		}

		wsURL, err = waitForWebSocketDebuggerURLAt(ctx, cdpEndpointOptions{
			BaseURL: opts.BaseURL,
			Headers: opts.Headers,
		}, deadline)
		if err != nil {
			_ = kernel.Close()
			return nil, err
		}
	}

	if err := waitForWebSocketReady(ctx, wsURL, deadline); err != nil {
		_ = kernel.Close()
		return nil, err
	}

	return &LaunchResult{
		WS:       wsURL,
		Resource: kernel,
		Kernel:   kernel,
	}, nil
}

func (k *LaunchedKernel) Close() error {
	if k == nil {
		return nil
	}

	k.closeOnce.Do(func() {
		if k.Cmd != nil {
			k.closeErr = politeTerminateProcess(k.Cmd, 7*time.Second)
		}
		if len(k.ShutdownCommand) > 0 {
			cmd := exec.Command(k.ShutdownCommand[0], k.ShutdownCommand[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil && k.closeErr == nil {
				k.closeErr = err
			}
		}
	})

	return k.closeErr
}
