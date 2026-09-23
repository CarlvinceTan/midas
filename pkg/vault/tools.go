package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The vault tools share one rule: a secret is returned only from an unlocked
// session, and the tools never invent an entry or a value.

type statusInput struct{}

type statusOutput struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Unlocked bool   `json:"unlocked"`
	Entries  int    `json:"entries,omitempty"`
}

func (s *server) statusTool(_ context.Context, request *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, statusOutput, error) {
	path, err := s.path()
	if err != nil {
		return nil, statusOutput{}, err
	}
	output := statusOutput{Path: path, Exists: Exists(path)}
	session, err := s.session(request)
	if err != nil {
		return nil, statusOutput{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.vault != nil {
		output.Unlocked = true
		output.Entries = len(session.vault.Entries())
	}
	return nil, output, nil
}

type unlockInput struct {
	Password string `json:"password" jsonschema:"the vault password, entered by the user for this session"`
	Path     string `json:"path,omitempty" jsonschema:"vault file to open, default vault.kdbx beside the agent config"`
}

type unlockOutput struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
}

func (s *server) unlockTool(_ context.Context, request *mcp.CallToolRequest, input unlockInput) (*mcp.CallToolResult, unlockOutput, error) {
	if strings.TrimSpace(input.Password) == "" {
		return nil, unlockOutput{}, errors.New("vault: a password is required")
	}
	file := strings.TrimSpace(input.Path)
	if file == "" {
		resolved, err := s.path()
		if err != nil {
			return nil, unlockOutput{}, err
		}
		file = resolved
	}
	if !Exists(file) {
		return nil, unlockOutput{}, fmt.Errorf("vault: no vault at %s; create one first with the user's password", file)
	}
	opened, err := Open(file, input.Password)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session, err := s.session(request)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session.mu.Lock()
	session.vault = opened
	entries := len(opened.Entries())
	session.mu.Unlock()
	return nil, unlockOutput{Path: file, Entries: entries}, nil
}

func (s *server) lockTool(_ context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, unlockOutput, error) {
	session, err := s.session(request)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session.mu.Lock()
	path := ""
	if session.vault != nil {
		path = session.vault.Path()
	}
	session.vault = nil
	session.mu.Unlock()
	return nil, unlockOutput{Path: path}, nil
}

type createInput struct {
	Password string `json:"password" jsonschema:"the password the user chose for the new vault"`
	Path     string `json:"path,omitempty" jsonschema:"where to create it, default vault.kdbx beside the agent config"`
}

func (s *server) createTool(_ context.Context, request *mcp.CallToolRequest, input createInput) (*mcp.CallToolResult, unlockOutput, error) {
	file := strings.TrimSpace(input.Path)
	if file == "" {
		resolved, err := s.path()
		if err != nil {
			return nil, unlockOutput{}, err
		}
		file = resolved
	}
	created, err := Create(file, input.Password)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session, err := s.session(request)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session.mu.Lock()
	session.vault = created
	session.mu.Unlock()
	return nil, unlockOutput{Path: file}, nil
}

// unlocked is the guard every secret-reading tool goes through.
func (s *server) unlocked(request *mcp.CallToolRequest) (*Vault, error) {
	session, err := s.session(request)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.vault == nil {
		return nil, errors.New("vault: locked; ask the user for the password and call vault_unlock first")
	}
	return session.vault, nil
}

type listInput struct{}

type listOutput struct {
	Entries []Entry `json:"entries"`
}

func (s *server) listTool(_ context.Context, request *mcp.CallToolRequest, _ listInput) (*mcp.CallToolResult, listOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, listOutput{}, err
	}
	entries := opened.Entries()
	if entries == nil {
		entries = []Entry{}
	}
	return nil, listOutput{Entries: entries}, nil
}

type titleInput struct {
	Title string `json:"title" jsonschema:"entry title"`
}

type passwordOutput struct {
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
}

func (s *server) passwordTool(_ context.Context, request *mcp.CallToolRequest, input titleInput) (*mcp.CallToolResult, passwordOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, passwordOutput{}, err
	}
	password, err := opened.Password(input.Title)
	if err != nil {
		return nil, passwordOutput{}, err
	}
	entry, _, _ := opened.Find(input.Title)
	return nil, passwordOutput{Title: entry.Title, Username: entry.Username, Password: password}, nil
}

type totpOutput struct {
	Title              string `json:"title"`
	Code               string `json:"code"`
	SecondsUntilExpiry int    `json:"secondsUntilExpiry"`
}

func (s *server) totpTool(_ context.Context, request *mcp.CallToolRequest, input titleInput) (*mcp.CallToolResult, totpOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, totpOutput{}, err
	}
	code, remaining, err := opened.TOTP(input.Title, s.now())
	if err != nil {
		return nil, totpOutput{}, err
	}
	return nil, totpOutput{Title: input.Title, Code: code, SecondsUntilExpiry: int(remaining.Seconds())}, nil
}

type recoveryOutput struct {
	Title string   `json:"title"`
	Codes []string `json:"codes"`
}

func (s *server) recoveryTool(_ context.Context, request *mcp.CallToolRequest, input titleInput) (*mcp.CallToolResult, recoveryOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, recoveryOutput{}, err
	}
	codes, err := opened.RecoveryCodes(input.Title)
	if err != nil {
		return nil, recoveryOutput{}, err
	}
	return nil, recoveryOutput{Title: input.Title, Codes: codes}, nil
}

type putInput struct {
	Title         string   `json:"title" jsonschema:"entry title, created when it does not exist"`
	Username      string   `json:"username,omitempty" jsonschema:"account name"`
	URL           string   `json:"url,omitempty" jsonschema:"site or service"`
	Notes         string   `json:"notes,omitempty" jsonschema:"free-form notes"`
	Password      string   `json:"password,omitempty" jsonschema:"password to store; omitted means leave an existing one alone"`
	TOTPSeed      string   `json:"totpSeed,omitempty" jsonschema:"TOTP secret, either a bare seed or an otpauth URL"`
	RecoveryCodes []string `json:"recoveryCodes,omitempty" jsonschema:"codes to store, replacing any already there"`
}

func (s *server) putTool(_ context.Context, request *mcp.CallToolRequest, input putInput) (*mcp.CallToolResult, Entry, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, Entry{}, err
	}
	options := WriteOptions{
		Title: input.Title, Username: input.Username, URL: input.URL, Notes: input.Notes,
		TOTPSeed: input.TOTPSeed, RecoveryCodes: input.RecoveryCodes,
	}
	// An omitted password must not clear a stored one: only a provided value is
	// written, which is why this is a pointer.
	if input.Password != "" {
		password := input.Password
		options.Password = &password
	}
	entry, err := opened.Put(options)
	if err != nil {
		return nil, Entry{}, err
	}
	return nil, entry, nil
}

type deleteOutput struct {
	Title   string `json:"title"`
	Removed bool   `json:"removed"`
}

func (s *server) deleteTool(_ context.Context, request *mcp.CallToolRequest, input titleInput) (*mcp.CallToolResult, deleteOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, deleteOutput{}, err
	}
	removed, err := opened.Delete(input.Title)
	if err != nil {
		return nil, deleteOutput{}, err
	}
	if !removed {
		return nil, deleteOutput{}, fmt.Errorf("vault: no entry named %q", input.Title)
	}
	return nil, deleteOutput{Title: input.Title, Removed: true}, nil
}

type changePasswordInput struct {
	Current string `json:"current" jsonschema:"the current password, from the user"`
	Next    string `json:"next" jsonschema:"the new password, chosen by the user"`
}

func (s *server) changePasswordTool(_ context.Context, request *mcp.CallToolRequest, input changePasswordInput) (*mcp.CallToolResult, statusOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, statusOutput{}, err
	}
	if err := opened.ChangePassword(input.Current, input.Next); err != nil {
		return nil, statusOutput{}, err
	}
	return nil, statusOutput{Path: opened.Path(), Exists: true, Unlocked: true, Entries: len(opened.Entries())}, nil
}

type destroyInput struct {
	Confirm string `json:"confirm" jsonschema:"the vault path, typed out to confirm deleting the whole vault"`
}

func (s *server) destroyTool(_ context.Context, request *mcp.CallToolRequest, input destroyInput) (*mcp.CallToolResult, unlockOutput, error) {
	opened, err := s.unlocked(request)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	if strings.TrimSpace(input.Confirm) != opened.Path() {
		return nil, unlockOutput{}, fmt.Errorf("vault: confirm must repeat the vault path exactly (%s)", opened.Path())
	}
	if err := opened.Destroy(); err != nil {
		return nil, unlockOutput{}, err
	}
	session, err := s.session(request)
	if err != nil {
		return nil, unlockOutput{}, err
	}
	session.mu.Lock()
	session.vault = nil
	session.mu.Unlock()
	return nil, unlockOutput{Path: input.Confirm}, nil
}
