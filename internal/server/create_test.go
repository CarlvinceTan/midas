package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// createEnvironment builds an environment whose agents can create agents, which is
// what the create_agent tool needs.
func createEnvironment(t *testing.T) (*Environment, string) {
	t.Helper()
	configDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	broker := testBroker(t)
	pool, err := NewPool(configDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	environment := &Environment{
		Config: Config{Token: "test-token", Agents: []AgentConfig{{Address: "main", Role: "orchestrator"}}},
		Broker: broker, Registry: NewRegistry(), Leases: NewLeases(time.Minute),
		Pool: pool, ConfigDir: configDir, Feed: NewFeed(), state: newTeamsState(nil, nil),
		options: Options{ConfigDir: configDir, StateDir: t.TempDir(), Root: t.TempDir(), Getenv: func(string) string { return "" }},
		getenv:  func(string) string { return "" },
		ctx:     ctx,
	}
	agent, err := NewAgent(broker, "main", "orchestrator", scriptedBrain{handle: func(_ context.Context, request Request) (string, error) {
		return "ok: " + request.Text, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	environment.Registry.Add(agent)
	go agent.Run(ctx)
	return environment, configDir
}

// TestCreateAgentAddsAndPersistsAnAgent: an agent grows the team, and the new
// agent survives a restart because the configuration is written.
func TestCreateAgentAddsAndPersistsAnAgent(t *testing.T) {
	environment, configDir := createEnvironment(t)
	status, err := environment.CreateAgent(context.Background(), AgentConfig{
		Address: "Researcher", Role: "worker", Prompt: "You research things.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Address != "researcher" || status.Role != "worker" {
		t.Fatalf("created %#v", status)
	}
	if _, ok := environment.Registry.Get("researcher"); !ok {
		t.Fatal("the created agent is not in the registry")
	}
	// The address is normalised, so "RESEARCHER" is the same agent as "researcher".
	if _, err := environment.CreateAgent(context.Background(), AgentConfig{Address: "RESEARCHER"}); err == nil {
		t.Fatal("a duplicate address was accepted")
	}
	// It is written to the environment config, which is what makes it survive.
	data, err := os.ReadFile(filepath.Join(configDir, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range config.Agents {
		if entry.Address == "researcher" && entry.Role == "worker" && strings.Contains(entry.Prompt, "research") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the created agent was not persisted: %s", data)
	}
}

// TestCreateAgentValidatesNamesAndBoundsTheEnvironment covers the guards that keep
// a confused run from filling the machine with agents.
func TestCreateAgentValidatesNamesAndBoundsTheEnvironment(t *testing.T) {
	environment, _ := createEnvironment(t)
	for name, address := range map[string]string{
		"empty":        "",
		"spaces":       "two words",
		"path":         "a/b",
		"upperbust":    strings.Repeat("a", 80),
		"punctuation!": "agent!",
	} {
		if _, err := environment.CreateAgent(context.Background(), AgentConfig{Address: address}); err == nil {
			t.Fatalf("%s was accepted as an address", name)
		}
	}
	// The environment has a limit; agents are created up to it and no further.
	for index := len(environment.Registry.Statuses()); index < maxAgents; index++ {
		address := "agent-" + strings.Repeat("x", 1) + string(rune('a'+index%26)) + "-" + strings.Repeat("0", index%7)
		if _, err := environment.CreateAgent(context.Background(), AgentConfig{Address: address}); err != nil {
			t.Fatalf("creating %s: %v", address, err)
		}
	}
	if _, err := environment.CreateAgent(context.Background(), AgentConfig{Address: "one-too-many"}); err == nil {
		t.Fatal("the agent limit was not enforced")
	}
}

// TestServerAgentsDoNotDelegateButCanCreateAgents is the rule for a server
// environment: there is no task tool that runs a subagent inside the caller's run,
// and an agent adds an agent instead.
func TestServerAgentsDoNotDelegateButCanCreateAgents(t *testing.T) {
	environment, _ := createEnvironment(t)
	brain := NewBrain(nil, ai.Model{ID: "test", Provider: "test"}, "", "", environment.Pool)
	brain.SetCreator(environment)
	request := Request{Agent: "main", From: UserAddress, Text: "do the work"}
	names := map[string]bool{}
	for _, tool := range brain.busTools(request) {
		names[tool.Definition().Name] = true
	}
	for _, want := range []string{"ask_agent", "tell_agent", "create_agent"} {
		if !names[want] {
			t.Fatalf("%s is missing from the agent tools: %#v", want, names)
		}
	}
	if names["task"] {
		t.Fatal("a server agent was given a delegation tool")
	}
	// The tool actually creates the agent through the environment.
	for _, tool := range brain.busTools(request) {
		if tool.Definition().Name != "create_agent" {
			continue
		}
		result, err := tool.Execute(context.Background(), ai.NewToolCall("1", "create_agent", map[string]any{
			"address": "helper", "role": "worker",
		}), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 || !strings.Contains(result.Content[0].(ai.TextContent).Text, "helper") {
			t.Fatalf("create_agent result = %#v", result.Content)
		}
		if _, ok := environment.Registry.Get("helper"); !ok {
			t.Fatal("create_agent did not create the agent")
		}
		return
	}
	t.Fatal("create_agent was not found among the tools")
}
