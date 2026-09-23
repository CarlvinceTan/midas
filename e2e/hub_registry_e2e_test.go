package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registryHub starts a hub whose configured registry is a file the test wrote,
// which is the "install a bridge by name" path: no source is passed by the caller.
func registryHub(t *testing.T, registry map[string]any) *server {
	t.Helper()
	config := t.TempDir()
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(config, "registry.json")
	if err := os.WriteFile(registryPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// The hub reads its registry from its own configuration, which is hub.json when
	// it runs on its own.
	hubConfig := fmt.Sprintf(`{"registry":%q}`, registryPath)
	if err := os.WriteFile(filepath.Join(config, "hub.json"), []byte(hubConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return startServerIn(t, "hub", config, nil)
}

// TestHubInstallsABridgeFromARegistry: with a registry configured, an agent
// installs by name instead of being told a path, which is how a real deployment
// hands bridges to its agents.
func TestHubInstallsABridgeFromARegistry(t *testing.T) {
	source := binaries["mock-bridge"]
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	// One bridge is served from an HTTP URL, which is how a registry actually hands
	// a binary over: the hub fetches it and verifies the checksum.
	files := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(data)
	}))
	defer files.Close()

	instance := registryHub(t, map[string]any{"bridges": []map[string]any{
		{"name": "local", "description": "From this machine", "source": source,
			"checksum": hex.EncodeToString(sum[:]), "command": []string{"mock-bridge"}},
		{"name": "remote", "description": "Fetched over HTTP", "source": files.URL + "/mock-bridge",
			"checksum": hex.EncodeToString(sum[:]), "command": []string{"mock-bridge"}},
		{"name": "broken", "source": source, "checksum": strings.Repeat("0", 64), "command": []string{"mock-bridge"}},
	}})

	available := callTool(t, instance, "hub_bridges", map[string]any{"action": "available"})
	entries := list(t, available, "available")
	if len(entries) != 3 {
		t.Fatalf("available = %#v", available)
	}
	if field(t, entries[0], "description") != "From this machine" {
		t.Fatalf("a registry entry lost its description: %#v", entries[0])
	}

	// Installing by name, with no source, uses the registry.
	callTool(t, instance, "hub_bridges", map[string]any{"action": "install", "name": "local"})
	callTool(t, instance, "hub_bridges", map[string]any{"action": "install", "name": "remote"})
	listed := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	names := []string{}
	for _, bridge := range list(t, listed, "bridges") {
		names = append(names, field(t, bridge, "name"))
	}
	if len(names) != 2 {
		t.Fatalf("bridges = %#v", names)
	}
	// The fetched bridge is a real binary: both installed bridges answer hello, and
	// every account names the bridge it belongs to.
	accounts := callTool(t, instance, "hub_accounts", map[string]any{})
	byBridge := map[string]int{}
	for _, account := range list(t, accounts, "accounts") {
		byBridge[field(t, account, "bridge")]++
	}
	if byBridge["local"] != 2 || byBridge["remote"] != 2 {
		t.Fatalf("accounts per bridge = %#v", byBridge)
	}
	// Installed bridges are marked as such in the catalog.
	available = callTool(t, instance, "hub_bridges", map[string]any{"action": "available"})
	for _, entry := range list(t, available, "available") {
		object, _ := entry.(map[string]any)
		if field(t, entry, "name") == "local" && (object == nil || object["installed"] != true) {
			t.Fatalf("an installed bridge is not marked: %#v", entry)
		}
	}

	// A name the registry does not have is refused, and says what it does have.
	message := callToolError(t, instance, "hub_bridges", map[string]any{"action": "install", "name": "absent"})
	if !strings.Contains(message, "absent") || !strings.Contains(message, "local") {
		t.Fatalf("unknown name = %q", message)
	}
	// A registry entry whose checksum does not match is refused rather than
	// installed: the hub does not run a binary it cannot verify.
	if message := callToolError(t, instance, "hub_bridges", map[string]any{"action": "install", "name": "broken"}); strings.TrimSpace(message) == "" {
		t.Fatal("a bridge with a bad checksum was installed")
	}
}

// TestHubWatchesAThreadOverTheProtocol: a client subscribes to a conversation and
// is told when it changes, which is how a chat client stays current. The thread id
// carries a character a URI reserves, so this also proves the resource URIs a
// client has to use.
func TestHubWatchesAThreadOverTheProtocol(t *testing.T) {
	updates := make(chan string, 8)
	instance := startServerWithClient(t, "hub", t.TempDir(), nil, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
			select {
			case updates <- request.Params.URI:
			default:
			}
		},
	})
	installMock(t, instance, nil)

	// Reading the thread is what makes the bridge start and answer.
	callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "dm:alice"})
	escaped := "hub://threads/personal/dm%3Aalice"
	if err := instance.Session.Subscribe(context.Background(), &mcp.SubscribeParams{URI: "hub://threads/personal/echo"}); err != nil {
		t.Fatal(err)
	}
	// Sending to the echo thread brings a reply back, which is a change to watch.
	callTool(t, instance, "hub_send", map[string]any{"account": "personal", "to": "echo", "text": "watch me"})

	select {
	case got := <-updates:
		if got != "hub://threads/personal/echo" {
			t.Fatalf("update for %q", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a subscribed client was not told about the reply")
	}

	// The resource carries the conversation the escaped URI names, so a client that
	// ignores notifications loses nothing.
	resource, err := instance.Session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: escaped})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "sent you the file") {
		t.Fatalf("thread resource = %#v", resource.Contents)
	}
	// An unescaped thread id is not addressable, which is why clients escape it.
	if _, err := instance.Session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "hub://threads/personal/dm:alice"}); err == nil {
		t.Fatal("an unescaped thread URI was accepted")
	}
}

// TestHubRefusesCallsItCannotServe: every tool that names an account or a bridge
// says so when the name is wrong, instead of answering with an empty result.
func TestHubRefusesCallsItCannotServe(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	for name, call := range map[string]struct {
		tool      string
		arguments map[string]any
		want      string
	}{
		"unknown account when sending": {"hub_send", map[string]any{"account": "nobody", "to": "alice", "text": "hi"}, "no such account"},
		"unknown account when reading": {"hub_messages", map[string]any{"account": "nobody"}, "no such account"},
		"unknown account when listing": {"hub_threads", map[string]any{"account": "nobody"}, "no such account"},
		"unknown bridge when sending":  {"hub_send", map[string]any{"bridge": "ghost", "account": "personal", "to": "alice", "text": "hi"}, "ghost"},
		"no recipient":                 {"hub_send", map[string]any{"account": "personal", "text": "hi"}, "to"},
		"unknown login":                {"hub_login", map[string]any{"action": "submit", "loginID": "login_999", "response": "x"}, "no such login"},
	} {
		message := callToolError(t, instance, call.tool, call.arguments)
		if !strings.Contains(strings.ToLower(message), strings.ToLower(call.want)) {
			t.Fatalf("%s = %q, want %q", name, message, call.want)
		}
	}

	// A login that cannot start is reported as a failed login with the reason, which
	// is what the panel a user watches renders.
	failed := loginOf(t, callTool(t, instance, "hub_login", map[string]any{"account": "nobody", "action": "start"}))
	if field(t, failed, "status") != "failed" || !strings.Contains(field(t, failed, "hint"), "no such account") {
		t.Fatalf("a login for an unknown account = %#v", failed)
	}
}

// TestHubHonoursTheMessageLimit: a client pages through a conversation, and the
// limit it asks for is the limit it gets.
func TestHubHonoursTheMessageLimit(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	all := callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "space:acme/ops"})
	if len(list(t, all, "messages")) < 3 {
		t.Fatalf("the scripted channel is too short to test a limit: %#v", all)
	}
	limited := callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "space:acme/ops", "limit": 2})
	if messages := list(t, limited, "messages"); len(messages) != 2 {
		t.Fatalf("limit 2 returned %d messages", len(messages))
	}
	// A thread with nothing in it is an empty list, not an error.
	empty := callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "space:acme/general"})
	if len(list(t, empty, "messages")) != 0 {
		t.Fatalf("an empty channel = %#v", empty)
	}
}

// TestHubReportsABridgeWhoseStreamBreaks: a bridge that writes a line the framing
// cannot hold is not a bridge that exited cleanly. The reason has to reach the
// caller and the bridge has to stop, rather than being reported as running with
// nothing explaining why it stopped answering.
func TestHubReportsABridgeWhoseStreamBreaks(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, map[string]string{"HUB_MOCK_FLOOD": "5000000"})

	// The first call starts the bridge, whose hello answer overflows the framing.
	// Listing accounts still answers; the reason a bridge could not be read is in
	// the message rather than failing the call, because an environment with nothing
	// to list is a normal state.
	accounts := callTool(t, instance, "hub_accounts", map[string]any{})
	if len(list(t, accounts, "accounts")) != 0 {
		t.Fatalf("a broken stream returned accounts: %#v", accounts)
	}
	if message, _ := accounts["message"].(string); !strings.Contains(message, "read from bridge") {
		t.Fatalf("the reason was lost: %#v", accounts)
	}
	// The hub reports the bridge as not running, with the reason kept.
	listed := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	bridges := list(t, listed, "bridges")
	if len(bridges) != 1 {
		t.Fatalf("bridges = %#v", listed)
	}
	if state := field(t, bridges[0], "state"); state == "running" {
		t.Fatalf("a bridge with a broken stream is reported as running: %#v", bridges[0])
	}
	if detail := field(t, bridges[0], "error"); !strings.Contains(detail, "read from bridge") {
		t.Fatalf("the reason was lost: %#v", bridges[0])
	}
	// Nothing of the bridge's process is left behind holding the pipe.
	waitFor(t, "the bridge process to be gone", func() bool {
		return !processRunning(t, binaries["mock-bridge"])
	})
}

// processRunning reports whether anything is still running with this executable.
func processRunning(t *testing.T, path string) bool {
	t.Helper()
	return exec.Command("pgrep", "-f", path).Run() == nil
}
