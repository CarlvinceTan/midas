package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPoolIsLazyAndShared covers the pool's contract: nothing connects until an
// agent needs a tool, and every agent gets the same set afterwards.
func TestPoolIsLazyAndShared(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	// A working directory with no MCP configuration: the pool exists, and has
	// nothing to connect to.
	pool, err := NewPool(configDir, root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if names := pool.Names(); len(names) != 0 {
		t.Fatalf("names = %#v", names)
	}
	tools, err := pool.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %#v", tools)
	}
	// Asking twice returns the same empty set rather than reconnecting.
	again, err := pool.Tools(context.Background())
	if err != nil || len(again) != 0 {
		t.Fatalf("second ask = %#v, %v", again, err)
	}
}

// TestPoolReportsAConfiguredServerWithoutStartingIt checks the lazy claim at the
// level an operator sees it: a configured server is listed as disconnected until
// something asks for a tool.
func TestPoolReportsAConfiguredServerWithoutStartingIt(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".midas"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"mcpServers":{"unstartable":{"command":["/nonexistent/mcp-server"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".midas", "mcp.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(configDir, root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	names := pool.Names()
	if len(names) != 1 || names[0] != "unstartable" {
		t.Fatalf("names = %#v", names)
	}
	// Creating the pool started nothing: the state is reported, not triggered.
	for _, status := range pool.Statuses() {
		if status.State != "disconnected" {
			t.Fatalf("a server started before anyone asked: %#v", status)
		}
	}
	// Asking for tools attempts the connection, and a server that cannot start
	// does not take the others down with it: the call still returns tools.
	tools, err := pool.Tools(context.Background())
	if err != nil {
		t.Fatalf("a failing MCP server should not fail the pool: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %#v", tools)
	}
}

// TestPoolStopsIdleServersAndCanStartAgain is the other half of the objective's
// pooling requirement: lazily started, and stopped when nothing is using them.
func TestPoolStopsIdleServersAndCanStartAgain(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".midas"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"mcpServers":{"flaky":{"command":["/nonexistent/mcp-server"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".midas", "mcp.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(configDir, root)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// A pool that has been used, and then left alone, is reaped. A server that
	// never connected counts as nothing to stop, so the pool simply returns to its
	// unstarted state and the next ask reconnects.
	if _, err := pool.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopped := pool.Reap(time.Nanosecond); stopped != 0 {
		t.Fatalf("a failed connection reported %d stopped servers", stopped)
	}
	if idle := pool.IdleFor(); idle <= 0 {
		t.Fatalf("idle = %v, want the time since the last use", idle)
	}
	// Reaping with no elapsed idle time does nothing.
	if stopped := pool.Reap(time.Hour); stopped != 0 {
		t.Fatalf("reaped a pool that was just used: %d", stopped)
	}
	if _, err := pool.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
}
