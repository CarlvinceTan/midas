package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"time"

	"github.com/coder/websocket"

	"github.com/CarlvinceTan/midas/pkg/vault"
)

// testEnvironment builds an environment with scripted agents, so the API can be
// tested without a model, a browser or an MCP server.
func testEnvironment(t *testing.T) *Environment {
	t.Helper()
	broker := testBroker(t)
	registry := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orchestrator, err := NewAgent(broker, "orchestrator", "orchestrator", scriptedBrain{handle: func(ctx context.Context, request Request) (string, error) {
		answer, err := request.Ask(ctx, "worker", request.Text)
		if err != nil {
			return "", err
		}
		return "worker said " + answer, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewAgent(broker, "worker", "worker", scriptedBrain{handle: func(_ context.Context, request Request) (string, error) {
		return "done: " + request.Text, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(orchestrator)
	registry.Add(worker)
	go orchestrator.Run(ctx)
	go worker.Run(ctx)
	t.Cleanup(func() {
		cancel()
		orchestrator.Wait()
		worker.Wait()
	})

	environment := &Environment{
		Config: Config{Token: "test-token", VaultMode: VaultAutonomous, Agents: []AgentConfig{{Address: "orchestrator", Role: "orchestrator"}, {Address: "worker", Role: "worker"}}},
		Broker: broker, Registry: registry, Leases: NewLeases(time.Minute),
		VaultMode: VaultAutonomous, Feed: NewFeed(), state: newTeamsState(nil, nil),
	}
	// The same wiring the real environment does: bus traffic becomes a change.
	broker.Subscribe(func(envelope Envelope) {
		copied := envelope
		environment.publish(Event{Kind: EventMessage, Action: string(envelope.Kind), At: envelope.At, Message: &copied})
	})
	return environment
}

func call(t *testing.T, environment *Environment, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	environment.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAPIRequiresTheEnvironmentToken(t *testing.T) {
	environment := testEnvironment(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/agents", nil)
	recorder := httptest.NewRecorder()
	environment.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request = %d", recorder.Code)
	}
	// The health check is deliberately open, for a load balancer.
	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder = httptest.NewRecorder()
	environment.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health = %d", recorder.Code)
	}
}

// TestUserMessageTravelsToAnAgentAndBack covers the user link end to end over the
// API: the user posts to a group, the orchestrator delegates to the worker, and
// both halves show up in the group transcript.
func TestUserMessageTravelsToAnAgentAndBack(t *testing.T) {
	environment := testEnvironment(t)
	if response := call(t, environment, http.MethodPost, "/v1/groups", `{"id":"project","title":"Project"}`); response.Code != http.StatusCreated {
		t.Fatalf("create group = %d: %s", response.Code, response.Body)
	}
	response := call(t, environment, http.MethodPost, "/v1/groups/project/messages", `{"text":"prepare the release"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("send = %d: %s", response.Code, response.Body)
	}
	// The reply arrives asynchronously, so wait for it rather than sleeping a
	// guessed amount.
	deadline := time.Now().Add(3 * time.Second)
	var messages []Envelope
	for time.Now().Before(deadline) {
		body := call(t, environment, http.MethodGet, "/v1/groups/project/messages", "").Body.String()
		var listing struct {
			Messages []Envelope `json:"messages"`
		}
		if err := json.Unmarshal([]byte(body), &listing); err != nil {
			t.Fatal(err)
		}
		messages = listing.Messages
		if len(messages) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	path := []string{}
	for _, message := range messages {
		path = append(path, message.From+"→"+message.To)
	}
	joined := strings.Join(path, ", ")
	if !strings.Contains(joined, "user→orchestrator") || !strings.Contains(joined, "orchestrator→worker") {
		t.Fatalf("transcript = %s", joined)
	}
	// An addressed message goes where it was addressed.
	if response := call(t, environment, http.MethodPost, "/v1/groups/project/messages", `{"text":"ping","to":"worker"}`); response.Code != http.StatusAccepted {
		t.Fatalf("addressed send = %d", response.Code)
	}
	// A message with no text is refused rather than queued blank.
	if response := call(t, environment, http.MethodPost, "/v1/groups/project/messages", `{"text":"   "}`); response.Code != http.StatusBadRequest {
		t.Fatalf("blank send = %d", response.Code)
	}
	// And an unknown group still accepts messages: a group is created by use.
	if response := call(t, environment, http.MethodGet, "/v1/groups/missing/messages", ""); response.Code != http.StatusOK {
		t.Fatalf("unknown group = %d", response.Code)
	}
	if response := call(t, environment, http.MethodGet, "/v1/agents", ""); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "orchestrator") {
		t.Fatalf("agents = %d: %s", response.Code, response.Body)
	}
}

func TestLeasesSerialiseASharedResource(t *testing.T) {
	environment := testEnvironment(t)
	first := call(t, environment, http.MethodPost, "/v1/leases", `{"resource":"browser","holder":"orchestrator","ttlSeconds":60}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("grant = %d: %s", first.Code, first.Body)
	}
	// The same holder may renew, another agent is refused with who holds it.
	if renew := call(t, environment, http.MethodPost, "/v1/leases", `{"resource":"browser","holder":"orchestrator"}`); renew.Code != http.StatusCreated {
		t.Fatalf("renew = %d", renew.Code)
	}
	conflict := call(t, environment, http.MethodPost, "/v1/leases", `{"resource":"browser","holder":"worker"}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "orchestrator") {
		t.Fatalf("conflict = %d: %s", conflict.Code, conflict.Body)
	}
	// Only the holder can release it.
	if wrong := call(t, environment, http.MethodDelete, "/v1/leases/browser?holder=worker", ""); wrong.Code != http.StatusConflict {
		t.Fatalf("release by a stranger = %d", wrong.Code)
	}
	if released := call(t, environment, http.MethodDelete, "/v1/leases/browser?holder=orchestrator", ""); released.Code != http.StatusOK {
		t.Fatalf("release = %d", released.Code)
	}
	if listing := call(t, environment, http.MethodGet, "/v1/leases", ""); !strings.Contains(listing.Body.String(), `"leases":[]`) {
		t.Fatalf("leases after release = %s", listing.Body)
	}
	// Leases expire, so a crashed holder does not block the resource forever.
	expiring := testEnvironment(t)
	expiring.Leases = NewLeases(time.Millisecond)
	// No ttlSeconds, so the registry's own short default applies.
	call(t, expiring, http.MethodPost, "/v1/leases", `{"resource":"device","holder":"worker"}`)
	time.Sleep(10 * time.Millisecond)
	if listing := call(t, expiring, http.MethodGet, "/v1/leases", ""); !strings.Contains(listing.Body.String(), `"leases":[]`) {
		t.Fatalf("expiry = %s", listing.Body)
	}
}

func TestAPIListsHostsAndTheSharedPool(t *testing.T) {
	environment := testEnvironment(t)
	environment.Config.Hosts = []HostConfig{{Name: "server", Local: true, Default: true}}
	hosts := call(t, environment, http.MethodGet, "/v1/hosts", "")
	if hosts.Code != http.StatusOK || !strings.Contains(hosts.Body.String(), `"name":"server"`) {
		t.Fatalf("hosts = %d: %s", hosts.Code, hosts.Body)
	}
	// No pool configured is not an error: the environment simply has no shared MCP
	// servers, and it says so rather than failing the request.
	pool := call(t, environment, http.MethodGet, "/v1/mcps", "")
	if pool.Code != http.StatusOK || !strings.Contains(pool.Body.String(), `"servers":[]`) {
		t.Fatalf("mcps = %d: %s", pool.Code, pool.Body)
	}
}

func TestVaultModeSwitchesOverTheAPI(t *testing.T) {
	environment := testEnvironment(t)
	// Autonomous is the default: unlocked, because a server has no user at the
	// keyboard to approve anything.
	status := call(t, environment, http.MethodGet, "/v1/vault/mode", "")
	if !strings.Contains(status.Body.String(), VaultAutonomous) || !strings.Contains(status.Body.String(), `"unlocked":true`) {
		t.Fatalf("default mode = %s", status.Body)
	}
	// Switching to user permission locks it, and it stays locked until the master
	// password arrives.
	switched := call(t, environment, http.MethodPut, "/v1/vault/mode", `{"mode":"user-permission"}`)
	if switched.Code != http.StatusOK || !strings.Contains(switched.Body.String(), `"unlocked":false`) {
		t.Fatalf("switch = %d: %s", switched.Code, switched.Body)
	}
	if blank := call(t, environment, http.MethodPost, "/v1/vault/unlock", `{"password":"  "}`); blank.Code != http.StatusBadRequest {
		t.Fatalf("blank password = %d", blank.Code)
	}
	// Unlocking is verified against the database: a password that cannot open the
	// vault must not be reported as unlocked.
	environment.ConfigDir = t.TempDir()
	if wrong := call(t, environment, http.MethodPost, "/v1/vault/unlock", `{"password":"correct horse"}`); wrong.Code != http.StatusConflict {
		t.Fatalf("unlock without a vault = %d: %s", wrong.Code, wrong.Body)
	}
	if _, err := vault.Create(vault.DefaultPath(environment.ConfigDir), "correct horse"); err != nil {
		t.Fatal(err)
	}
	if wrong := call(t, environment, http.MethodPost, "/v1/vault/unlock", `{"password":"wrong horse"}`); wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d: %s", wrong.Code, wrong.Body)
	}
	unlocked := call(t, environment, http.MethodPost, "/v1/vault/unlock", `{"password":"correct horse"}`)
	if unlocked.Code != http.StatusOK || !strings.Contains(unlocked.Body.String(), `"unlocked":true`) {
		t.Fatalf("unlock = %d: %s", unlocked.Code, unlocked.Body)
	}
	// Back to autonomous for the next deployment, persisted to the config file.
	config := Config{Token: "test-token", VaultMode: VaultAutonomous}
	environment.ConfigDir = t.TempDir()
	environment.Config = config
	back := call(t, environment, http.MethodPut, "/v1/vault/mode", `{"mode":"autonomous"}`)
	if back.Code != http.StatusOK {
		t.Fatalf("back = %d", back.Code)
	}
	saved, err := Load(environment.ConfigDir)
	if err != nil || saved.VaultMode != VaultAutonomous {
		t.Fatalf("saved mode = %q, %v", saved.VaultMode, err)
	}
	if invalid := call(t, environment, http.MethodPut, "/v1/vault/mode", `{"mode":"whatever"}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode = %d", invalid.Code)
	}
}

// eventsFrom reads a change feed in the background, so a test can bound its wait
// instead of blocking forever on a stream that has gone quiet.
func eventsFrom(reader *bufio.Reader) <-chan Event {
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &event); err != nil {
				continue
			}
			events <- event
		}
	}()
	return events
}

// startTestServer runs the environment on its own listener. A streaming handler
// only returns when its client goes away, and httptest.Server.Close waits for
// handlers, so these tests own the server and close it hard.
func startTestServer(t *testing.T, environment *Environment) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: environment.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return "http://" + listener.Addr().String()
}

func TestChangeFeedStreamsMessagesWithoutPolling(t *testing.T) {
	environment := testEnvironment(t)
	base := startTestServer(t, environment)
	stream, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	request, err := http.NewRequestWithContext(stream, http.MethodGet, base+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	// Give the subscription a moment, then send: the event has to arrive without
	// the client asking for it again.
	time.Sleep(50 * time.Millisecond)
	call(t, environment, http.MethodPost, "/v1/groups/feed/messages", `{"text":"live","to":"worker"}`)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind == EventMessage && event.Message != nil && event.Message.Text == "live" && event.Message.Group == "feed" {
			return
		}
	}
	t.Fatal("the change feed never delivered the message")
}

// TestChangeFeedCarriesEveryKindOfChange covers the objective's requirement that a
// client learns about teams, groups and hosts without polling.
func TestChangeFeedCarriesEveryKindOfChange(t *testing.T) {
	environment := testEnvironment(t)
	base := startTestServer(t, environment)
	stream, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	request, err := http.NewRequestWithContext(stream, http.MethodGet, base+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	events := eventsFrom(bufio.NewReader(response.Body))
	time.Sleep(50 * time.Millisecond)

	call(t, environment, http.MethodPost, "/v1/teams", `{"id":"ops","name":"Operations"}`)
	call(t, environment, http.MethodPost, "/v1/groups", `{"id":"release","title":"Release","team":"ops"}`)
	call(t, environment, http.MethodPut, "/v1/groups/release", `{"title":"Release train"}`)
	call(t, environment, http.MethodDelete, "/v1/groups/release", "")
	call(t, environment, http.MethodPost, "/v1/leases", `{"resource":"browser","holder":"orchestrator"}`)
	call(t, environment, http.MethodPut, "/v1/vault/mode", `{"mode":"user-permission"}`)

	// Pairing a device is the host change: the code comes back to the caller that
	// started the handshake, and completing it registers the device.
	pairing := call(t, environment, http.MethodPost, "/v1/hosts/pair", `{"name":"phone"}`)
	var code struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(pairing.Body.Bytes(), &code); err != nil || code.Code == "" {
		t.Fatalf("pairing = %d: %s", pairing.Code, pairing.Body)
	}
	if completed := call(t, environment, http.MethodPost, "/v1/hosts/pair/complete", `{"code":"`+code.Code+`"}`); completed.Code != http.StatusOK {
		t.Fatalf("pairing completion = %d: %s", completed.Code, completed.Body)
	}
	if reused := call(t, environment, http.MethodPost, "/v1/hosts/pair/complete", `{"code":"`+code.Code+`"}`); reused.Code != http.StatusConflict {
		t.Fatalf("a pairing code was reusable: %d", reused.Code)
	}
	if defaults := call(t, environment, http.MethodPut, "/v1/hosts/phone/default", ""); defaults.Code != http.StatusOK ||
		!strings.Contains(defaults.Body.String(), `"default":true`) {
		t.Fatalf("default host = %d: %s", defaults.Code, defaults.Body)
	}
	if local := call(t, environment, http.MethodDelete, "/v1/hosts/local", ""); local.Code != http.StatusConflict {
		t.Fatalf("the local host was removable: %d", local.Code)
	}

	seen := map[string]string{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 4 {
		select {
		case event, open := <-events:
			if !open {
				t.Fatalf("the feed closed early (seen %#v)", seen)
			}
			seen[event.Kind] = event.Action
		case <-deadline:
			t.Fatalf("the feed went quiet (seen %#v)", seen)
		}
	}
	for kind, action := range map[string]string{
		EventTeam: "created", EventGroup: "removed", EventHost: "paired", EventVault: "user-permission",
	} {
		if seen[kind] != action {
			t.Fatalf("%s change = %q, want %q (seen: %#v)", kind, seen[kind], action, seen)
		}
	}
}

// TestSocketCarriesTheFeedAndAcceptsSends is the WebSocket half of the user
// transport: one connection for both directions.
func TestSocketCarriesTheFeedAndAcceptsSends(t *testing.T) {
	environment := testEnvironment(t)
	base := startTestServer(t, environment)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/socket?token=test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()

	// A send over the socket reaches the agent, and the change feed reports it on
	// the same connection.
	request := socketRequest{Action: "send", To: "worker", Group: "socket", Text: "over the wire"}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.Write(ctx, websocket.MessageText, encoded); err != nil {
		t.Fatal(err)
	}
	// An event and a reply both carry a "message" field, so the kind tells them
	// apart: presence of "ok" means it is a reply.
	sawReply, sawEvent := false, false
	for !sawReply || !sawEvent {
		_, data, err := socket.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (reply=%v event=%v)", err, sawReply, sawEvent)
		}
		var probe struct {
			Kind    string    `json:"kind"`
			OK      *bool     `json:"ok"`
			Message *Envelope `json:"message"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatalf("socket sent invalid JSON: %s", data)
		}
		switch {
		case probe.OK != nil:
			sawReply = *probe.OK && probe.Message != nil && probe.Message.Text == "over the wire"
		case probe.Kind == EventMessage:
			sawEvent = probe.Message != nil && probe.Message.Text == "over the wire"
		}
	}
	// An unauthenticated socket is refused: the token is not optional.
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/socket?token=wrong", nil); err == nil {
		t.Fatal("the socket accepted a wrong token")
	}
}

// TestAgentSettingsAreEditableAndPersisted covers the registry's write side: an
// operator retunes one agent without editing the file by hand, and the change
// survives a restart.
func TestAgentSettingsAreEditableAndPersisted(t *testing.T) {
	environment := testEnvironment(t)
	environment.ConfigDir = t.TempDir()
	if err := Save(environment.ConfigDir, environment.Config); err != nil {
		t.Fatal(err)
	}

	updated := call(t, environment, http.MethodPut, "/v1/agents/worker/settings",
		`{"model":"anthropic/claude-sonnet-4.5","prompt":"be terse","role":"worker"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("settings = %d: %s", updated.Code, updated.Body)
	}
	if !strings.Contains(updated.Body.String(), "claude-sonnet-4.5") {
		t.Fatalf("settings body = %s", updated.Body)
	}
	// It is on disk, so a redeploy keeps the tuning.
	saved, err := Load(environment.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, agent := range saved.Agents {
		if agent.Address == "worker" {
			found = agent.Model == "anthropic/claude-sonnet-4.5" && agent.Prompt == "be terse"
		}
	}
	if !found {
		t.Fatalf("saved agents = %#v", saved.Agents)
	}
	// Partial updates leave the other fields alone.
	partial := call(t, environment, http.MethodPut, "/v1/agents/worker/settings", `{"prompt":"even terser"}`)
	if partial.Code != http.StatusOK || !strings.Contains(partial.Body.String(), "claude-sonnet-4.5") {
		t.Fatalf("partial update = %d: %s", partial.Code, partial.Body)
	}
	// An unknown agent is refused rather than created on the fly.
	if missing := call(t, environment, http.MethodPut, "/v1/agents/nobody/settings", `{"model":"x/y"}`); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", missing.Code)
	}
	// The registry serves one agent on its own, which is what a client polls for
	// status.
	single := call(t, environment, http.MethodGet, "/v1/agents/worker", "")
	if single.Code != http.StatusOK || !strings.Contains(single.Body.String(), `"address":"worker"`) {
		t.Fatalf("agent = %d: %s", single.Code, single.Body)
	}
	if absent := call(t, environment, http.MethodGet, "/v1/agents/nobody", ""); absent.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", absent.Code)
	}
}

// TestTeamsAndGroupsAreManaged covers the teams half of the user transport:
// create, update, remove, and the group's own create/update/remove.
func TestTeamsAndGroupsAreManaged(t *testing.T) {
	environment := testEnvironment(t)
	team := call(t, environment, http.MethodPost, "/v1/teams", `{"id":"ops","name":"Operations","members":["user","orchestrator"]}`)
	if team.Code != http.StatusCreated {
		t.Fatalf("team = %d: %s", team.Code, team.Body)
	}
	if listed := call(t, environment, http.MethodGet, "/v1/teams", ""); !strings.Contains(listed.Body.String(), `"name":"Operations"`) {
		t.Fatalf("teams = %s", listed.Body)
	}
	group := call(t, environment, http.MethodPost, "/v1/groups", `{"id":"release","title":"Release","team":"ops"}`)
	if group.Code != http.StatusCreated || !strings.Contains(group.Body.String(), `"team":"ops"`) {
		t.Fatalf("group = %d: %s", group.Code, group.Body)
	}
	// Updating keeps the fields the caller did not mention.
	updated := call(t, environment, http.MethodPut, "/v1/groups/release", `{"title":"Release train"}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "Release train") ||
		!strings.Contains(updated.Body.String(), `"team":"ops"`) {
		t.Fatalf("group update = %d: %s", updated.Code, updated.Body)
	}
	if marked := call(t, environment, http.MethodPost, "/v1/groups/release/mark-read", ""); marked.Code != http.StatusOK {
		t.Fatalf("mark read = %d", marked.Code)
	}
	// Removing a team takes its groups with it: a conversation with no team is not
	// left pointing at one that is gone.
	if removed := call(t, environment, http.MethodDelete, "/v1/teams/ops", ""); removed.Code != http.StatusOK {
		t.Fatalf("team removal = %d", removed.Code)
	}
	if groups := call(t, environment, http.MethodGet, "/v1/groups", ""); !strings.Contains(groups.Body.String(), `"groups":[]`) {
		t.Fatalf("groups after team removal = %s", groups.Body)
	}
	if missing := call(t, environment, http.MethodDelete, "/v1/teams/ops", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("removing a missing team = %d", missing.Code)
	}
	if missing := call(t, environment, http.MethodDelete, "/v1/groups/release", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("removing a missing group = %d", missing.Code)
	}
}
