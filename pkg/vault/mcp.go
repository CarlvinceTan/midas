package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names, stable so a client's request prefix does not change under it.
const (
	ToolStatus        = "vault_status"
	ToolUnlock        = "vault_unlock"
	ToolLock          = "vault_lock"
	ToolCreate        = "vault_create"
	ToolList          = "vault_list"
	ToolPassword      = "vault_password"
	ToolTOTP          = "vault_totp"
	ToolRecoveryCodes = "vault_recovery_codes"
	ToolPut           = "vault_put"
	ToolDelete        = "vault_delete"
	ToolChangePass    = "vault_change_password"
	ToolDestroy       = "vault_destroy"
)

// Session is one client's unlocked vault. Unlocking is per session on purpose: a
// second agent must not inherit a vault someone else opened, and a password never
// leaves the process it was typed into.
type Session struct {
	mu    sync.Mutex
	vault *Vault
}

// ServerOptions configure the vault server.
type ServerOptions struct {
	// Path is the database file. Defaults to vault.kdbx in the config directory.
	Path string
	// Now is injectable for tests.
	Now func() time.Time
}

type server struct {
	options ServerOptions
	mu      sync.Mutex
	// sessions holds each MCP session's unlocked vault.
	sessions map[string]*Session
	// next is used when a transport hands out no session ID.
	keys    map[*mcp.ServerSession]string
	nextKey int
}

// NewServer builds the vault MCP server.
func NewServer(options ServerOptions) *mcp.Server {
	instance := &server{options: options, sessions: map[string]*Session{}, keys: map[*mcp.ServerSession]string{}}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "vault", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "A KeePass-compatible store for account passwords, TOTP seeds, and recovery codes. The user unlocks it once per session with their password; until then every tool reports that the vault is locked.",
	})
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolStatus,
		Description: "Report where the vault is, whether it exists, and whether this session has unlocked it.",
	}, instance.statusTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolUnlock,
		Description: "Unlock the vault for this session with the user's password. The password is held in memory only and never written anywhere.",
	}, instance.unlockTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolLock,
		Description: "Forget the unlocked vault in this session.",
	}, instance.lockTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolCreate,
		Description: "Create a new empty vault. Refuses when a database already exists, so this can never destroy one; the user chooses the password.",
	}, instance.createTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolList,
		Description: "List stored entries with what each one holds (password, TOTP, recovery codes). Never returns a secret.",
	}, instance.listTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolPassword,
		Description: "Return a stored password. The user asked for this work, so use it for that request only and never echo it into logs or a report.",
	}, instance.passwordTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolTOTP,
		Description: "Return the current one-time code for an entry, and how long it stays valid. Wait for a fresh code rather than sending one that is about to expire.",
	}, instance.totpTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolRecoveryCodes,
		Description: "Return the stored recovery codes for an entry.",
	}, instance.recoveryTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolPut,
		Description: "Create or update an entry. Only the fields provided are changed, so adding a TOTP seed does not clear a password. Ask the user before storing a secret you did not generate.",
	}, instance.putTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolDelete,
		Description: "Delete one entry. Ask the user first: this is not reversible.",
	}, instance.deleteTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolChangePass,
		Description: "Re-encrypt the whole database with a new password. The current password is required, and the old one stops working immediately.",
	}, instance.changePasswordTool)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        ToolDestroy,
		Description: "Delete the vault file itself. Only on an explicit instruction naming the vault, because this cannot be undone.",
	}, instance.destroyTool)
	return mcpServer
}

// sessionKey is a stable identity for an MCP session, so unlocking is per client
// even when a transport hands out no ID.
func (s *server) sessionKey(session *mcp.ServerSession) string {
	if session == nil {
		return ""
	}
	if id := strings.TrimSpace(session.ID()); id != "" {
		return id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.keys[session]; ok {
		return key
	}
	s.nextKey++
	key := fmt.Sprintf("session_%d", s.nextKey)
	s.keys[session] = key
	return key
}

func (s *server) path() (string, error) {
	if strings.TrimSpace(s.options.Path) != "" {
		return s.options.Path, nil
	}
	directory := strings.TrimSpace(os.Getenv("MIDAS_CONFIG_DIR"))
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(home, ".midas")
	}
	return DefaultPath(directory), nil
}

// session returns the unlocked vault for a client, or an explanation.
func (s *server) session(request *mcp.CallToolRequest) (*Session, error) {
	key := s.sessionKey(requestSession(request))
	if key == "" {
		return nil, errors.New("vault: this transport provides no session identity, so the vault cannot be unlocked safely")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[key]; ok {
		return existing, nil
	}
	created := &Session{}
	s.sessions[key] = created
	return created, nil
}

func requestSession(request *mcp.CallToolRequest) *mcp.ServerSession {
	if request == nil {
		return nil
	}
	return request.Session
}

func (s *server) now() time.Time {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}
