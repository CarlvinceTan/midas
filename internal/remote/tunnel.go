package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var quickTunnelURL = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`)

type commandTunnel struct {
	url      string
	cancel   context.CancelFunc
	done     <-chan error
	closeOne sync.Once
}

func (t *commandTunnel) URL() string { return t.url }

func (t *commandTunnel) Close() error {
	var result error
	t.closeOne.Do(func() {
		t.cancel()
		select {
		case err := <-t.done:
			if err != nil && !isExpectedTunnelExit(err) {
				result = err
			}
		case <-time.After(3 * time.Second):
			result = errors.New("remote: timed out stopping public tunnel")
		}
	})
	return result
}

type tunnelOutput struct {
	mu    sync.Mutex
	value strings.Builder
	found chan string
}

func newTunnelOutput() *tunnelOutput {
	return &tunnelOutput{found: make(chan string, 1)}
}

func (o *tunnelOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	if o.value.Len() < 64<<10 {
		remaining := (64 << 10) - o.value.Len()
		if len(value) > remaining {
			o.value.Write(value[:remaining])
		} else {
			o.value.Write(value)
		}
	}
	text := o.value.String()
	o.mu.Unlock()
	if url := quickTunnelURL.FindString(text); url != "" {
		select {
		case o.found <- url:
		default:
		}
	}
	return len(value), nil
}

func (o *tunnelOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.value.String()
}

// StartQuickTunnel starts an ephemeral Cloudflare Quick Tunnel. Quick tunnels
// require no Cloudflare account, project or stored credential.
func StartQuickTunnel(ctx context.Context, port int) (Tunnel, error) {
	bin := strings.TrimSpace(os.Getenv("MIDAS_CLOUDFLARED_BIN"))
	if bin == "" {
		var err error
		bin, err = exec.LookPath("cloudflared")
		if err != nil {
			return nil, errors.New("remote: cloudflared is not installed; install it once with 'brew install cloudflared' (no account is required)")
		}
	}
	commandContext, cancel := context.WithCancel(ctx)
	// Ignore any account-bound ~/.cloudflared/config.yml. A local named-tunnel
	// ingress can otherwise silently replace the requested loopback origin with
	// its own rules, defeating both the no-account contract and readiness check.
	command := exec.CommandContext(commandContext, bin, "--config", os.DevNull, "tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
	output := newTunnelOutput()
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("remote: start cloudflared: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case url := <-output.found:
		return &commandTunnel{url: url, cancel: cancel, done: done}, nil
	case err := <-done:
		cancel()
		return nil, fmt.Errorf("remote: cloudflared exited before publishing a link: %w: %s", err, strings.TrimSpace(output.String()))
	case <-timer.C:
		cancel()
		<-done
		return nil, fmt.Errorf("remote: cloudflared did not publish a link within 30 seconds: %s", strings.TrimSpace(output.String()))
	case <-ctx.Done():
		cancel()
		<-done
		return nil, ctx.Err()
	}
}

func isExpectedTunnelExit(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return true
	}
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}
