package manager

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testManager builds a manager whose process control is recorded, so nothing is
// actually spawned.
func testManager(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	started := &[]string{}
	manager, err := NewManager(Options{ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	manager.Run = func(directory string, _ []string, binary string, arguments []string) (int, error) {
		*started = append(*started, binary+" "+strings.Join(arguments, " "))
		return 4242, nil
	}
	manager.Signal = func(pid int, signal syscall.Signal) error {
		*started = append(*started, "signal "+signal.String())
		return nil
	}
	return manager, started
}

// TestDeploymentIsOneUserToOneServer covers the manager's core promise: each user
// gets their own directory, port, token and browser profile, and only one.
func TestDeploymentIsOneUserToOneServer(t *testing.T) {
	manager, _ := testManager(t)
	first, err := manager.Create("alice", "anthropic/claude-sonnet-4.5", "/usr/local/bin/midas-server")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create("bob", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Port == second.Port || first.Dir == second.Dir || first.Token == second.Token {
		t.Fatalf("deployments share state: %#v %#v", first, second)
	}
	if first.Port != DefaultBasePort || second.Port != DefaultBasePort+1 {
		t.Fatalf("ports = %d, %d", first.Port, second.Port)
	}
	// One user, one server: creating a second deployment for the same user fails.
	if _, err := manager.Create("alice", "", ""); err == nil {
		t.Fatal("a second deployment was created for the same user")
	}
	// A user name that would escape its directory is refused.
	for _, bad := range []string{"", "  ", "a/b", "a b", `..\x`} {
		if _, err := manager.Create(bad, "", ""); err == nil {
			t.Fatalf("user name %q was accepted", bad)
		}
	}

	// The deployment's own configuration holds its token, model, browser profile
	// and roster: the server reads this on boot.
	data, err := os.ReadFile(filepath.Join(first.Dir, "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Token   string `json:"token"`
		Model   string `json:"model"`
		Browser struct {
			ProfileDir string `json:"profileDir"`
		} `json:"browser"`
		Agents []struct {
			Address string `json:"address"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Token != first.Token || config.Model != first.Model {
		t.Fatalf("config = %#v", config)
	}
	if !strings.HasPrefix(config.Browser.ProfileDir, first.Dir) {
		t.Fatalf("browser profile is outside the deployment: %q", config.Browser.ProfileDir)
	}
	if len(config.Agents) != 2 || config.Agents[0].Address != "orchestrator" {
		t.Fatalf("agents = %#v", config.Agents)
	}
	// Directories it owns exist, and permissions are owner-only.
	for _, sub := range []string{"", "state", "browser"} {
		info, err := os.Stat(filepath.Join(first.Dir, sub))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing %q: %v", sub, err)
		}
	}
	info, err := os.Stat(filepath.Join(first.Dir, "server.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("server.json mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestStartStopAndList(t *testing.T) {
	manager, started := testManager(t)
	deployment, err := manager.Create("alice", "", "/usr/local/bin/midas-server")
	if err != nil {
		t.Fatal(err)
	}
	// Start runs the deployment's own binary with its own config directory and
	// port, and records the process.
	startedDeployment, err := manager.Start("alice")
	if err != nil {
		t.Fatal(err)
	}
	if startedDeployment.PID == 0 || !startedDeployment.Restart {
		t.Fatalf("started = %#v", startedDeployment)
	}
	want := "/usr/local/bin/midas-server --config-dir " + deployment.Dir + " --listen 127.0.0.1:" + itoa(deployment.Port)
	if len(*started) != 1 || (*started)[0] != want {
		t.Fatalf("started = %#v, want %q", *started, want)
	}
	// Starting an already-running deployment is a no-op rather than a second
	// process.
	manager.Running = func(Deployment) bool { return true }
	if _, err := manager.Start("alice"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 {
		t.Fatalf("a running deployment was started twice: %#v", *started)
	}
	// Stopping clears the restart flag, so a graceful stop sticks.
	stopped, err := manager.Stop("alice")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.PID != 0 || stopped.Restart {
		t.Fatalf("stopped = %#v", stopped)
	}
	if last := (*started)[len(*started)-1]; !strings.HasPrefix(last, "signal") {
		t.Fatalf("stop did not signal the process: %#v", *started)
	}
	// Listing reports every user with their URL.
	listings := manager.List()
	if len(listings) != 1 || listings[0].User != "alice" || listings[0].URL != "http://127.0.0.1:"+itoa(deployment.Port) {
		t.Fatalf("listings = %#v", listings)
	}
	// An unknown user is an error, not a silent no-op.
	if _, err := manager.Start("nobody"); err == nil {
		t.Fatal("starting an unknown deployment succeeded")
	}
	if _, err := manager.Stop("nobody"); err == nil {
		t.Fatal("stopping an unknown deployment succeeded")
	}
}

func TestRemoveKeepsTheDirectoryUnlessPurged(t *testing.T) {
	manager, _ := testManager(t)
	deployment, err := manager.Create("carol", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("carol", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deployment.Dir); err != nil {
		t.Fatalf("removing a user destroyed their state: %v", err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("the deployment is still listed")
	}
	// Purging is the explicit way to delete the state.
	again, err := manager.Create("carol", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("carol", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(again.Dir); !os.IsNotExist(err) {
		t.Fatalf("purge left the directory: %v", err)
	}
}

// TestDeploymentsSurviveAManagerRestart covers the file the CLI reads: a manager
// reload must see the same users, ports and tokens.
func TestDeploymentsSurviveAManagerRestart(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	manager, err := NewManager(Options{ConfigDir: configDir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create("dave", "openai/gpt-5.6", "/bin/midas-server")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(Options{ConfigDir: configDir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	deployment, ok := reloaded.Config().Deployments["dave"]
	if !ok || deployment.Port != created.Port || deployment.Token != created.Token || deployment.Model != created.Model {
		t.Fatalf("reloaded = %#v", deployment)
	}
	info, err := os.Stat(Path(configDir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manager.json mode = %v, %v", info.Mode().Perm(), err)
	}
}

// TestStartActuallySpawnsAndStops uses a real process, because the manager's job
// is process control and a fake would not prove it.
func TestStartActuallySpawnsAndStops(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary on this platform")
	}
	manager, err := NewManager(Options{ConfigDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	deployment, err := manager.Create("erin", "", sleep)
	if err != nil {
		t.Fatal(err)
	}
	// The manager's own runner is used, with arguments the deployment would pass.
	pid, err := manager.Run(deployment.Dir, os.Environ(), sleep, []string{"30"})
	if err != nil {
		t.Fatal(err)
	}
	deployment.PID = pid
	if !manager.Running(deployment) {
		t.Fatalf("the spawned process is not running (pid %d)", pid)
	}
	if err := manager.Signal(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// The process exits on its own after the signal; poll rather than guess how
	// long the kernel takes.
	deadline := time.Now().Add(5 * time.Second)
	for manager.Running(deployment) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if manager.Running(deployment) {
		t.Fatal("the process survived SIGTERM")
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
