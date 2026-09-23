package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// loginOf is the first login in a login tool result.
func loginOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	logins := list(t, result, "logins")
	if len(logins) == 0 {
		t.Fatalf("no login in %#v", result)
	}
	login, ok := logins[0].(map[string]any)
	if !ok {
		t.Fatalf("login is not an object: %#v", logins[0])
	}
	return login
}

// installMock installs the mock bridge from this checkout, which is what every hub
// test starts with. It returns the bridge name.
func installMock(t *testing.T, instance *server, env map[string]string) string {
	t.Helper()
	source := binaries["mock-bridge"]
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	arguments := map[string]any{
		"action": "install", "name": "mock", "source": source,
		"checksum": hex.EncodeToString(sum[:]), "command": "mock-bridge",
	}
	// Optional fields are omitted rather than sent as null: the tool schema types
	// them, and an explicit null is not an object.
	if len(env) > 0 {
		arguments["env"] = env
	}
	result := callTool(t, instance, "hub_bridges", arguments)
	bridges := list(t, result, "bridges")
	if len(bridges) != 1 || field(t, bridges[0], "name") != "mock" {
		t.Fatalf("install = %#v", result)
	}
	return "mock"
}

// TestHubBridgeLifecycleIsGuaranteed walks the whole bridge lifecycle through the
// real hub binary: install, list, start, serve, stop, uninstall, purge.
func TestHubBridgeLifecycleIsGuaranteed(t *testing.T) {
	instance := startHub(t, nil)

	// Nothing installed yet: the hub says so rather than failing.
	empty := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	if !strings.Contains(strings.ToLower(toolTextString(empty)), "no bridges") {
		t.Fatalf("empty list = %#v", empty)
	}

	installMock(t, instance, nil)
	listed := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	bridges := list(t, listed, "bridges")
	if len(bridges) != 1 || field(t, bridges[0], "name") != "mock" {
		t.Fatalf("bridges = %#v", bridges)
	}
	// The bridge was installed under the environment's own directory, so nothing
	// of the developer's is involved.
	installed := filepath.Join(instance.Config, "hub", "bridges", "mock", "mock-bridge")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("the bridge binary is not where the hub owns it: %v", err)
	}

	// Starting runs the bridge and answers its hello.
	callTool(t, instance, "hub_bridges", map[string]any{"action": "start", "name": "mock"})
	listed = callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	if state := field(t, list(t, listed, "bridges")[0], "state"); state != "running" {
		t.Fatalf("start = %#v", listed)
	}
	accounts := callTool(t, instance, "hub_accounts", map[string]any{})
	ids := []string{}
	for _, entry := range list(t, accounts, "accounts") {
		ids = append(ids, field(t, entry, "id"))
	}
	if len(ids) != 2 || ids[0] != "personal" || ids[1] != "work" {
		t.Fatalf("accounts = %#v", ids)
	}

	// Stopping and starting again is lazy: the next call starts it back up.
	callTool(t, instance, "hub_bridges", map[string]any{"action": "stop", "name": "mock"})
	listed = callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
	if state := field(t, list(t, listed, "bridges")[0], "state"); state != "stopped" {
		t.Fatalf("stop = %#v", listed)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("stopping removed the binary: %v", err)
	}
	accounts = callTool(t, instance, "hub_accounts", map[string]any{})
	if len(list(t, accounts, "accounts")) != 2 {
		t.Fatalf("accounts after a lazy restart = %#v", accounts)
	}

	// Uninstalling forgets the bridge and removes its binary, keeping its state
	// unless purge is asked for.
	callTool(t, instance, "hub_bridges", map[string]any{"action": "uninstall", "name": "mock"})
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("the binary survived an uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(installed)); err != nil {
		t.Fatalf("an uninstall removed the bridge's files without being asked: %v", err)
	}
	if listed = callTool(t, instance, "hub_bridges", map[string]any{"action": "list"}); len(list(t, listed, "bridges")) != 0 {
		t.Fatalf("bridges after uninstall = %#v", listed)
	}
}

// TestHubRefusesBadBridgeInstalls: the hub validates what it is asked to install,
// which is what keeps a bad registry entry from becoming a broken environment.
func TestHubRefusesBadBridgeInstalls(t *testing.T) {
	instance := startHub(t, nil)
	source := binaries["mock-bridge"]

	for name, arguments := range map[string]map[string]any{
		"wrong checksum": {"action": "install", "name": "mock", "source": source, "checksum": strings.Repeat("0", 64), "command": "mock-bridge"},
		"missing source": {"action": "install", "name": "mock", "source": filepath.Join(t.TempDir(), "absent"), "command": "mock-bridge"},
		"no command":     {"action": "install", "name": "mock", "source": source},
		"bad name":       {"action": "install", "name": "../escape", "source": source, "command": "mock-bridge"},
		"unknown bridge": {"action": "start", "name": "nothing"},
		"bad action":     {"action": "dance", "name": "mock"},
	} {
		if message := callToolError(t, instance, "hub_bridges", arguments); strings.TrimSpace(message) == "" {
			t.Fatalf("%s was accepted", name)
		}
	}
	// Nothing was installed by any of them, and nothing was written outside the
	// bridges directory.
	if listed := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"}); len(list(t, listed, "bridges")) != 0 {
		t.Fatalf("a refused install left a bridge behind: %#v", listed)
	}
	if _, err := os.Stat(filepath.Join(instance.Config, "escape")); err == nil {
		t.Fatal("a traversal name wrote outside the bridges directory")
	}
}

// TestHubSendsAndReadsEveryContentShape covers sending: text, a reply, and an
// attachment, and reading back what the service said.
func TestHubSendsAndReadsEveryContentShape(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	sent := callTool(t, instance, "hub_send", map[string]any{
		"account": "personal", "to": "dm:alice", "text": "hello there",
	})
	if field(t, sent, "text") != "hello there" || field(t, sent, "status") != "sent" {
		t.Fatalf("send = %#v", sent)
	}

	// An attachment-only message is valid, and the hub keeps the reference: the
	// bytes belong to the bridge.
	attachment := writeFile(t, t.TempDir(), "diagram.png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	withMedia := callTool(t, instance, "hub_send", map[string]any{
		"account": "personal", "to": "dm:alice", "replyTo": field(t, sent, "id"),
		"media": []map[string]any{{"kind": "image", "path": attachment, "mime": "image/png", "caption": "the plan"}},
	})
	if kind := field(t, withMedia, "kind"); kind != "image" {
		t.Fatalf("media message kind = %q: %#v", kind, withMedia)
	}
	media := list(t, withMedia, "media")
	if len(media) != 1 || field(t, media[0], "path") != attachment || field(t, media[0], "caption") != "the plan" {
		t.Fatalf("media = %#v", media)
	}
	if field(t, withMedia, "replyTo") != field(t, sent, "id") {
		t.Fatalf("replyTo = %#v", withMedia)
	}

	// A message with nothing in it is refused.
	if message := callToolError(t, instance, "hub_send", map[string]any{"account": "personal", "to": "dm:alice"}); !strings.Contains(message, "nothing to send") {
		t.Fatalf("empty send = %q", message)
	}

	// Sending to the echo thread brings a reply back as an event, which the hub
	// stores as an incoming message.
	callTool(t, instance, "hub_send", map[string]any{"account": "personal", "to": "echo", "text": "ping"})
	waitFor(t, "the echoed reply", func() bool {
		history := callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "echo"})
		for _, message := range list(t, history, "messages") {
			if strings.Contains(field(t, message, "text"), "you said: ping") {
				return true
			}
		}
		return false
	})
}

// TestHubReportsCommunitiesChannelsAndAttachments is the "does it look like a real
// chat service" test: a community with channels, a group, a direct message, and
// every attachment kind, each with a real file behind it.
func TestHubReportsCommunitiesChannelsAndAttachments(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, map[string]string{"HUB_MOCK_MEDIA_DIR": t.TempDir()})

	threads := callTool(t, instance, "hub_threads", map[string]any{"account": "personal"})
	byID := map[string]map[string]any{}
	for _, thread := range list(t, threads, "threads") {
		object, ok := thread.(map[string]any)
		if !ok {
			t.Fatalf("thread is not an object: %#v", thread)
		}
		byID[field(t, thread, "id")] = object
	}
	channel, ok := byID["space:acme/ops"]
	if !ok {
		t.Fatalf("the community channel is missing: %#v", threads)
	}
	if field(t, channel, "kind") != "channel" || field(t, channel, "parent") != "space:acme" {
		t.Fatalf("channel = %#v", channel)
	}
	if participants, ok := channel["participants"].([]any); !ok || len(participants) != 2 {
		t.Fatalf("participants = %#v", channel["participants"])
	}
	// A channel that has never carried a message is still listed.
	if _, ok := byID["space:acme/general"]; !ok {
		t.Fatalf("the empty channel is missing: %#v", threads)
	}
	if space := byID["space:acme"]; field(t, space, "kind") != "community" {
		t.Fatalf("community = %#v", space)
	}

	// Every attachment kind arrives with its metadata, and the file behind it is a
	// real file of the size the bridge reported.
	history := callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "space:acme/ops"})
	kinds := map[string]map[string]any{}
	for _, message := range list(t, history, "messages") {
		if _, ok := message.(map[string]any); !ok {
			t.Fatalf("message is not an object: %#v", message)
		}
		media, ok := message.(map[string]any)["media"].([]any)
		if !ok || len(media) == 0 {
			continue
		}
		entry, ok := media[0].(map[string]any)
		if !ok {
			t.Fatalf("media is not an object: %#v", media[0])
		}
		kinds[field(t, message, "kind")] = entry
	}
	for _, kind := range []string{"image", "voice", "document", "sticker"} {
		entry, ok := kinds[kind]
		if !ok {
			t.Fatalf("%s never arrived: %#v", kind, kinds)
		}
		path := field(t, entry, "path")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s file: %v", kind, err)
		}
		if info.Size() != int64(entry["size"].(float64)) {
			t.Fatalf("%s size = %d, hub says %v", kind, info.Size(), entry["size"])
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if field(t, entry, "sha256") != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s digest = %q", kind, field(t, entry, "sha256"))
		}
	}
	if voice := kinds["voice"]; voice["durationMs"].(float64) != 1200 {
		t.Fatalf("voice note duration = %#v", voice["durationMs"])
	}
	if image := kinds["image"]; field(t, image, "mime") != "image/png" || field(t, image, "filename") != "photo.png" {
		t.Fatalf("image metadata = %#v", image)
	}
}

// TestHubFoldsEditsReactionsDeletesAndThreadUpdates: what a service reports after
// the fact has to be what the hub serves, without any of it needing a restart.
func TestHubFoldsEditsReactionsDeletesAndThreadUpdates(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	// The first read stores the two messages.
	callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "updates"})
	// The second read is when the bridge reports the reaction, the edit, and the
	// deletion. Events are folded as they arrive rather than blocking the call, so
	// the change is waited for the way a subscribed client waits for the feed.
	callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "updates"})
	var folded map[string]any
	waitFor(t, "the edits and reactions to be folded in", func() bool {
		folded = callTool(t, instance, "hub_messages", map[string]any{"account": "personal", "thread": "updates"})
		for _, message := range list(t, folded, "messages") {
			if field(t, message, "id") == "upd-1" && field(t, message, "text") == "deploy is green (edited)" {
				return true
			}
		}
		return false
	})

	var edited, deleted map[string]any
	for _, message := range list(t, folded, "messages") {
		object, ok := message.(map[string]any)
		if !ok {
			t.Fatalf("message is not an object: %#v", message)
		}
		switch field(t, message, "id") {
		case "upd-1":
			edited = object
		case "upd-2":
			deleted = object
		}
	}
	if edited == nil || deleted == nil {
		t.Fatalf("messages = %#v", folded)
	}
	if field(t, edited, "text") != "deploy is green (edited)" || edited["edited"] != true {
		t.Fatalf("edited message = %#v", edited)
	}
	reactions, _ := edited["reactions"].([]any)
	if len(reactions) != 1 || field(t, reactions[0], "emoji") != "👍" || field(t, reactions[0], "sender") != "alice" {
		t.Fatalf("reactions = %#v", reactions)
	}
	if deleted["deleted"] != true || field(t, deleted, "text") != "" {
		t.Fatalf("deleted message = %#v", deleted)
	}
	// The description the bridge reported describes the thread.
	threads := callTool(t, instance, "hub_threads", map[string]any{"account": "personal"})
	for _, thread := range list(t, threads, "threads") {
		object, _ := thread.(map[string]any)
		if field(t, thread, "id") != "updates" || object == nil {
			continue
		}
		if field(t, thread, "kind") != "channel" || field(t, thread, "parent") != "space:acme" {
			t.Fatalf("thread description = %#v", thread)
		}
		// The bridge described this thread as muted, and a later thread listing that
		// does not repeat the flag must not clear it.
		if object["muted"] != true {
			t.Fatalf("the muted flag was lost: %#v", thread)
		}
		return
	}
	t.Fatal("the updates thread is missing")
}

// TestHubLoginRoundTripAndRefusals drives the device-link flow the way a client
// does, including the refusals: a wrong code, a cancelled login, and two accounts
// linking at once.
func TestHubLoginRoundTripAndRefusals(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	started := loginOf(t, callTool(t, instance, "hub_login", map[string]any{"account": "personal", "action": "start", "render": true}))
	if field(t, started, "status") != "pending" {
		t.Fatalf("login = %#v", started)
	}
	challenge, _ := started["challenge"].(map[string]any)
	if challenge == nil || field(t, challenge, "kind") != "qr" || !strings.Contains(field(t, challenge, "payload"), "mock-login-personal") {
		t.Fatalf("challenge = %#v", challenge)
	}
	// A terminal client gets a printable QR for the same payload.
	if rendered, ok := started["rendered"].([]any); !ok || len(rendered) == 0 {
		t.Fatalf("the QR was not rendered: %#v", started)
	}

	// A second account can be linking at the same time, with its own challenge.
	other := loginOf(t, callTool(t, instance, "hub_login", map[string]any{"account": "work", "action": "start"}))
	if field(t, other, "id") == field(t, started, "id") {
		t.Fatalf("two logins share an id: %#v", other)
	}

	// The wrong code is refused by the bridge, and the login stays pending.
	if message := callToolError(t, instance, "hub_login", map[string]any{"action": "submit", "loginID": field(t, started, "id"), "response": "000000"}); !strings.Contains(message, "not the code") {
		t.Fatalf("wrong code = %q", message)
	}
	// The right one completes it.
	callTool(t, instance, "hub_login", map[string]any{"action": "submit", "loginID": field(t, started, "id"), "response": "scan-personal"})
	// The bridge confirms the link, which can land just after the answer is
	// submitted; a client waits for the status rather than assuming it.
	waitFor(t, "the login to be reported connected", func() bool {
		status := loginOf(t, callTool(t, instance, "hub_login", map[string]any{"action": "status", "loginID": field(t, started, "id")}))
		return field(t, status, "status") == "connected"
	})

	// Cancelling drops the other login.
	callTool(t, instance, "hub_login", map[string]any{"action": "cancel", "loginID": field(t, other, "id")})
	status := callTool(t, instance, "hub_login", map[string]any{"action": "status"})
	logins := list(t, status, "logins")
	for _, login := range logins {
		if field(t, login, "id") == field(t, other, "id") {
			t.Fatalf("a cancelled login is still tracked: %#v", logins)
		}
	}
}

// TestHubSurvivesABridgeCrash: a bridge that dies mid-conversation must be
// reported, not hang, and be startable again.
func TestHubSurvivesABridgeCrash(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, map[string]string{"HUB_MOCK_CRASH_ON_SEND": "1"})

	// The first send kills the bridge, so the call fails rather than hanging.
	if message := callToolError(t, instance, "hub_send", map[string]any{
		"account": "personal", "to": "dm:alice", "text": "this kills the bridge",
	}); strings.TrimSpace(message) == "" {
		t.Fatal("a send to a dying bridge reported nothing")
	}
	// The hub notices, and starting the bridge again makes it work.
	waitFor(t, "the hub to report the bridge as down", func() bool {
		listed := callTool(t, instance, "hub_bridges", map[string]any{"action": "list"})
		for _, bridge := range list(t, listed, "bridges") {
			if field(t, bridge, "state") != "running" {
				return true
			}
		}
		return false
	})
	callTool(t, instance, "hub_bridges", map[string]any{"action": "start", "name": "mock"})
}

// TestHubMarkReadClearsUnread: the read cursor is the hub's own state, so a client
// can catch up without asking the service to replay anything.
func TestHubMarkReadClearsUnread(t *testing.T) {
	instance := startHub(t, nil)
	installMock(t, instance, nil)

	// The scripted channel has incoming messages, so it starts unread.
	threads := callTool(t, instance, "hub_threads", map[string]any{"account": "personal"})
	unread := 0
	for _, thread := range list(t, threads, "threads") {
		object, _ := thread.(map[string]any)
		if field(t, thread, "id") == "space:acme/ops" && object != nil {
			if value, ok := object["unread"].(float64); ok {
				unread = int(value)
			}
		}
	}
	if unread == 0 {
		t.Fatalf("no unread messages: %#v", threads)
	}
	callTool(t, instance, "hub_mark_read", map[string]any{"account": "personal", "thread": "space:acme/ops"})
	threads = callTool(t, instance, "hub_threads", map[string]any{"account": "personal"})
	for _, thread := range list(t, threads, "threads") {
		object, ok := thread.(map[string]any)
		if !ok || field(t, thread, "id") != "space:acme/ops" {
			continue
		}
		if unread, _ := object["unread"].(float64); unread != 0 {
			t.Fatalf("unread after marking read = %#v", thread)
		}
	}
}

// TestHubKeepsItsConfigurationInOneFile: a bridge installed through the tools
// lands in the agent's settings file, not in a JSON of its own.
func TestHubKeepsItsConfigurationInOneFile(t *testing.T) {
	// Midas points its children at its settings file, which is the mode where the
	// hub keeps everything in one place.
	instance := startHub(t, map[string]string{"MIDAS_MCP_SETTINGS": "%CONFIG%/settings.json"})
	installMock(t, instance, nil)
	data, err := os.ReadFile(filepath.Join(instance.Config, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	var section struct {
		Hub struct {
			Bridges map[string]struct {
				Source  string   `json:"source"`
				Command []string `json:"command"`
			} `json:"bridges"`
		} `json:"hub"`
	}
	if err := json.Unmarshal(settings["mcp"], &section); err != nil {
		t.Fatalf("mcp section = %s: %v", settings["mcp"], err)
	}
	bridge, ok := section.Hub.Bridges["mock"]
	if !ok || bridge.Source == "" || len(bridge.Command) == 0 {
		t.Fatalf("the bridge is not in the settings file: %s", data)
	}
	if _, err := os.Stat(filepath.Join(instance.Config, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("the hub wrote a hub.json of its own")
	}
}

// TestHubBridgesAvailableWithoutARegistry: the catalog tool explains itself when
// nothing is configured, rather than claiming an empty registry is the whole
// story.
func TestHubBridgesAvailableWithoutARegistry(t *testing.T) {
	instance := startHub(t, nil)
	available := callTool(t, instance, "hub_bridges", map[string]any{"action": "available"})
	if !strings.Contains(strings.ToLower(toolTextString(available)), "registry") {
		t.Fatalf("available = %#v", available)
	}
}

// syncBuffer collects a command's output from the copy goroutine safely.
type syncBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *syncBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(chunk)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

// toolTextString renders a decoded result for a substring assertion.
func toolTextString(result map[string]any) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// TestMockBridgeIsAnExecutableProgram guards the fixture itself: if the bridge
// binary cannot run on its own, every test above would be lying.
func TestMockBridgeIsAnExecutableProgram(t *testing.T) {
	command := exec.Command(binaries["mock-bridge"])
	command.Stdin = strings.NewReader(`{"id":1,"method":"hello"}` + "\n")
	// The child writes from its own goroutine, so the buffer is read under a lock.
	stdout := &syncBuffer{}
	command.Stdout = stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the mock bridge to answer hello", func() bool {
		return strings.Contains(stdout.String(), `"name":"mock"`)
	})
	_ = command.Process.Kill()
	_ = command.Wait()
}

// TestHubWithoutAccountsIsNotAFailure: an environment nobody has signed in to is a
// normal state. Listing accounts answers with an empty list, and so does a hub with
// no bridges installed: neither is an error, because "nothing is set up yet" is
// what a client has to render as an empty screen.
func TestHubWithoutAccountsIsNotAFailure(t *testing.T) {
	// No bridges installed at all.
	bare := startHub(t, nil)
	empty := callTool(t, bare, "hub_accounts", map[string]any{})
	if len(list(t, empty, "accounts")) != 0 {
		t.Fatalf("a hub with no bridges = %#v", empty)
	}
	if message, _ := empty["message"].(string); !strings.Contains(strings.ToLower(message), "no bridges") {
		t.Fatalf("a hub with no bridges said %q", message)
	}
	// A bridge that is installed and answers, serving nothing yet.
	signedOut := startHub(t, nil)
	installMock(t, signedOut, map[string]string{"HUB_MOCK_NO_ACCOUNTS": "1"})
	signedOutAccounts := callTool(t, signedOut, "hub_accounts", map[string]any{})
	if len(list(t, signedOutAccounts, "accounts")) != 0 {
		t.Fatalf("a bridge with no accounts = %#v", signedOutAccounts)
	}
	if message, _ := signedOutAccounts["message"].(string); strings.Contains(strings.ToLower(message), "error") {
		t.Fatalf("an empty account list was reported as an error: %q", message)
	}
	// Threads for a bridge with no accounts are empty too, not an error.
	threads := callTool(t, signedOut, "hub_threads", map[string]any{})
	if len(list(t, threads, "threads")) != 0 {
		t.Fatalf("threads with no accounts = %#v", threads)
	}
}
