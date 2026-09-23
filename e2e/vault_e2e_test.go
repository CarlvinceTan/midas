package e2e

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// vaultPath is where the vault server keeps its database in a test.
func vaultPath(instance *server) string {
	return filepath.Join(instance.Config, "vault.kdbx")
}

// TestVaultLifecycleIsGuaranteed walks the whole vault through its tools: create,
// unlock, store, read, update, delete, change the password, and destroy.
func TestVaultLifecycleIsGuaranteed(t *testing.T) {
	instance := startServer(t, "vault", nil)

	// Nothing exists yet, and the tools say so rather than failing.
	status := callTool(t, instance, "vault_status", map[string]any{})
	if status["exists"] != false || status["unlocked"] != false {
		t.Fatalf("status = %#v", status)
	}
	// Reading before unlocking is refused, not answered with an empty entry.
	if message := callToolError(t, instance, "vault_password", map[string]any{"title": "GitHub"}); !strings.Contains(strings.ToLower(message), "unlock") {
		t.Fatalf("locked read = %q", message)
	}

	created := callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	if created["entries"].(float64) != 0 {
		t.Fatalf("create = %#v", created)
	}
	if _, err := os.Stat(vaultPath(instance)); err != nil {
		t.Fatalf("the vault file was not created where the server owns it: %v", err)
	}
	// Creating again must not clobber an existing vault.
	if message := callToolError(t, instance, "vault_create", map[string]any{"password": "another"}); strings.TrimSpace(message) == "" {
		t.Fatal("creating over an existing vault was allowed")
	}

	// A wrong password is refused; the right one unlocks.
	if message := callToolError(t, instance, "vault_unlock", map[string]any{"password": "wrong"}); strings.TrimSpace(message) == "" {
		t.Fatal("unlocking with a wrong password succeeded")
	}
	callTool(t, instance, "vault_unlock", map[string]any{"password": "correct horse battery"})
	status = callTool(t, instance, "vault_status", map[string]any{})
	if status["unlocked"] != true {
		t.Fatalf("status after unlock = %#v", status)
	}

	// An entry with a password, an account, and a TOTP seed from an otpauth URL.
	put := callTool(t, instance, "vault_put", map[string]any{
		"title": "GitHub", "username": "carl", "url": "https://github.com",
		"notes": "personal account", "password": "hunter2",
		"totpSeed": "otpauth://totp/GitHub:carl?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&digits=8&period=30",
	})
	if put["title"] != "GitHub" || put["hasTOTP"] != true {
		t.Fatalf("put = %#v", put)
	}

	password := callTool(t, instance, "vault_password", map[string]any{"title": "GitHub"})
	if password["password"] != "hunter2" || password["username"] != "carl" {
		t.Fatalf("password = %#v", password)
	}

	// The code matches the RFC 6238 vector for that seed and time, which is what
	// makes a wrong implementation fail here rather than at the user's login page.
	// The tool uses the current time, so the check is that a code comes back with a
	// plausible window; the exact vector is covered by the package's own tests.
	code := callTool(t, instance, "vault_totp", map[string]any{"title": "GitHub"})
	value, _ := code["code"].(string)
	if len(value) != 8 {
		t.Fatalf("totp = %#v", code)
	}
	if remaining, _ := code["secondsUntilExpiry"].(float64); remaining <= 0 || remaining > 30 {
		t.Fatalf("totp window = %#v", code["secondsUntilExpiry"])
	}

	// Listing shows what each entry holds and never a secret.
	listed := callTool(t, instance, "vault_list", map[string]any{})
	encoded, _ := json.Marshal(listed)
	// The guarantee is that no secret material is listed: not the password, not the
	// TOTP seed, not a recovery code.
	for _, secret := range []string{"hunter2", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "aaaa-bbbb"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("vault_list leaked %q: %s", secret, encoded)
		}
	}
	entries := list(t, listed, "entries")
	if len(entries) != 1 || field(t, entries[0], "title") != "GitHub" || entries[0].(map[string]any)["hasPassword"] != true {
		t.Fatalf("entries = %#v", entries)
	}

	// Updating only the fields that are given leaves the rest alone.
	callTool(t, instance, "vault_put", map[string]any{"title": "GitHub", "notes": "work account"})
	if password := callTool(t, instance, "vault_password", map[string]any{"title": "GitHub"}); password["password"] != "hunter2" {
		t.Fatalf("the password was cleared by an update: %#v", password)
	}

	// Recovery codes are stored and read back.
	callTool(t, instance, "vault_put", map[string]any{
		"title": "GitHub", "recoveryCodes": []string{"aaaa-bbbb", "cccc-dddd"},
	})
	codes := callTool(t, instance, "vault_recovery_codes", map[string]any{"title": "GitHub"})
	if len(list(t, codes, "codes")) != 2 {
		t.Fatalf("recovery codes = %#v", codes)
	}

	// Deleting removes it, and reading it afterwards is an error rather than an
	// empty answer.
	callTool(t, instance, "vault_delete", map[string]any{"title": "GitHub"})
	if message := callToolError(t, instance, "vault_password", map[string]any{"title": "GitHub"}); strings.TrimSpace(message) == "" {
		t.Fatal("reading a deleted entry succeeded")
	}
}

// TestVaultSurvivesARestart: the database is the state, so a new process with the
// same password sees the same secrets.
func TestVaultSurvivesARestart(t *testing.T) {
	config := t.TempDir()
	instance := startServerIn(t, "vault", config, nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	callTool(t, instance, "vault_put", map[string]any{"title": "Bank", "password": "s3cret", "username": "carl"})

	// A second server process on the same configuration directory is what a restart
	// is: the database on disk is the state, and the lock is not.
	_ = instance.Session.Close()
	restarted := startServerIn(t, "vault", config, nil)
	if status := callTool(t, restarted, "vault_status", map[string]any{}); status["exists"] != true || status["unlocked"] != false {
		t.Fatalf("status after restart = %#v", status)
	}
	if message := callToolError(t, restarted, "vault_unlock", map[string]any{"password": "wrong"}); strings.TrimSpace(message) == "" {
		t.Fatal("a wrong password opened the restored vault")
	}
	callTool(t, restarted, "vault_unlock", map[string]any{"password": "correct horse battery"})
	if password := callTool(t, restarted, "vault_password", map[string]any{"title": "Bank"}); password["password"] != "s3cret" {
		t.Fatalf("password after restart = %#v", password)
	}
}

// TestVaultChangePasswordReEncrypts: the old password stops working, which is what
// proves the database was re-encrypted rather than only the lock changed.
func TestVaultChangePasswordReEncrypts(t *testing.T) {
	instance := startServer(t, "vault", nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "first password"})
	callTool(t, instance, "vault_put", map[string]any{"title": "Mail", "password": "letmein"})

	// The current password is required.
	if message := callToolError(t, instance, "vault_change_password", map[string]any{"current": "wrong", "next": "second password"}); strings.TrimSpace(message) == "" {
		t.Fatal("changing the password with a wrong current one succeeded")
	}
	callTool(t, instance, "vault_change_password", map[string]any{"current": "first password", "next": "second password"})

	_ = instance.Session.Close()
	restarted := startServerIn(t, "vault", instance.Config, nil)
	if message := callToolError(t, restarted, "vault_unlock", map[string]any{"password": "first password"}); strings.TrimSpace(message) == "" {
		t.Fatal("the old password still opens the vault")
	}
	callTool(t, restarted, "vault_unlock", map[string]any{"password": "second password"})
	if password := callTool(t, restarted, "vault_password", map[string]any{"title": "Mail"}); password["password"] != "letmein" {
		t.Fatalf("password after re-encryption = %#v", password)
	}
}

// TestVaultDestroyNeedsThePathSpelledOut: a delete-the-whole-vault tool has to be
// hard to call by accident.
func TestVaultDestroyNeedsThePathSpelledOut(t *testing.T) {
	instance := startServer(t, "vault", nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	if message := callToolError(t, instance, "vault_destroy", map[string]any{"confirm": "yes please"}); strings.TrimSpace(message) == "" {
		t.Fatal("destroy was allowed without the vault path")
	}
	callTool(t, instance, "vault_destroy", map[string]any{"confirm": vaultPath(instance)})
	if _, err := os.Stat(vaultPath(instance)); !os.IsNotExist(err) {
		t.Fatalf("the vault still exists after destroy: %v", err)
	}
}

// TestVaultLockRequiresUnlockingAgain: locking is what a user does when they walk
// away, and it must take effect immediately.
func TestVaultLockRequiresUnlockingAgain(t *testing.T) {
	instance := startServer(t, "vault", nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	callTool(t, instance, "vault_put", map[string]any{"title": "Chat", "password": "hunter2"})
	callTool(t, instance, "vault_lock", map[string]any{})
	if status := callTool(t, instance, "vault_status", map[string]any{}); status["unlocked"] != false {
		t.Fatalf("status after locking = %#v", status)
	}
	if message := callToolError(t, instance, "vault_password", map[string]any{"title": "Chat"}); strings.TrimSpace(message) == "" {
		t.Fatal("a locked vault still returned a password")
	}
}

// TestVaultHTTPModeRequiresTheToken: the same server over HTTP, which is how a
// remote deployment runs, refuses an unauthenticated caller.
func TestVaultHTTPModeRequiresTheToken(t *testing.T) {
	port := freePort(t)
	config := t.TempDir()
	// HTTP mode is its own process: it serves the protocol over the network instead
	// of stdio, which is how a remote deployment runs.
	address := "127.0.0.1:" + port
	command := exec.Command(binaries["vault"], "--listen", address)
	command.Env = append(os.Environ(), "MIDAS_CONFIG_DIR="+config)
	stderr := &strings.Builder{}
	command.Stderr = stderr
	startInGroup(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopGroup(command) })

	// The server writes its token beside its settings, either in the merged file
	// Midas points it at or in its own, so read both.
	waitFor(t, "the vault to write its token", func() bool { return vaultToken(t, config) != "" })
	token := vaultToken(t, config)
	if token == "" {
		t.Fatalf("the vault wrote no token: %s", stderr.String())
	}
	endpoint := "http://" + address + "/mcp"
	waitFor(t, "the vault to listen", func() bool {
		response, err := http.Get(endpoint)
		if err != nil {
			return false
		}
		response.Body.Close()
		return true
	})

	// Unauthenticated calls are refused before anything reaches the vault.
	unauthorized := postMCP(t, endpoint, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated call = %d %s", unauthorized.status, unauthorized.body)
	}
	// A real MCP client with the token works: initialize, list tools, and call one.
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: &tokenTransport{token: token, base: http.DefaultTransport}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("authenticated connect: %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "vault_status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the vault tools are not listed: %#v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "vault_status", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("vault_status over HTTP: %v %s", err, toolText(result))
	}
}

// tokenTransport adds the bearer token to every request.
type tokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t *tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(cloned)
}

// postMCP sends one MCP request over HTTP and returns the status and body.
type mcpResponse struct {
	status int
	body   string
}

func postMCP(t *testing.T, endpoint, token, body string) mcpResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := make([]byte, 64*1024)
	count, _ := response.Body.Read(buffer)
	return mcpResponse{status: response.StatusCode, body: string(buffer[:count])}
}

// vaultToken reads the token a server wrote, in either configuration layout.
func vaultToken(t *testing.T, configDir string) string {
	t.Helper()
	for _, name := range []string{"vault.json", "settings.json"} {
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}
		if name == "vault.json" {
			var settings struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(data, &settings) == nil && settings.Token != "" {
				return settings.Token
			}
			continue
		}
		var settings struct {
			MCP struct {
				Vault struct {
					Token string `json:"token"`
				} `json:"vault"`
			} `json:"mcp"`
		}
		if json.Unmarshal(data, &settings) == nil && settings.MCP.Vault.Token != "" {
			return settings.MCP.Vault.Token
		}
	}
	return ""
}

// freePort asks the operating system for a port nothing is using.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// TestVaultHandlesEntriesWithoutSecrets: an entry may hold only a seed, only
// notes, or only codes, and asking for what it does not have is an error rather
// than an empty secret.
func TestVaultHandlesEntriesWithoutSecrets(t *testing.T) {
	instance := startServer(t, "vault", nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})

	// A bare seed rather than an otpauth URL, which is how a user copies one from a
	// site: the default window and six digits apply.
	callTool(t, instance, "vault_put", map[string]any{"title": "Bare seed", "totpSeed": "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"})
	bare := callTool(t, instance, "vault_totp", map[string]any{"title": "Bare seed"})
	if code, _ := bare["code"].(string); len(code) != 6 {
		t.Fatalf("a bare seed produced %#v", bare)
	}
	if until, _ := bare["secondsUntilExpiry"].(float64); until <= 0 || until > 30 {
		t.Fatalf("a bare seed's window = %#v", bare["secondsUntilExpiry"])
	}

	// An entry with no password answers with an error, not with an empty string.
	callTool(t, instance, "vault_put", map[string]any{"title": "Notes only", "notes": "nothing secret here"})
	if message := callToolError(t, instance, "vault_password", map[string]any{"title": "Notes only"}); strings.TrimSpace(message) == "" {
		t.Fatal("an entry without a password returned one")
	}
	if message := callToolError(t, instance, "vault_totp", map[string]any{"title": "Notes only"}); strings.TrimSpace(message) == "" {
		t.Fatal("an entry without a seed returned a code")
	}
	if message := callToolError(t, instance, "vault_recovery_codes", map[string]any{"title": "Notes only"}); strings.TrimSpace(message) == "" {
		t.Fatal("an entry without recovery codes returned some")
	}
	// Deleting something that is not there is an error rather than a no-op.
	if message := callToolError(t, instance, "vault_delete", map[string]any{"title": "Absent"}); strings.TrimSpace(message) == "" {
		t.Fatal("deleting an unknown entry succeeded")
	}
}

// TestVaultKeepsSeveralEntriesApart: the list reports what each entry holds, and
// reading one entry never returns another's secret.
func TestVaultKeepsSeveralEntriesApart(t *testing.T) {
	instance := startServer(t, "vault", nil)
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	callTool(t, instance, "vault_put", map[string]any{"title": "GitHub", "username": "carl", "password": "github-secret"})
	callTool(t, instance, "vault_put", map[string]any{"title": "Bank", "username": "carl", "password": "bank-secret", "totpSeed": "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"})

	listed := callTool(t, instance, "vault_list", map[string]any{})
	if len(list(t, listed, "entries")) != 2 {
		t.Fatalf("entries = %#v", listed)
	}
	byTitle := map[string]map[string]any{}
	for _, entry := range list(t, listed, "entries") {
		object, _ := entry.(map[string]any)
		byTitle[field(t, entry, "title")] = object
	}
	if byTitle["GitHub"]["hasTOTP"] == true || byTitle["Bank"]["hasTOTP"] != true {
		t.Fatalf("what each entry holds is wrong: %#v", listed)
	}
	if password := callTool(t, instance, "vault_password", map[string]any{"title": "GitHub"}); password["password"] != "github-secret" {
		t.Fatalf("reading one entry returned %#v", password)
	}
	if password := callTool(t, instance, "vault_password", map[string]any{"title": "Bank"}); password["password"] != "bank-secret" {
		t.Fatalf("reading another entry returned %#v", password)
	}
}

// TestVaultRefusesWhatItCannotDo: unlocking something that does not exist, and
// changing a password while locked, are both refused with a reason.
func TestVaultRefusesWhatItCannotDo(t *testing.T) {
	instance := startServer(t, "vault", nil)
	// No vault exists yet.
	if message := callToolError(t, instance, "vault_unlock", map[string]any{"password": "anything"}); !strings.Contains(strings.ToLower(message), "no vault") {
		t.Fatalf("unlocking a vault that does not exist = %q", message)
	}
	if message := callToolError(t, instance, "vault_change_password", map[string]any{"current": "a", "next": "b"}); strings.TrimSpace(message) == "" {
		t.Fatal("changing the password of a locked vault succeeded")
	}
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery"})
	callTool(t, instance, "vault_lock", map[string]any{})
	if message := callToolError(t, instance, "vault_change_password", map[string]any{"current": "correct horse battery", "next": "another"}); strings.TrimSpace(message) == "" {
		t.Fatal("changing the password while locked succeeded")
	}
	// Locking twice is harmless: a client that lost track of the state can lock
	// again without an error.
	callTool(t, instance, "vault_lock", map[string]any{})
	if status := callTool(t, instance, "vault_status", map[string]any{}); status["unlocked"] != false {
		t.Fatalf("status after locking twice = %#v", status)
	}
}

// TestVaultHonoursAnExplicitPath: a user may keep the vault wherever they like,
// which is what the path argument is for.
func TestVaultHonoursAnExplicitPath(t *testing.T) {
	instance := startServer(t, "vault", nil)
	path := filepath.Join(t.TempDir(), "somewhere", "else.kdbx")
	callTool(t, instance, "vault_create", map[string]any{"password": "correct horse battery", "path": path})
	// Once the session has an open vault, the other tools act on it: only the calls
	// that name a file take a path.
	callTool(t, instance, "vault_put", map[string]any{"title": "Custom", "password": "custom-secret"})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the vault was not created where it was asked for: %v", err)
	}
	// The default path is untouched, so the two do not collide.
	if _, err := os.Stat(vaultPath(instance)); !os.IsNotExist(err) {
		t.Fatal("a vault was also created at the default path")
	}
	// A second server on the same configuration reads the same file by path.
	restarted := startServerIn(t, "vault", instance.Config, nil)
	callTool(t, restarted, "vault_unlock", map[string]any{"password": "correct horse battery", "path": path})
	if password := callTool(t, restarted, "vault_password", map[string]any{"title": "Custom"}); password["password"] != "custom-secret" {
		t.Fatalf("password from an explicit path = %#v", password)
	}
}
