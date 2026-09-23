package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CarlvinceTan/midas/pkg/vault"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The API is shaped like Polymux Teams on purpose: a client that already speaks
// teams, groups, messages, an agent registry, leases and hosts needs no new
// concepts to drive this environment.

// Group is a conversation between any mix of agents and the user.
type Group struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// Team names the team this conversation belongs to, when it has one.
	Team    string   `json:"team,omitempty"`
	Members []string `json:"members,omitempty"`
	At      int64    `json:"at,omitempty"`
}

// Environment is what the API serves.
type Environment struct {
	Config    Config
	Broker    *Broker
	Registry  *Registry
	Leases    *LeaseRegistry
	Pool      *Pool
	VaultMode string
	// ConfigDir is where the environment writes its own config, so a mode change
	// survives a restart.
	ConfigDir string
	StartedAt time.Time

	// Feed is the change feed every client renders from.
	Feed *Feed
	// Tuning is what the environment applied to bound itself, and TuningReport
	// says what it actually managed to set.
	Tuning       Tuning
	TuningReport TuningReport

	// vaultKey holds the master password once a session has unlocked it, which is
	// what "unlocked" means in user-permission mode.
	mu       sync.Mutex
	vaultKey string
	state    *teamsState

	// options and ctx are kept so an agent created at run time is built exactly
	// like the ones the environment started with, and stops with it.
	options Options
	getenv  func(string) string
	ctx     context.Context
	// appliedAgents is the configuration each running agent was built from, so a
	// reload can tell an unchanged entry from one that needs a new brain.
	appliedAgents map[string]AgentConfig
}

// Handler returns the HTTP surface, with bearer authentication.
func (e *Environment) Handler() http.Handler {
	mux := http.NewServeMux()
	// Agents and groups.
	mux.HandleFunc("GET /v1/agents", e.handleAgents)
	mux.HandleFunc("GET /v1/agents/{address}", e.handleAgent)
	mux.HandleFunc("PUT /v1/agents/{address}/settings", e.handleUpdateAgentSettings)
	// Reload applies an edited server.json without restarting the environment.
	mux.HandleFunc("POST /v1/reload", e.handleReload)
	// Teams and their conversations.
	mux.HandleFunc("GET /v1/teams", e.handleTeams)
	mux.HandleFunc("POST /v1/teams", e.handlePutTeam)
	mux.HandleFunc("DELETE /v1/teams/{team}", e.handleRemoveTeam)
	mux.HandleFunc("GET /v1/groups", e.handleGroups)
	mux.HandleFunc("POST /v1/groups", e.handleCreateGroup)
	mux.HandleFunc("PUT /v1/groups/{group}", e.handleUpdateGroup)
	mux.HandleFunc("DELETE /v1/groups/{group}", e.handleRemoveGroup)
	mux.HandleFunc("GET /v1/groups/{group}/messages", e.handleMessages)
	mux.HandleFunc("POST /v1/groups/{group}/messages", e.handleSend)
	mux.HandleFunc("POST /v1/groups/{group}/mark-read", e.handleMarkRead)
	// Leases and hosts.
	mux.HandleFunc("GET /v1/leases", e.handleLeases)
	mux.HandleFunc("POST /v1/leases", e.handleGrantLease)
	mux.HandleFunc("DELETE /v1/leases/{resource}", e.handleReleaseLease)
	mux.HandleFunc("GET /v1/hosts", e.handleHosts)
	mux.HandleFunc("POST /v1/hosts/pair", e.handleBeginPairing)
	mux.HandleFunc("POST /v1/hosts/pair/complete", e.handleCompletePairing)
	mux.HandleFunc("PUT /v1/hosts/{host}/default", e.handleSetDefaultHost)
	mux.HandleFunc("DELETE /v1/hosts/{host}", e.handleRemoveHost)
	// The shared MCP pool: one connection per server, visible so an operator can
	// see which tools every agent is using.
	mux.HandleFunc("GET /v1/mcps", e.handleMCPs)
	mux.HandleFunc("GET /v1/tuning", e.handleTuning)
	// Vault mode and the change feed.
	mux.HandleFunc("GET /v1/vault/mode", e.handleVaultMode)
	mux.HandleFunc("PUT /v1/vault/mode", e.handleSetVaultMode)
	mux.HandleFunc("POST /v1/vault/unlock", e.handleVaultUnlock)
	mux.HandleFunc("GET /v1/events", e.handleEvents)
	// The same change feed over a WebSocket, with sends accepted on the socket so a
	// desktop client holds one connection instead of two.
	mux.HandleFunc("GET /v1/socket", e.handleSocket)
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	return e.authorize(mux)
}

// authorize requires the environment's token on everything but the health check.
func (e *Environment) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// The health check is open for a load balancer, and the socket authenticates
		// itself because a browser client cannot set headers on a WebSocket
		// handshake and passes ?token= instead.
		if request.URL.Path == "/healthz" || request.URL.Path == "/v1/socket" {
			next.ServeHTTP(writer, request)
			return
		}
		if strings.TrimSpace(request.Header.Get("Authorization")) != "Bearer "+e.token() {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeError(writer, http.StatusUnauthorized, "unauthorized", "a bearer token is required")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (e *Environment) handleAgents(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"agents": e.Registry.Statuses()})
}

func (e *Environment) handleGroups(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"groups": e.state.Groups()})
}

func (e *Environment) handleTeams(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"teams": e.state.Teams()})
}

func (e *Environment) handlePutTeam(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	team, created, err := e.state.PutTeam(body.ID, body.Name, body.Members)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status := http.StatusOK
	action := "updated"
	if created {
		status, action = http.StatusCreated, "created"
	}
	e.publish(Event{Kind: EventTeam, Action: action, Team: &team})
	writeJSON(writer, status, team)
}

func (e *Environment) handleRemoveTeam(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("team")
	if !e.state.RemoveTeam(id) {
		writeError(writer, http.StatusNotFound, "unknown_team", "no such team")
		return
	}
	e.publish(Event{Kind: EventTeam, Action: "removed", Detail: id})
	writeJSON(writer, http.StatusOK, map[string]any{"removed": id})
}

// handleUpdateGroup changes a conversation's title, team, or members without
// touching its transcript.
func (e *Environment) handleUpdateGroup(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Title   string   `json:"title"`
		Team    string   `json:"team"`
		Members []string `json:"members"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	group, _, err := e.state.PutGroup(request.PathValue("group"), body.Title, body.Team, body.Members)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	e.publish(Event{Kind: EventGroup, Action: "updated", Group: &group})
	writeJSON(writer, http.StatusOK, group)
}

func (e *Environment) handleCreateGroup(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Team    string   `json:"team"`
		Members []string `json:"members"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a group needs an id")
		return
	}
	group, created, err := e.state.PutGroup(id, body.Title, body.Team, body.Members)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status, action := http.StatusOK, "updated"
	if created {
		status, action = http.StatusCreated, "created"
	}
	e.publish(Event{Kind: EventGroup, Action: action, Group: &group})
	writeJSON(writer, status, group)
}

func (e *Environment) handleRemoveGroup(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("group")
	if !e.state.RemoveGroup(id) {
		writeError(writer, http.StatusNotFound, "unknown_group", "no such group")
		return
	}
	e.publish(Event{Kind: EventGroup, Action: "removed", Detail: id})
	writeJSON(writer, http.StatusOK, map[string]any{"removed": id})
}

func (e *Environment) handleMessages(writer http.ResponseWriter, request *http.Request) {
	group := request.PathValue("group")
	limit := 50
	if value := request.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			// The client asks for a bounded window, so a huge limit cannot make the
			// server build a huge response.
			limit = min(parsed, maxHistoryLimit)
		}
	}
	messages, err := e.Broker.History(group, limit)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "history_unavailable", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"messages": messages})
}

// handleSend is the user link's write path: a message from the user to a group,
// which reaches whichever agent is addressed.
func (e *Environment) handleSend(writer http.ResponseWriter, request *http.Request) {
	group := request.PathValue("group")
	var body struct {
		Text string `json:"text"`
		To   string `json:"to"`
		From string `json:"from"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a message needs text")
		return
	}
	to := strings.TrimSpace(body.To)
	if to == "" {
		// With no recipient named, the orchestrator takes it, which is what a user
		// talking to "the environment" means.
		to = e.defaultAgent()
	}
	from := strings.TrimSpace(body.From)
	if from == "" {
		from = UserAddress
	}
	envelope, err := e.Broker.Send(from, to, group, text)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "send_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusAccepted, envelope)
}

func (e *Environment) handleMarkRead(writer http.ResponseWriter, request *http.Request) {
	group := request.PathValue("group")
	messages, err := e.Broker.History(group, 1000)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "history_unavailable", err.Error())
		return
	}
	at := int64(0)
	if len(messages) > 0 {
		at = messages[len(messages)-1].At
	}
	writeJSON(writer, http.StatusOK, map[string]any{"group": group, "readAt": at})
}

func (e *Environment) handleLeases(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"leases": e.Leases.Holders()})
}

func (e *Environment) handleGrantLease(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Resource string `json:"resource"`
		Holder   string `json:"holder"`
		Note     string `json:"note"`
		TTL      int    `json:"ttlSeconds"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	lease, err := e.Leases.Grant(body.Resource, body.Holder, body.Note, time.Duration(body.TTL)*time.Second)
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, ErrLeaseHeld) {
			status = http.StatusBadRequest
		}
		writeError(writer, status, "lease_refused", err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, lease)
}

func (e *Environment) handleReleaseLease(writer http.ResponseWriter, request *http.Request) {
	holder := strings.TrimSpace(request.URL.Query().Get("holder"))
	if err := e.Leases.Release(request.PathValue("resource"), holder); err != nil {
		status := http.StatusConflict
		if errors.Is(err, ErrNoLease) {
			status = http.StatusNotFound
		}
		writeError(writer, status, "release_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"released": request.PathValue("resource")})
}

func (e *Environment) handleHosts(writer http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	hosts := append([]HostConfig(nil), e.Config.Hosts...)
	e.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"hosts": hosts})
}

func (e *Environment) handleMCPs(writer http.ResponseWriter, _ *http.Request) {
	statuses := []any{}
	if e.Pool != nil {
		for _, status := range e.Pool.Statuses() {
			statuses = append(statuses, map[string]any{"name": status.Name, "state": string(status.State)})
		}
	}
	report := map[string]any{"servers": statuses, "shared": true}
	if e.Pool != nil {
		report["idleSeconds"] = int(e.Pool.IdleFor().Seconds())
		report["idleStops"] = e.Pool.StoppedCount()
	}
	writeJSON(writer, http.StatusOK, report)
}

// token returns the configured bearer token.
func (e *Environment) token() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Config.Token
}

func (e *Environment) handleTuning(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, e.TuningReport)
}

func (e *Environment) handleVaultMode(writer http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	mode, unlocked := e.VaultMode, e.unlockedLocked()
	e.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"mode": mode, "unlocked": unlocked})
}

// unlockedLocked reports whether the vault is readable right now. Autonomous mode
// is unlocked by definition: there is no user present to ask, which is the point
// of running in this environment.
func (e *Environment) unlockedLocked() bool {
	return e.VaultMode == VaultAutonomous || e.vaultKey != ""
}

// handleSetVaultMode switches between the two modes the environment supports:
// autonomous, where it stays unlocked because no user is present, and
// user-permission, where it locks until the master password arrives.
func (e *Environment) handleSetVaultMode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode != VaultAutonomous && mode != VaultUserPermission {
		writeError(writer, http.StatusBadRequest, "invalid_request", "mode must be autonomous or user-permission")
		return
	}
	var unlocked bool
	if err := e.updateConfig(func(config *Config) {
		config.VaultMode = mode
		// VaultMode, vaultKey, and Config are guarded by the same mutex, so the
		// runtime switch happens with the persisted one.
		e.VaultMode = mode
		if mode == VaultUserPermission {
			// Switching to user permission locks it again: the previous unlock does
			// not carry over a mode change.
			e.vaultKey = ""
		}
		unlocked = e.unlockedLocked()
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	e.publish(Event{Kind: EventVault, Action: mode, Detail: "mode"})
	writeJSON(writer, http.StatusOK, map[string]any{"mode": mode, "unlocked": unlocked})
}

func (e *Environment) handleVaultUnlock(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(body.Password) == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a password is required")
		return
	}
	e.mu.Lock()
	mode, configDir := e.VaultMode, e.ConfigDir
	e.mu.Unlock()
	if mode == VaultAutonomous {
		writeError(writer, http.StatusConflict, "already_unlocked", "autonomous mode keeps the vault unlocked")
		return
	}
	// The password is verified against the database before anything is recorded:
	// storing an unverified password would report a vault as unlocked that no
	// other tool can open.
	directory := strings.TrimSpace(configDir)
	if directory == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			writeError(writer, http.StatusInternalServerError, "vault_unavailable", homeErr.Error())
			return
		}
		directory = filepath.Join(home, ".midas")
	}
	if _, err := vault.Open(vault.DefaultPath(directory), body.Password); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(writer, http.StatusConflict, "vault_missing", "no vault exists yet")
			return
		}
		writeError(writer, http.StatusUnauthorized, "invalid_password", "the password does not unlock the vault")
		return
	}
	e.mu.Lock()
	e.vaultKey = body.Password
	unlocked := e.unlockedLocked()
	e.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"unlocked": unlocked})
}

// handleEvents streams the change feed. A client that knows when it last saw a
// change catches up through ?since=, which is what makes a reconnect cheap.
func (e *Environment) handleEvents(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	flusher, _ := writer.(http.Flusher)

	events, unsubscribe := e.Feed.Subscribe()
	defer unsubscribe()
	if since := request.URL.Query().Get("since"); since != "" {
		if parsed, err := strconv.ParseInt(since, 10, 64); err == nil {
			for _, event := range e.Feed.Since(parsed) {
				if err := writeEvent(writer, event); err != nil {
					return
				}
			}
		}
	}
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeEvent(writer, event); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// writeEvent renders one change as an SSE frame.
func writeEvent(writer http.ResponseWriter, event Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	return err
}

// defaultAgent is who receives a message the user did not address: the first
// orchestrator, else the first agent.
func (e *Environment) defaultAgent() string {
	e.mu.Lock()
	agents := append([]AgentConfig(nil), e.Config.Agents...)
	e.mu.Unlock()
	for _, agent := range agents {
		if strings.EqualFold(agent.Role, "orchestrator") {
			return agent.Address
		}
	}
	if len(agents) > 0 {
		return agents[0].Address
	}
	return ""
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}

// maxJSONBody bounds a request body. The API never needs more than this, so a
// client cannot make the server allocate without limit.
const maxJSONBody = 1 << 20

// maxHistoryLimit is the most messages one page of history may return.
const maxHistoryLimit = 500

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	if request.Body == nil {
		return errors.New("a JSON body is required")
	}
	body := http.MaxBytesReader(writer, request.Body, maxJSONBody)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("body is larger than %d bytes", maxJSONBody)
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}
