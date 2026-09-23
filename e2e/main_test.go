package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// binaries are built once for the whole package, so every test drives the code in
// this checkout rather than whatever happens to be installed.
var binaries = map[string]string{}

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "midas-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	for _, name := range []string{"control", "hub", "vault", "mock-bridge"} {
		binary := filepath.Join(directory, name)
		command := exec.Command("go", "build", "-o", binary, "./cmd/"+name)
		command.Dir = moduleRoot()
		if output, err := command.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: build %s: %v\n%s", name, err, output)
			os.Exit(1)
		}
		binaries[name] = binary
	}
	// The binaries are this run's only large artefact, so they are removed before
	// the process exits, which a defer cannot do once os.Exit is called.
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

// moduleRoot is the checkout this suite belongs to.
func moduleRoot() string {
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Dir(directory)
}

// server is one MCP server running over stdio with its own configuration.
type server struct {
	Name   string
	Config string
	// Media is a directory of this server's own, for a fixture that needs somewhere
	// to write files.
	Media   string
	Session *mcp.ClientSession
	// Stderr collects what the server logged, which a failure prints.
	Stderr *strings.Builder
}

// startServer runs one MCP server binary over stdio. env entries are added to the
// process environment, and may reference %CONFIG% and %MEDIA% to keep the test's
// directories out of the code.
func startServer(t *testing.T, name string, env map[string]string, args ...string) *server {
	t.Helper()
	return startServerIn(t, name, t.TempDir(), env, args...)
}

// startServerIn runs one MCP server against a configuration directory the caller
// owns, which is how a restart test hands the same state to a second process.
func startServerIn(t *testing.T, name, config string, env map[string]string, args ...string) *server {
	t.Helper()
	return startServerWithClient(t, name, config, env, nil, args...)
}

// startServerWithClient is startServerIn with client options, which a test that
// watches resources needs.
func startServerWithClient(t *testing.T, name, config string, env map[string]string, clientOptions *mcp.ClientOptions, args ...string) *server {
	t.Helper()
	binary, ok := binaries[name]
	if !ok {
		t.Fatalf("no binary for %s", name)
	}
	media := t.TempDir()
	command := exec.Command(binary, args...)
	environment := append([]string{}, os.Environ()...)
	environment = append(environment, "MIDAS_CONFIG_DIR="+config)
	for key, value := range env {
		value = strings.ReplaceAll(value, "%CONFIG%", config)
		value = strings.ReplaceAll(value, "%MEDIA%", media)
		environment = append(environment, key+"="+value)
	}
	command.Env = environment
	stderr := &strings.Builder{}
	command.Stderr = stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, clientOptions)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connect to %s: %v\n%s", name, err, stderr.String())
	}
	instance := &server{Name: name, Config: config, Media: media, Session: session, Stderr: stderr}
	t.Cleanup(func() {
		_ = session.Close()
		if t.Failed() && stderr.Len() > 0 {
			t.Logf("%s stderr:\n%s", name, stderr.String())
		}
	})
	return instance
}

// startHub runs the hub server, which is what most of these tests need.
func startHub(t *testing.T, env map[string]string) *server {
	t.Helper()
	return startServer(t, "hub", env)
}

// callTool calls one tool and returns its structured content as a value. A failure
// includes the tool's own error text, which is what makes a red test readable.
func callTool(t *testing.T, instance *server, name string, arguments map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := instance.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, instance.Stderr.String())
	}
	if result.IsError {
		t.Fatalf("%s failed: %s", name, toolText(result))
	}
	if result.StructuredContent == nil {
		// A tool that answers with text only: hand the text back under one key.
		return map[string]any{"text": toolText(result)}
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return decoded
}

// callToolError calls a tool that is expected to fail and returns the message.
func callToolError(t *testing.T, instance *server, name string, arguments map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := instance.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err == nil && result != nil && !result.IsError {
		t.Fatalf("%s was expected to fail but succeeded: %s", name, toolText(result))
	}
	if err != nil {
		return err.Error()
	}
	return toolText(result)
}

// toolText flattens a tool result into text for assertions and messages.
func toolText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var builder strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(value.Text)
		}
	}
	return builder.String()
}

// list returns a slice from a decoded result. A key that is absent means an empty
// list: several tools answer with only a message when there is nothing to report.
func list(t *testing.T, result map[string]any, key string) []any {
	t.Helper()
	value, ok := result[key]
	if !ok || value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not a list: %#v", key, value)
	}
	return items
}

// field reads a nested string field, e.g. field(t, item, "kind").
func field(t *testing.T, item any, key string) string {
	t.Helper()
	object, ok := item.(map[string]any)
	if !ok {
		t.Fatalf("item is not an object: %#v", item)
	}
	value, ok := object[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// writeFile writes a fixture file and returns its path.
func writeFile(t *testing.T, directory, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// get reads a URL as text, which is how a test checks that a service a tool
// reported is really answering.
func get(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	buffer := make([]byte, 64*1024)
	count, _ := response.Body.Read(buffer)
	return string(buffer[:count])
}

// waitFor polls until the condition holds, which is how a test observes an update
// that arrives asynchronously over the bridge.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
