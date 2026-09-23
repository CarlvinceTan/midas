package vault

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectVault(t *testing.T, path string, now time.Time) (*mcp.ClientSession, *mcp.ClientSession) {
	t.Helper()
	server := NewServer(ServerOptions{Path: path, Now: func() time.Time { return now }})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	firstServer, firstClient := mcp.NewInMemoryTransports()
	secondServer, secondClient := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), firstServer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Connect(context.Background(), secondServer, nil); err != nil {
		t.Fatal(err)
	}
	sessionA, err := client.Connect(context.Background(), firstClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionA.Close() })
	sessionB, err := client.Connect(context.Background(), secondClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionB.Close() })
	return sessionA, sessionB
}

func call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s returned an error: %#v", name, result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func fails(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err == nil && !result.IsError {
		t.Fatalf("%s should have failed", name)
	}
}

// TestVaultIsLockedUntilUnlockedPerSession covers the heart of the server: a
// session can read nothing before it unlocks, and unlocking one session does not
// unlock another.
func TestVaultIsLockedUntilUnlockedPerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	now := time.Unix(1_700_000_000, 0)
	first, second := connectVault(t, path, now)

	status := call(t, first, ToolStatus, nil)
	if status["exists"] != false || status["unlocked"] != false {
		t.Fatalf("fresh status = %#v", status)
	}
	// Reading before unlocking is refused with the action to take.
	fails(t, first, ToolList, nil)
	fails(t, first, ToolPassword, map[string]any{"title": "GitHub"})
	// Creating needs a password and leaves the session unlocked.
	fails(t, first, ToolCreate, map[string]any{"password": "   "})
	call(t, first, ToolCreate, map[string]any{"password": "correct horse"})
	if status := call(t, first, ToolStatus, nil); status["unlocked"] != true || status["exists"] != true {
		t.Fatalf("status after create = %#v", status)
	}

	entry := call(t, first, ToolPut, map[string]any{
		"title": "GitHub", "username": "carl", "password": "hunter2",
		"totpSeed": "JBSWY3DPEHPK3PXP", "recoveryCodes": []string{"aaaa-bbbb"},
	})
	if entry["hasPassword"] != true || entry["hasTOTP"] != true || entry["hasRecoveryCodes"] != true {
		t.Fatalf("entry = %#v", entry)
	}
	password := call(t, first, ToolPassword, map[string]any{"title": "github"})
	if password["password"] != "hunter2" || password["username"] != "carl" {
		t.Fatalf("password = %#v", password)
	}
	code := call(t, first, ToolTOTP, map[string]any{"title": "GitHub"})
	if len(code["code"].(string)) != 6 {
		t.Fatalf("totp = %#v", code)
	}
	// The listing never carries a secret.
	listed := call(t, first, ToolList, nil)
	if strings.Contains(toString(listed), "hunter2") || strings.Contains(toString(listed), "JBSWY3DPEHPK3PXP") {
		t.Fatalf("listing leaked a secret: %s", toString(listed))
	}
	codes := call(t, first, ToolRecoveryCodes, map[string]any{"title": "GitHub"})
	if len(codes["codes"].([]any)) != 1 {
		t.Fatalf("recovery = %#v", codes)
	}

	// The second session is still locked, and unlocks with the same password.
	if status := call(t, second, ToolStatus, nil); status["unlocked"] != false {
		t.Fatalf("second session status = %#v", status)
	}
	fails(t, second, ToolList, nil)
	fails(t, second, ToolUnlock, map[string]any{"password": "wrong"})
	call(t, second, ToolUnlock, map[string]any{"password": "correct horse"})
	if entries := call(t, second, ToolList, nil); len(entries["entries"].([]any)) != 1 {
		t.Fatalf("second session entries = %#v", entries)
	}

	// Locking forgets the vault without touching the file.
	call(t, first, ToolLock, nil)
	fails(t, first, ToolList, nil)
	if status := call(t, second, ToolStatus, nil); status["unlocked"] != true {
		t.Fatalf("locking one session affected another: %#v", status)
	}
}

func TestVaultDestructiveToolsNeedExplicitConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.kdbx")
	session, _ := connectVault(t, path, time.Unix(1_700_000_000, 0))
	call(t, session, ToolCreate, map[string]any{"password": "pw"})
	call(t, session, ToolPut, map[string]any{"title": "Mail", "password": "s3cret"})

	// A delete keeps the rest, and destroying needs the path typed out.
	removed := call(t, session, ToolDelete, map[string]any{"title": "Mail"})
	if removed["removed"] != true {
		t.Fatalf("delete = %#v", removed)
	}
	fails(t, session, ToolDelete, map[string]any{"title": "Mail"})
	call(t, session, ToolPut, map[string]any{"title": "Mail", "password": "s3cret"})
	fails(t, session, ToolDestroy, map[string]any{"confirm": "yes"})
	call(t, session, ToolDestroy, map[string]any{"confirm": path})
	if status := call(t, session, ToolStatus, nil); status["exists"] != false {
		t.Fatalf("status after destroy = %#v", status)
	}
	// Changing a password needs the current one, and the old one stops working.
	call(t, session, ToolCreate, map[string]any{"password": "first"})
	fails(t, session, ToolChangePass, map[string]any{"current": "nope", "next": "second"})
	call(t, session, ToolChangePass, map[string]any{"current": "first", "next": "second"})
	call(t, session, ToolLock, nil)
	fails(t, session, ToolUnlock, map[string]any{"password": "first"})
	call(t, session, ToolUnlock, map[string]any{"password": "second"})
}

func toString(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
