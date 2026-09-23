package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// loginPage is a small site that signs a user in, which is what the vault and
// control are used together for: one holds the credential, the other reaches the
// page it belongs to.
type loginPage struct {
	username string
	password string
	// served counts the requests that reached the page, so the test can prove the
	// browser really loaded it.
	served int
	mu     sync.Mutex
}

func (p *loginPage) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		p.served++
		p.mu.Unlock()
		fmt.Fprint(writer, `<!doctype html><html><body>
<input id="user" autocomplete="off">
<input id="pass" type="password" autocomplete="off">
<button id="go">Sign in</button>
<div id="result">signed out</div>
<script>
document.getElementById('go').onclick = async () => {
  const user = document.getElementById('user').value;
  const pass = document.getElementById('pass').value;
  const response = await fetch('/login', {method: 'POST', body: JSON.stringify({user, pass})});
  const body = await response.json();
  document.getElementById('result').textContent = body.ok ? 'signed in as ' + user : 'denied';
};
</script></body></html>`)
	})
	mux.HandleFunc("/login", func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		ok := body.User == p.username && body.Pass == p.password
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": ok})
	})
	return mux
}

// TestVaultCredentialSignsInThroughControl is the two servers working together: the
// vault holds the password, control reaches the page, and the page really
// authenticates with what the vault returned.
func TestVaultCredentialSignsInThroughControl(t *testing.T) {
	launched := startBrowser(t)
	page := &loginPage{username: "carl", password: "correct horse battery staple"}
	site := httptest.NewServer(page.handler())
	defer site.Close()

	// The vault: a password the agent reads the way it would for a user request.
	vault := startServer(t, "vault", nil)
	callTool(t, vault, "vault_create", map[string]any{"password": "vault password"})
	callTool(t, vault, "vault_put", map[string]any{
		"title": "Sign-in page", "username": page.username, "url": site.URL, "password": page.password,
	})
	credential := callTool(t, vault, "vault_password", map[string]any{"title": "Sign-in page"})
	username, _ := credential["username"].(string)
	password, _ := credential["password"].(string)
	if username == "" || password == "" {
		t.Fatalf("the vault returned no credential: %#v", credential)
	}

	// Control: the page's browser, found through the endpoint the browser wrote.
	control := controlFor(t, launched, nil)
	attached := callTool(t, control, "control_browser", map[string]any{"action": "attach", "browser": "Chromium"})
	status, _ := attached["status"].(map[string]any)
	if status == nil {
		t.Fatalf("attach = %#v", attached)
	}
	endpoint := strings.TrimSuffix(field(t, status, "url"), "/")
	if endpoint == "" {
		t.Fatalf("no endpoint: %#v", status)
	}

	// The page itself: open it, fill the form with the vault's credential, and let
	// its own script sign in.
	client := dialCDP(t, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, err := client.call(ctx, "Target.createTarget", map[string]any{"url": site.URL})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(target, &created); err != nil {
		t.Fatal(err)
	}
	attachedTarget, err := client.call(ctx, "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true})
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attachedTarget, &session); err != nil {
		t.Fatal(err)
	}

	// Wait for the page's script to be there, then drive it: the click the user
	// would make, with the password the vault holds. The tab is reused for the
	// denial check below, so the sign-in is done against the tab the test opened.
	fill := signInScript(username, password)
	waitFor(t, "the page to be ready", func() bool {
		value, err := client.evaluate(ctx, session.SessionID, fill)
		return err == nil && value == "clicked"
	})
	if !waitForSignIn(ctx, client, session.SessionID, "signed in as "+username) {
		text, _ := client.evaluate(ctx, session.SessionID, "document.getElementById('result').textContent")
		t.Fatalf("the page did not sign in with the vault's credential; it says %q", text)
	}
	page.mu.Lock()
	served := page.served
	page.mu.Unlock()
	if served == 0 {
		t.Fatal("the page was never really loaded by the browser")
	}

	// The same with a wrong password is denied, which is what shows the sign-in
	// above was real rather than unconditional.
	page.password = "something else"
	denied := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		_, err := client.evaluate(ctx, session.SessionID, fill)
		if err != nil {
			break
		}
		text, err := client.evaluate(ctx, session.SessionID, "document.getElementById('result').textContent")
		if err == nil && strings.Contains(text, "denied") {
			denied = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !denied {
		t.Fatal("a wrong password was accepted by the page")
	}
}

// dialCDP connects to a browser's debugger websocket and speaks CDP over it: the
// same protocol an agent uses once control has told it where the endpoint is.
type cdpClient struct {
	conn   *websocket.Conn
	mu     sync.Mutex
	next   int
	waiter map[int]chan json.RawMessage
	closed chan struct{}
}

func dialCDP(t *testing.T, endpoint string) *cdpClient {
	t.Helper()
	version := get(t, endpoint+"/json/version")
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal([]byte(version), &info); err != nil || info.WebSocketDebuggerURL == "" {
		t.Fatalf("no debugger websocket in %s", version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, info.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &cdpClient{conn: connection, waiter: map[int]chan json.RawMessage{}, closed: make(chan struct{})}
	go client.read()
	t.Cleanup(func() { _ = connection.CloseNow() })
	return client
}

func (c *cdpClient) read() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &message) != nil || message.ID == 0 {
			continue
		}
		c.mu.Lock()
		waiter := c.waiter[message.ID]
		delete(c.waiter, message.ID)
		c.mu.Unlock()
		if waiter != nil {
			waiter <- message.Result
		}
	}
}

func (c *cdpClient) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return c.callSession(ctx, "", method, params)
}

// callSession sends a command, optionally addressed to one flattened session.
func (c *cdpClient) callSession(ctx context.Context, sessionID, method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	waiter := make(chan json.RawMessage, 1)
	c.waiter[id] = waiter
	c.mu.Unlock()
	message := map[string]any{"id": id, "method": method}
	if sessionID != "" {
		message["sessionId"] = sessionID
	}
	if params != nil {
		message["params"] = params
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		return nil, err
	}
	select {
	case result := <-waiter:
		return result, nil
	case <-c.closed:
		return nil, fmt.Errorf("the debugger connection closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *cdpClient) evaluate(ctx context.Context, sessionID, expression string) (string, error) {
	result, err := c.callSession(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return "", err
	}
	var evaluated struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &evaluated); err != nil {
		return "", err
	}
	if evaluated.ExceptionDetails != nil {
		return "", fmt.Errorf("%s", evaluated.ExceptionDetails.Text)
	}
	text, _ := evaluated.Result.Value.(string)
	return text, nil
}

// TestInboundMessageSignsInWithTheVault is the three servers in one flow, the way
// an agent actually uses them: a message arrives through the hub, the credential
// it asks for comes from the vault, control reaches the page, and the outcome goes
// back through the hub.
func TestInboundMessageSignsInWithTheVault(t *testing.T) {
	launched := startBrowser(t)
	page := &loginPage{username: "carl", password: "correct horse battery staple"}
	site := httptest.NewServer(page.handler())
	defer site.Close()

	// The hub: the mock bridge delivers an instruction from the service, and the
	// thread it lands in is where the answer goes back.
	hub := startHub(t, nil)
	instruction := "please sign in to " + site.URL + " with the password saved as \"Sign-in page\""
	installMock(t, hub, map[string]string{
		"HUB_MOCK_INJECT": "50ms", "HUB_MOCK_INJECT_TEXT": instruction,
	})
	callTool(t, hub, "hub_accounts", map[string]any{})
	var message map[string]any
	waitFor(t, "the instruction to arrive", func() bool {
		history := callTool(t, hub, "hub_messages", map[string]any{"account": "personal", "thread": "dm:alice"})
		for _, candidate := range list(t, history, "messages") {
			object, _ := candidate.(map[string]any)
			if object != nil && strings.Contains(field(t, candidate, "text"), "please sign in") {
				message = object
				return true
			}
		}
		return false
	})

	// The vault: whatever the message names is what the agent looks up.
	vault := startServer(t, "vault", nil)
	callTool(t, vault, "vault_create", map[string]any{"password": "vault password"})
	callTool(t, vault, "vault_put", map[string]any{
		"title": "Sign-in page", "username": page.username, "url": site.URL, "password": page.password,
	})
	credential := callTool(t, vault, "vault_password", map[string]any{"title": "Sign-in page"})
	username, _ := credential["username"].(string)
	password, _ := credential["password"].(string)

	// Control: the browser the page needs, found through its own endpoint.
	control := controlFor(t, launched, nil)
	attached := callTool(t, control, "control_browser", map[string]any{"action": "attach", "browser": "Chromium"})
	status, _ := attached["status"].(map[string]any)
	if status == nil || field(t, status, "url") == "" {
		t.Fatalf("no browser endpoint: %#v", attached)
	}

	client := dialCDP(t, strings.TrimSuffix(field(t, status, "url"), "/"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	signedIn := signIn(t, ctx, client, site.URL, username, password)
	if !signedIn {
		t.Fatal("the page did not sign in with the credential from the vault")
	}

	// The answer goes back through the hub, in the thread the request came from.
	callTool(t, hub, "hub_send", map[string]any{
		"account": "personal", "to": "dm:alice", "replyTo": field(t, message, "id"),
		"text": "signed in as " + username,
	})
	history := callTool(t, hub, "hub_messages", map[string]any{"account": "personal", "thread": "dm:alice"})
	replied := false
	for _, candidate := range list(t, history, "messages") {
		if strings.Contains(field(t, candidate, "text"), "signed in as "+username) {
			replied = true
		}
	}
	if !replied {
		t.Fatalf("the answer never reached the thread: %#v", history)
	}
}

// signInScript is the click a user would make, with the credentials it is given.
func signInScript(username, password string) string {
	return fmt.Sprintf(`(() => {
	  const user = document.getElementById('user');
	  const pass = document.getElementById('pass');
	  if (!user || !pass) return 'not ready';
	  user.value = %q;
	  pass.value = %q;
	  document.getElementById('go').click();
	  return 'clicked';
	})()`, username, password)
}

// waitForSignIn waits for the page to report the text a successful sign-in shows.
func waitForSignIn(ctx context.Context, client *cdpClient, sessionID, expected string) bool {
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		text, err := client.evaluate(ctx, sessionID, "document.getElementById('result').textContent")
		if err == nil && strings.Contains(text, expected) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// signIn opens a page in the browser and lets its own script log in with the
// credentials it is given.
func signIn(t *testing.T, ctx context.Context, client *cdpClient, url, username, password string) bool {
	t.Helper()
	target, err := client.call(ctx, "Target.createTarget", map[string]any{"url": url})
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(target, &created); err != nil {
		t.Fatal(err)
	}
	attached, err := client.call(ctx, "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true})
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attached, &session); err != nil {
		t.Fatal(err)
	}
	fill := signInScript(username, password)
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		value, err := client.evaluate(ctx, session.SessionID, fill)
		if err == nil && value == "clicked" {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		return false
	}
	return waitForSignIn(ctx, client, session.SessionID, "signed in as "+username)
}
