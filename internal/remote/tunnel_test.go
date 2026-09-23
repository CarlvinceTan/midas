//go:build !windows

package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuickTunnelIgnoresAccountBoundCloudflaredConfig(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "fake-cloudflared")
	arguments := filepath.Join(directory, "arguments")
	source := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MIDAS_ARGS_FILE\"\nprintf 'https://account-free-test.trycloudflare.com\\n' >&2\nexec sleep 30\n"
	if err := os.WriteFile(script, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDAS_CLOUDFLARED_BIN", script)
	t.Setenv("MIDAS_ARGS_FILE", arguments)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tunnel, err := StartQuickTunnel(ctx, 8765)
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(data))
	want := []string{"--config", os.DevNull, "tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:8765"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("cloudflared arguments = %#v, want %#v", got, want)
	}
}
