package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reloadEnvironment starts a real environment from a written config, so a reload
// exercises the same path a running server uses.
func reloadEnvironment(t *testing.T) (*Environment, string) {
	t.Helper()
	configDir := t.TempDir()
	config := Config{
		Token: "test-token", Model: "openai/gpt-test",
		Agents: []AgentConfig{
			{Address: "main", Role: "orchestrator"},
			{Address: "worker", Role: "worker"},
		},
	}
	if err := Save(configDir, config); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	environment, err := New(ctx, Options{
		ConfigDir: configDir, StateDir: filepath.Join(configDir, "state"), Root: t.TempDir(),
		Getenv: func(name string) string {
			if name == "OPENAI_API_KEY" {
				return "sk-test"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(environment.Registry.StopAll)
	return environment, configDir
}

func writeAgentConfig(t *testing.T, configDir string, agents ...AgentConfig) {
	t.Helper()
	config, err := Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	config.Agents = agents
	if err := Save(configDir, config); err != nil {
		t.Fatal(err)
	}
}

// TestReloadAppliesAddedUpdatedAndRemovedAgents is the whole contract: the file is
// the intent, the running agents are what it is applied to.
func TestReloadAppliesAddedUpdatedAndRemovedAgents(t *testing.T) {
	environment, configDir := reloadEnvironment(t)
	ctx := context.Background()
	worker, ok := environment.Registry.Get("worker")
	if !ok {
		t.Fatal("the environment did not start its configured agents")
	}
	before := worker.CurrentBrain()

	// A changed model and prompt: the agent keeps running and swaps its brain.
	writeAgentConfig(t, configDir,
		AgentConfig{Address: "main", Role: "orchestrator"},
		AgentConfig{Address: "worker", Role: "maintainer", Model: "openai/gpt-other", Prompt: "You maintain things."},
	)
	report, err := environment.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Updated) != 1 || report.Updated[0] != "worker" || len(report.Added)+len(report.Removed) != 0 {
		t.Fatalf("report = %#v", report)
	}
	if worker.CurrentBrain() == before {
		t.Fatal("the brain was not replaced")
	}
	if status := worker.Status(); status.Role != "maintainer" {
		t.Fatalf("role after reload = %q", status.Role)
	}

	// A new agent starts and is reachable.
	writeAgentConfig(t, configDir,
		AgentConfig{Address: "main", Role: "orchestrator"},
		AgentConfig{Address: "worker", Role: "maintainer", Model: "openai/gpt-other", Prompt: "You maintain things."},
		AgentConfig{Address: "helper", Role: "worker"},
	)
	report, err = environment.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Added) != 1 || report.Added[0] != "helper" {
		t.Fatalf("report = %#v", report)
	}
	if _, ok := environment.Registry.Get("helper"); !ok {
		t.Fatal("the added agent is not running")
	}

	// Removing an agent stops it.
	writeAgentConfig(t, configDir, AgentConfig{Address: "main", Role: "orchestrator"})
	report, err = environment.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if _, ok := environment.Registry.Get("worker"); ok {
		t.Fatal("a removed agent is still registered")
	}
	stopped := make(chan struct{})
	go func() { worker.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("a removed agent kept running")
	}

	// A reload with nothing to do reports that and replaces nothing.
	report, err = environment.Reload(ctx)
	if err != nil || !report.Empty() {
		t.Fatalf("second reload = %#v, %v", report, err)
	}
	if got := report.Summary(); got != "no changes" {
		t.Fatalf("summary = %q", got)
	}
}

// TestReloadKeepsRunningAgentsWhenTheFileIsUnusable: a bad edit must not take a
// live environment down.
func TestReloadKeepsRunningAgentsWhenTheFileIsUnusable(t *testing.T) {
	environment, configDir := reloadEnvironment(t)
	main, _ := environment.Registry.Get("main")
	before := main.CurrentBrain()

	if err := os.WriteFile(filepath.Join(configDir, ConfigFile), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.Reload(context.Background()); err == nil {
		t.Fatal("an unreadable configuration was accepted")
	}
	if _, ok := environment.Registry.Get("main"); !ok {
		t.Fatal("a live agent disappeared because of an unreadable file")
	}
	if main.CurrentBrain() != before {
		t.Fatal("an unreadable file replaced a running brain")
	}

	// The same holds for a file that parses but cannot be applied as a whole: a
	// duplicate address is refused before anything is changed.
	duplicate := Config{
		Token: "test-token", Model: "openai/gpt-test",
		Agents: []AgentConfig{{Address: "main"}, {Address: "Main"}},
	}
	if err := Save(configDir, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.Reload(context.Background()); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate report = %v", err)
	}
	if _, ok := environment.Registry.Get("Main"); ok {
		t.Fatal("an entry from an unusable file was applied")
	}
}

// TestReloadReportsAnAgentItCannotBuild: the agent whose configuration cannot be
// built keeps its old brain, and the others still apply.
func TestReloadReportsAnAgentItCannotBuild(t *testing.T) {
	environment, configDir := reloadEnvironment(t)
	ctx := context.Background()
	worker, _ := environment.Registry.Get("worker")
	before := worker.CurrentBrain()

	writeAgentConfig(t, configDir,
		AgentConfig{Address: "main", Role: "orchestrator"},
		AgentConfig{Address: "worker", Model: "not-a-provider/model"},
	)
	report, err := environment.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failed) != 1 || report.Failed[0].Address != "worker" {
		t.Fatalf("report = %#v", report)
	}
	if worker.CurrentBrain() != before {
		t.Fatal("an agent that could not be built was still swapped")
	}
	if !strings.Contains(report.Summary(), "failed worker") {
		t.Fatalf("summary = %q", report.Summary())
	}
}

// TestReloadEndpointAppliesTheFile covers the operator path over HTTP.
func TestReloadEndpointAppliesTheFile(t *testing.T) {
	environment, configDir := reloadEnvironment(t)
	writeAgentConfig(t, configDir,
		AgentConfig{Address: "main", Role: "orchestrator"},
		AgentConfig{Address: "worker"},
		AgentConfig{Address: "helper"},
	)
	response := call(t, environment, http.MethodPost, "/v1/reload", "")
	if response.Code != http.StatusOK {
		t.Fatalf("reload = %d: %s", response.Code, response.Body)
	}
	var report ReloadReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Added) != 1 || report.Added[0] != "helper" {
		t.Fatalf("report = %#v", report)
	}
	if _, ok := environment.Registry.Get("helper"); !ok {
		t.Fatal("the endpoint did not start the added agent")
	}
	// A file that cannot be applied is refused with nothing changed.
	if err := os.WriteFile(filepath.Join(configDir, ConfigFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if broken := call(t, environment, http.MethodPost, "/v1/reload", ""); broken.Code != http.StatusBadRequest {
		t.Fatalf("reload of a broken file = %d: %s", broken.Code, broken.Body)
	}
}

// TestAgentSettingsApplyWithoutARestart: editing an agent's settings is no longer
// persist-only; the running agent picks them up on its next message.
func TestAgentSettingsApplyWithoutARestart(t *testing.T) {
	environment, configDir := reloadEnvironment(t)
	worker, _ := environment.Registry.Get("worker")
	before := worker.CurrentBrain()

	response := call(t, environment, http.MethodPut, "/v1/agents/worker/settings", `{"role":"maintainer","prompt":"You maintain things."}`)
	if response.Code != http.StatusOK {
		t.Fatalf("settings = %d: %s", response.Code, response.Body)
	}
	if worker.CurrentBrain() == before {
		t.Fatal("the settings endpoint did not apply the change")
	}
	if status := worker.Status(); status.Role != "maintainer" {
		t.Fatalf("role = %q", status.Role)
	}

	// A model that cannot be built is refused, and the file keeps the old one.
	persisted, err := Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	refused := call(t, environment, http.MethodPut, "/v1/agents/worker/settings", `{"model":"not-a-provider/model"}`)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("unbuildable model = %d: %s", refused.Code, refused.Body)
	}
	after, err := Load(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Agents[1].Model != persisted.Agents[1].Model {
		t.Fatalf("a refused model was written: %#v", after.Agents[1])
	}
}
