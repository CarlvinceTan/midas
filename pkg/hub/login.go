package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Login is one in-flight account login. Bridges drive it: they emit challenge
// events, the hub keeps the latest, and a client renders it and answers.
type Login struct {
	ID        string    `json:"id"`
	Bridge    string    `json:"bridge"`
	Account   string    `json:"account"`
	Status    string    `json:"status"`
	Challenge Challenge `json:"challenge,omitempty"`
	Hint      string    `json:"hint,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Rendered holds ready-to-print QR rows, filled only when a client asks for
	// them so the payload never rides along unrequested.
	Rendered []string `json:"rendered,omitempty"`
	// Session records which MCP session owns the login, so a QR payload is only
	// readable by the client that started it.
	Session string `json:"-"`
}

// loginStatuses are the states a login moves through.
const (
	LoginPending      = "pending"
	LoginScanned      = "scanned"
	LoginAwaitingCode = "awaiting_code"
	LoginConnected    = "connected"
	LoginFailed       = "failed"
	LoginExpired      = "expired"
)

// loginManager tracks in-flight logins.
type loginManager struct {
	mu     sync.Mutex
	logins map[string]*Login
	next   int
	emit   func(Login)
}

func newLoginManager(emit func(Login)) *loginManager {
	return &loginManager{logins: map[string]*Login{}, emit: emit}
}

// start begins a login and asks the bridge for its first challenge.
func (m *loginManager) start(ctx context.Context, session, bridge, account string, call func(context.Context, string, map[string]any, any) error) (Login, error) {
	m.mu.Lock()
	m.next++
	loginID := fmt.Sprintf("login_%d", m.next)
	login := &Login{
		ID: loginID, Bridge: bridge, Account: account,
		Status: LoginPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Session: session,
	}
	m.logins[login.ID] = login
	m.mu.Unlock()

	// A bridge may answer the call with its first challenge, emit one as an
	// event, or both; either path lands in the same place.
	var challenge Challenge
	callErr := call(ctx, MethodLogin, map[string]any{"account": account}, &challenge)
	m.mu.Lock()
	login, ok := m.logins[loginID]
	if !ok {
		m.mu.Unlock()
		return Login{}, errors.New("hub: the login was cancelled")
	}
	if callErr != nil {
		login.Status = LoginFailed
		login.Hint = callErr.Error()
	}
	if challenge.Kind != "" {
		login.Challenge = challenge
		login.Hint = challenge.Hint
		if challenge.Kind == "code" {
			login.Status = LoginAwaitingCode
		}
	}
	login.UpdatedAt = time.Now()
	snapshot := *login
	m.mu.Unlock()
	m.notify(snapshot)
	return snapshot, nil
}

// apply folds a challenge event into the login it belongs to.
func (m *loginManager) apply(bridge, account string, challenge Challenge) (Login, bool) {
	m.mu.Lock()
	var target *Login
	for _, login := range m.logins {
		if login.Bridge != bridge || login.Account != account || login.Status == LoginConnected {
			continue
		}
		// Several logins can be pending for one account; the newest is the one a
		// challenge belongs to.
		if target == nil || login.CreatedAt.After(target.CreatedAt) {
			target = login
		}
	}
	if target == nil {
		m.mu.Unlock()
		return Login{}, false
	}
	target.Challenge = challenge
	target.Hint = challenge.Hint
	target.UpdatedAt = time.Now()
	if challenge.Kind == "code" {
		target.Status = LoginAwaitingCode
	} else if target.Status == "" {
		target.Status = LoginPending
	}
	snapshot := *target
	m.mu.Unlock()
	m.notify(snapshot)
	return snapshot, true
}

// status returns a login, expiring it when its challenge has lapsed.
func (m *loginManager) status(id string) (Login, bool) {
	m.mu.Lock()
	login, ok := m.logins[id]
	if !ok {
		m.mu.Unlock()
		return Login{}, false
	}
	if login.Challenge.ExpiresAt > 0 && time.Now().UnixMilli() > login.Challenge.ExpiresAt && login.Status != LoginConnected {
		login.Status = LoginExpired
	}
	snapshot := *login
	m.mu.Unlock()
	return snapshot, true
}

// answer records a user's reply to a challenge.
func (m *loginManager) answer(id, response string) (Login, error) {
	m.mu.Lock()
	login, ok := m.logins[id]
	if !ok {
		m.mu.Unlock()
		return Login{}, errors.New("no such login")
	}
	// A bridge may report the outcome while the answer is being submitted, so a
	// status it already settled is not overwritten with "pending" again.
	if login.Status != LoginConnected && login.Status != LoginFailed {
		login.Status = LoginPending
	}
	login.UpdatedAt = time.Now()
	snapshot := *login
	m.mu.Unlock()
	m.notify(snapshot)
	_ = response
	return snapshot, nil
}

// complete marks a login connected once the bridge reports success.
func (m *loginManager) complete(bridge, account, status, reason string) {
	m.mu.Lock()
	for _, login := range m.logins {
		if login.Bridge == bridge && login.Account == account {
			login.Status = status
			login.Hint = reason
			login.UpdatedAt = time.Now()
		}
	}
	m.mu.Unlock()
}

// cancel drops a login.
func (m *loginManager) cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.logins[id]; !ok {
		return false
	}
	delete(m.logins, id)
	return true
}

// list returns every tracked login, newest first.
func (m *loginManager) list() []Login {
	m.mu.Lock()
	defer m.mu.Unlock()
	logins := make([]Login, 0, len(m.logins))
	for _, login := range m.logins {
		logins = append(logins, *login)
	}
	slices.SortStableFunc(logins, func(a, b Login) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return logins
}

// listOwned returns the logins belonging to one session, newest first. A login
// carries a challenge payload that links a device, so it is only listed for the
// session that started it.
func (m *loginManager) listOwned(session string) []Login {
	if session == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	logins := make([]Login, 0, len(m.logins))
	for _, login := range m.logins {
		if login.Session == session {
			logins = append(logins, *login)
		}
	}
	slices.SortStableFunc(logins, func(a, b Login) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return logins
}

// ownedBy reports whether a session may read a login's challenge payload. A
// login whose owner is unknown belongs to nobody: a QR payload links a device,
// so failing closed is the only safe default.
func (m *loginManager) ownedBy(id, session string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	login, ok := m.logins[id]
	if !ok {
		return false
	}
	return login.Session != "" && login.Session == session
}

func (m *loginManager) notify(login Login) {
	if m.emit != nil {
		m.emit(login)
	}
}
