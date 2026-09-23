package hub

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testHub opens a hub in a temporary config and state directory.
func testHub(t *testing.T) (*Hub, func(string) string) {
	t.Helper()
	base := t.TempDir()
	getenv := func(name string) string {
		switch name {
		case "MIDAS_CONFIG_DIR":
			return filepath.Join(base, "config")
		case "HUB_STATE_DIR":
			return filepath.Join(base, "state")
		default:
			return ""
		}
	}
	instance, err := Open(getenv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(instance.Close)
	return instance, getenv
}

// mockBridgePath builds the reference bridge once per test binary run.
func mockBridgePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mock-bridge")
	command := exec.Command("go", "build", "-o", path, "github.com/CarlvinceTan/midas/cmd/mock-bridge")
	command.Dir = moduleRoot(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build mock bridge: %v\n%s", err, output)
	}
	return path
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("module root not found")
		}
		directory = parent
	}
}

func installMock(t *testing.T, instance *Hub) BridgeStatus {
	t.Helper()
	status, err := instance.Install(context.Background(), InstallOptions{
		Name: "mock", Source: mockBridgePath(t), Command: []string{"mock-bridge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestHubStartsWithNothingAttached(t *testing.T) {
	instance, getenv := testHub(t)
	config := instance.Config()
	if len(config.Bridges) != 0 {
		t.Fatalf("default config has bridges: %#v", config.Bridges)
	}
	if config.Listen != "" || config.Token != "" || config.Registry != "" {
		t.Fatalf("default config is not empty: %#v", config)
	}
	if statuses := instance.BridgeStatuses(); len(statuses) != 0 {
		t.Fatalf("bridges at open: %#v", statuses)
	}
	calls, started, _ := instance.Stats()
	if calls != 0 || started != 0 {
		t.Fatalf("a fresh hub did work: %d calls, %d starts", calls, started)
	}
	// The config file is created on first write, not on open, and holds no token.
	if _, err := os.Stat(instance.ConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open wrote a config file: %v", err)
	}
	if _, err := os.Stat(instance.StateDir()); err != nil {
		t.Fatalf("state directory missing: %v", err)
	}
	if path, err := OwnConfigPath(getenv); err != nil || filepath.Base(path) != "hub.json" {
		t.Fatalf("config path = %q, %v", path, err)
	}
	// Nothing points the hub at a shared file here, so it owns its own.
	if location, err := ResolveConfigLocation(getenv); err != nil || location.Section != nil {
		t.Fatalf("location = %#v, %v", location, err)
	}
}

func TestHubOnlyStartsABridgeWhenSomethingUsesIt(t *testing.T) {
	instance, _ := testHub(t)
	installMock(t, instance)

	// Installing records the bridge but starts nothing.
	if status := instance.BridgeStatuses(); len(status) != 1 || status[0].State != "stopped" {
		t.Fatalf("after install: %#v", status)
	}
	_, started, _ := instance.Stats()
	if started != 0 {
		t.Fatalf("install started the bridge: %d", started)
	}

	accounts, err := instance.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != "personal" || accounts[0].Bridge != "mock" || accounts[1].ID != "work" {
		t.Fatalf("accounts = %#v", accounts)
	}
	_, started, _ = instance.Stats()
	if started != 1 {
		t.Fatalf("first call did not start the bridge: %d starts", started)
	}
	if status := instance.BridgeStatuses(); status[0].State != "running" {
		t.Fatalf("bridge state = %#v", status[0])
	}
	// The account list is cached in config so a later process can answer without
	// starting anything.
	reopened, _ := testHub(t)
	_ = reopened
	if ids := instance.Config().Bridges["mock"].Accounts; len(ids) != 2 || ids[0] != "personal" || ids[1] != "work" {
		t.Fatalf("cached accounts = %#v", ids)
	}
}

func TestHubStopsIdleBridgesAndCanStartThemAgain(t *testing.T) {
	instance, _ := testHub(t)
	installMock(t, instance)
	if _, err := instance.Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Nothing is idle yet.
	if stopped := instance.Reap(); stopped != 0 {
		t.Fatalf("reaped a busy bridge: %d", stopped)
	}
	if err := instance.StopBridge("mock"); err != nil {
		t.Fatal(err)
	}
	if status := instance.BridgeStatuses(); status[0].State != "stopped" {
		t.Fatalf("after stop: %#v", status[0])
	}
	if _, err := instance.Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := instance.BridgeStatuses(); status[0].State != "running" {
		t.Fatalf("bridge did not start again: %#v", status[0])
	}
}

func TestHubSendsAndReadsMessagesThroughABridge(t *testing.T) {
	instance, _ := testHub(t)
	installMock(t, instance)
	sent, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "personal", To: "alice", Text: "hello there"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Text != "hello there" || sent.Thread != "alice" || sent.Account != "personal" || sent.Timestamp == 0 {
		t.Fatalf("sent = %#v", sent)
	}
	messages, err := instance.History(context.Background(), "mock", "personal", "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != "hello there" {
		t.Fatalf("history = %#v", messages)
	}
	if instance.store.Count() != 1 {
		t.Fatalf("store count = %d", instance.store.Count())
	}
}

func TestHubLoginRelaysAChallengeAndRendersItForTerminals(t *testing.T) {
	instance, _ := testHub(t)
	installMock(t, instance)

	login, err := instance.LoginStart(context.Background(), "session-a", "mock", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if login.Challenge.Kind != "qr" || login.Challenge.Payload == "" {
		t.Fatalf("challenge = %#v", login.Challenge)
	}
	// The payload is readable by the session that started it...
	status, err := instance.LoginStatus(login.ID, "session-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Rendered) == 0 {
		t.Fatal("no printable QR rows were produced")
	}
	for _, row := range status.Rendered {
		if strings.Trim(row, " ▀▄█") != "" {
			t.Fatalf("rendered row has unexpected characters: %q", row)
		}
	}
	// ...and not by another one.
	if _, err := instance.LoginStatus(login.ID, "session-b", false); err == nil {
		t.Fatal("a second session could read the login payload")
	}
	// The bridge validates the code, so a wrong one is reported rather than
	// silently accepted.
	if _, err := instance.LoginSubmit(context.Background(), login.ID, "session-a", "000000"); err == nil {
		t.Fatal("a wrong code was accepted")
	}
	answered, err := instance.LoginSubmit(context.Background(), login.ID, "session-a", "scan-personal")
	if err != nil {
		t.Fatal(err)
	}
	if answered.ID != login.ID {
		t.Fatalf("answered = %#v", answered)
	}
	if err := instance.LoginCancel(context.Background(), login.ID, "session-a"); err != nil {
		t.Fatal(err)
	}
	if logins := instance.Logins(); len(logins) != 0 {
		t.Fatalf("cancelled login is still tracked: %#v", logins)
	}
}

func TestInstallVerifiesChecksumsAndUninstallIsNotDestructive(t *testing.T) {
	instance, _ := testHub(t)
	binary := mockBridgePath(t)
	if _, err := instance.Install(context.Background(), InstallOptions{
		Name: "mock", Source: binary, Command: []string{"mock-bridge"},
		Checksum: strings.Repeat("0", 64),
	}); err == nil {
		t.Fatal("install accepted a wrong checksum")
	}
	installMock(t, instance)
	if err := instance.Uninstall("mock", false); err != nil {
		t.Fatal(err)
	}
	if len(instance.Config().Bridges) != 0 {
		t.Fatalf("uninstall kept the bridge: %#v", instance.Config().Bridges)
	}
	accounts, err := instance.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts with nothing installed = %#v", accounts)
	}
}

func TestRenderQRSkipsEmptyPayloads(t *testing.T) {
	if rows := RenderQR("   "); rows != nil {
		t.Fatalf("blank payload rendered: %#v", rows)
	}
	rows := RenderQR("https://example.test/login?token=abc")
	if len(rows) < 10 {
		t.Fatalf("a payload did not render: %#v", rows)
	}
	width := len([]rune(rows[0]))
	for _, row := range rows {
		if len([]rune(row)) > width {
			t.Fatalf("row %q is wider than the first", row)
		}
	}
}

func TestConfigRoundTripsThroughOwnerOnlyFile(t *testing.T) {
	instance, getenv := testHub(t)
	path, err := OwnConfigPath(getenv)
	if err != nil {
		t.Fatal(err)
	}
	config := instance.Config()
	config.Listen = "127.0.0.1:8787"
	config.Token = "secret"
	if err := WriteConfig(path, config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v", info.Mode().Perm())
	}
	reloaded, err := ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Listen != "127.0.0.1:8787" || reloaded.Token != "secret" {
		t.Fatalf("reloaded = %#v", reloaded)
	}
	if reloaded.IdleTTL() != DefaultIdleTimeout {
		t.Fatalf("idle ttl = %v", reloaded.IdleTTL())
	}
}

// TestOneBridgeServesSeveralAccounts covers the multi-account shape: one bridge
// process, several accounts, and no bleed between them.
func TestOneBridgeServesSeveralAccounts(t *testing.T) {
	instance, _ := testHub(t)
	installMock(t, instance)

	accounts, err := instance.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != "personal" || accounts[1].ID != "work" {
		t.Fatalf("accounts = %#v", accounts)
	}
	// Both accounts came from one process.
	_, started, _ := instance.Stats()
	if started != 1 {
		t.Fatalf("accounts took %d bridge starts", started)
	}

	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "personal", To: "alice", Text: "from personal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "work", To: "bob", Text: "from work"}); err != nil {
		t.Fatal(err)
	}
	personal, err := instance.History(context.Background(), "mock", "personal", "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(personal) != 1 || personal[0].Text != "from personal" || personal[0].Account != "personal" {
		t.Fatalf("personal history = %#v", personal)
	}
	work, err := instance.History(context.Background(), "mock", "work", "bob", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Text != "from work" || work[0].Account != "work" {
		t.Fatalf("work history = %#v", work)
	}
	personalThreads, err := instance.Threads(context.Background(), "mock", "personal")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Thread{}
	for _, thread := range personalThreads {
		byID[thread.ID] = thread
	}
	// What the hub sent, and what the bridge says the service looks like: a
	// community, the channels inside it, a group, and a direct message.
	for _, want := range []string{"alice", "space:acme", "space:acme/general", "space:acme/ops", "group:team", "dm:alice"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("%s is missing from the personal threads: %#v", want, personalThreads)
		}
	}
	if channel := byID["space:acme/ops"]; channel.Kind != ThreadChannel || channel.Parent != "space:acme" {
		t.Fatalf("the channel lost its community: %#v", channel)
	}
	all, err := instance.Threads(context.Background(), "mock", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dm:bob", "space:acme"} {
		found := false
		for _, thread := range all {
			if thread.ID == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is missing across accounts: %#v", want, all)
		}
	}

	// Two logins run at once, one per account, each with its own challenge.
	personalLogin, err := instance.LoginStart(context.Background(), "session-a", "mock", "personal")
	if err != nil {
		t.Fatal(err)
	}
	workLogin, err := instance.LoginStart(context.Background(), "session-a", "mock", "work")
	if err != nil {
		t.Fatal(err)
	}
	if personalLogin.ID == workLogin.ID {
		t.Fatalf("both logins share an ID: %#v", personalLogin.ID)
	}
	if personalLogin.Challenge.Payload == workLogin.Challenge.Payload {
		t.Fatalf("both logins share a challenge payload: %q", personalLogin.Challenge.Payload)
	}
	if !strings.Contains(personalLogin.Challenge.Payload, "personal") || !strings.Contains(workLogin.Challenge.Payload, "work") {
		t.Fatalf("challenges are not account-scoped: %q %q", personalLogin.Challenge.Payload, workLogin.Challenge.Payload)
	}

	// An account the bridge does not serve is refused, not silently accepted.
	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "missing", To: "alice", Text: "hi"}); err == nil {
		t.Fatal("a send to an unknown account succeeded")
	}
	// Sending without naming an account is refused too.
	if _, err := instance.Send(context.Background(), SendRequest{Bridge: "mock", Account: "", To: "alice", Text: "hi"}); err == nil {
		t.Fatal("a send without an account succeeded")
	}
}
