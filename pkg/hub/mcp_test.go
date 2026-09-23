package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect starts an in-process MCP client against the hub, with its own session.
func connect(t *testing.T, instance *Hub) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := instance.server().Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s returned an error: %s", name, textOf(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestToolListIsFixedRegardlessOfBridges guards the property the prompt cache
// depends on: installing a bridge must not change which tools exist.
func TestToolListIsFixedRegardlessOfBridges(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)

	before := toolNames(t, session)
	want := "hub_accounts,hub_bridges,hub_login,hub_mark_read,hub_messages,hub_send,hub_threads"
	if strings.Join(before, ",") != want {
		t.Fatalf("tools = %#v", before)
	}
	installMock(t, instance)
	after := toolNames(t, session)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("the tool list changed when a bridge was installed:\n%v\n%v", before, after)
	}
}

// textOf renders a tool result's text content for failure messages.
func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, " ")
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	names := []string{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

func TestToolsExplainThemselvesWithNothingInstalled(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	accounts := callTool(t, session, "hub_accounts", map[string]any{})
	if !strings.Contains(accounts, "no bridges are installed") {
		t.Fatalf("accounts = %s", accounts)
	}
	bridges := callTool(t, session, "hub_bridges", map[string]any{"action": "list"})
	if !strings.Contains(bridges, "no bridges are installed") {
		t.Fatalf("bridges = %s", bridges)
	}
}

func TestToolsDriveABridgeEndToEnd(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	installMock(t, instance)

	accounts := callTool(t, session, "hub_accounts", map[string]any{})
	if !strings.Contains(accounts, `"id":"personal"`) || !strings.Contains(accounts, `"id":"work"`) {
		t.Fatalf("accounts = %s", accounts)
	}
	sent := callTool(t, session, "hub_send", map[string]any{"account": "personal", "to": "alice", "text": "hi"})
	if !strings.Contains(sent, `"text":"hi"`) {
		t.Fatalf("send = %s", sent)
	}
	history := callTool(t, session, "hub_messages", map[string]any{"account": "personal", "thread": "alice"})
	if !strings.Contains(history, `"text":"hi"`) {
		t.Fatalf("messages = %s", history)
	}
	// Stopping and starting again is a tool call too, and starts lazily.
	if stopped := callTool(t, session, "hub_bridges", map[string]any{"action": "stop", "name": "mock"}); !strings.Contains(stopped, "stopped") {
		t.Fatalf("stop = %s", stopped)
	}
	if listed := callTool(t, session, "hub_bridges", map[string]any{"action": "list"}); !strings.Contains(listed, `"state":"stopped"`) {
		t.Fatalf("list after stop = %s", listed)
	}
}

func TestLoginToolRendersQRRowsForOneSessionOnly(t *testing.T) {
	instance, _ := testHub(t)
	first := connect(t, instance)
	second := connect(t, instance)
	installMock(t, instance)

	started := callTool(t, first, "hub_login", map[string]any{"action": "start", "account": "personal", "render": true})
	if !strings.Contains(started, `"kind":"qr"`) {
		t.Fatalf("start = %s", started)
	}
	if !strings.Contains(started, `"rendered"`) {
		t.Fatalf("start did not render QR rows: %s", started)
	}
	var output struct {
		Logins []struct {
			ID string `json:"id"`
		} `json:"logins"`
	}
	if err := json.Unmarshal([]byte(started), &output); err != nil || len(output.Logins) != 1 {
		t.Fatalf("start output = %s (%v)", started, err)
	}
	loginID := output.Logins[0].ID

	// The session that started it can read it; another one cannot.
	if status := callTool(t, first, "hub_login", map[string]any{"action": "status", "loginID": loginID}); !strings.Contains(status, loginID) {
		t.Fatalf("owner status = %s", status)
	}
	result, err := second.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hub_login", Arguments: map[string]any{"action": "status", "loginID": loginID},
	})
	if err == nil && !result.IsError {
		t.Fatalf("a second session read someone else's login: %#v", result.StructuredContent)
	}
}

func TestThreadsAndMarkReadTools(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	installMock(t, instance)

	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "personal", To: "alice", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	threads := callTool(t, session, "hub_threads", map[string]any{"account": "personal"})
	if !strings.Contains(threads, `"id":"alice"`) || !strings.Contains(threads, `"account":"personal"`) {
		t.Fatalf("threads = %s", threads)
	}
	marked := callTool(t, session, "hub_mark_read", map[string]any{"account": "personal", "thread": "alice"})
	if !strings.Contains(marked, `"id":"alice"`) {
		t.Fatalf("mark read = %s", marked)
	}
	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "personal", To: "bob", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	listed := callTool(t, session, "hub_threads", map[string]any{"account": "personal"})
	if !strings.Contains(listed, `"id":"bob"`) {
		t.Fatalf("threads after a second send = %s", listed)
	}
}

func TestThreadResourceReadsStoredMessages(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	installMock(t, instance)
	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "personal", To: "dm:alice", Text: "resource hello"}); err != nil {
		t.Fatal(err)
	}
	result, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "hub://threads/personal/dm%3Aalice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 || !strings.Contains(result.Contents[0].Text, "resource hello") {
		t.Fatalf("resource = %#v", result.Contents)
	}
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "hub://threads/bad"}); err == nil {
		t.Fatal("a malformed thread URI was accepted")
	}
}

// TestBridgeEventsReachSubscribedClients covers push: a client subscribed to a
// thread resource is told when that thread changes, and a client that never
// subscribes still reads the same data through the tools.
func TestBridgeEventsReachSubscribedClients(t *testing.T) {
	instance, _ := testHub(t)
	updated := make(chan string, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "subscribed-agent", Version: "0"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
			select {
			case updated <- request.Params.URI:
			default:
			}
		},
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := instance.mcpServer().Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.Subscribe(context.Background(), &mcp.SubscribeParams{URI: "hub://threads/personal/dm%3Aalice"}); err != nil {
		t.Fatal(err)
	}
	binary := mockBridgePath(t)
	if _, err := instance.Install(context.Background(), InstallOptions{
		Name: "mock", Source: binary, Command: []string{"mock-bridge"},
		Env: map[string]string{"HUB_MOCK_INJECT": "20ms"},
	}); err != nil {
		t.Fatal(err)
	}
	// Starting the bridge is what triggers the injected incoming message.
	if _, err := instance.Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updated:
		if uri != "hub://threads/personal/dm%3Aalice" {
			t.Fatalf("update for %q", uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a subscribed client was not told about an incoming message")
	}
	// The message is also readable through the resource and the tools, so a
	// client that ignores notifications loses nothing.
	resource, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "hub://threads/personal/dm%3Aalice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "hello from the mock") {
		t.Fatalf("thread resource = %#v", resource.Contents)
	}
	history := callTool(t, session, "hub_messages", map[string]any{"account": "personal", "thread": "dm:alice"})
	if !strings.Contains(history, "hello from the mock") {
		t.Fatalf("messages = %s", history)
	}
}

// TestAccountScopedToolsAcrossSeveralAccounts drives the tool surface with two
// accounts served by one bridge.
func TestAccountScopedToolsAcrossSeveralAccounts(t *testing.T) {
	instance, _ := testHub(t)
	session := connect(t, instance)
	installMock(t, instance)

	accounts := callTool(t, session, "hub_accounts", map[string]any{})
	if !strings.Contains(accounts, `"id":"personal"`) || !strings.Contains(accounts, `"id":"work"`) {
		t.Fatalf("accounts = %s", accounts)
	}
	callTool(t, session, "hub_send", map[string]any{"account": "work", "to": "bob", "text": "work note"})
	workThreads := callTool(t, session, "hub_threads", map[string]any{"account": "work"})
	if !strings.Contains(workThreads, `"id":"bob"`) {
		t.Fatalf("work threads = %s", workThreads)
	}
	personalMessages := callTool(t, session, "hub_messages", map[string]any{"account": "personal"})
	if strings.Contains(personalMessages, "work note") {
		t.Fatalf("work message leaked into personal: %s", personalMessages)
	}
	// An unknown account is an error, not an empty success.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hub_send", Arguments: map[string]any{"account": "missing", "to": "bob", "text": "hi"},
	})
	if err == nil && !result.IsError {
		t.Fatalf("sending from an unknown account succeeded: %#v", result.StructuredContent)
	}
}
