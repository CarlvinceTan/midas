package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/CarlvinceTan/midas/pkg/ai"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text"`
}

type echoOutput struct {
	Echo string `json:"echo"`
}

func TestManagerConnectsListsAndExecutesNamespacedTools(t *testing.T) {
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "echo", Description: "echo text"}, func(_ context.Context, _ *sdkmcp.CallToolRequest, input echoInput) (*sdkmcp.CallToolResult, echoOutput, error) {
		return nil, echoOutput{Echo: input.Text}, nil
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	manager, err := newManager(map[string]Config{"local test": {Command: []string{"unused"}}}, func(Config) (sdkmcp.Transport, error) {
		return clientTransport, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Connect(context.Background(), "local test"); err != nil {
		t.Fatal(err)
	}
	statuses := manager.Statuses()
	if len(statuses) != 1 || statuses[0].State != StateConnected || statuses[0].Tools != 1 {
		t.Fatalf("statuses = %#v", statuses)
	}
	available := manager.Tools()
	if len(available) != 1 || available[0].Definition().Name != "mcp__local_test__echo" {
		t.Fatalf("tools = %#v", available)
	}
	result, err := available[0].Execute(context.Background(), ai.NewToolCall("1", available[0].Definition().Name, map[string]any{"text": "hello"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Content[0].(ai.TextContent).Text; got != `{"echo":"hello"}` {
		t.Fatalf("content = %q", got)
	}
	if err := manager.Disconnect("local test"); err != nil {
		t.Fatal(err)
	}
	if manager.Statuses()[0].State != StateDisconnected || len(manager.Tools()) != 0 {
		t.Fatalf("after disconnect: %#v tools=%d", manager.Statuses(), len(manager.Tools()))
	}
}

func TestManagerConfigAndFailureStates(t *testing.T) {
	invalid := []Config{{}, {Command: []string{"x"}, URL: "https://example.com"}, {Command: []string{""}}}
	for _, config := range invalid {
		if _, err := New(map[string]Config{"bad": config}); err == nil {
			t.Fatalf("config %#v succeeded", config)
		}
	}
	manager, err := newManager(map[string]Config{
		"disabled": {Disabled: true},
		"broken":   {Command: []string{"broken"}},
	}, func(Config) (sdkmcp.Transport, error) { return nil, errors.New("boom") })
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(context.Background(), "disabled"); err == nil {
		t.Fatal("disabled server connected")
	}
	if err := manager.Connect(context.Background(), "broken"); err == nil {
		t.Fatal("broken server connected")
	}
	statuses := manager.Statuses()
	if statuses[0].Name != "broken" || statuses[0].State != StateFailed || statuses[0].Error != "boom" {
		t.Fatalf("broken status = %#v", statuses[0])
	}
	if statuses[1].State != StateDisabled {
		t.Fatalf("disabled status = %#v", statuses[1])
	}
}

func TestConnectAllKeepsSuccessfulServersAndJoinsFailures(t *testing.T) {
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	manager, err := newManager(map[string]Config{
		"good": {Command: []string{"good"}},
		"bad":  {Command: []string{"bad"}},
	}, func(config Config) (sdkmcp.Transport, error) {
		if config.Command[0] == "bad" {
			return nil, errors.New("unavailable")
		}
		return clientTransport, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.ConnectAll(context.Background()); err == nil {
		t.Fatal("ConnectAll did not return partial failure")
	}
	statuses := manager.Statuses()
	if statuses[0].State != StateFailed || statuses[1].State != StateConnected {
		t.Fatalf("statuses = %#v", statuses)
	}
}
