package server

import (
	"net/http"
	"strings"
)

// publish records a change on the feed so clients see it without polling.
func (e *Environment) publish(event Event) {
	if e.Feed != nil {
		e.Feed.Publish(event)
	}
}

// handleAgent serves one agent's registry entry.
func (e *Environment) handleAgent(writer http.ResponseWriter, request *http.Request) {
	address := request.PathValue("address")
	for _, status := range e.Registry.Statuses() {
		if status.Address == address {
			writeJSON(writer, http.StatusOK, status)
			return
		}
	}
	writeError(writer, http.StatusNotFound, "unknown_agent", "no such agent")
}

// handleUpdateAgentSettings changes an agent's model or prompt and persists it, so
// an operator can retune one agent without editing the file by hand.
func (e *Environment) handleUpdateAgentSettings(writer http.ResponseWriter, request *http.Request) {
	address := strings.TrimSpace(request.PathValue("address"))
	var body struct {
		Model  *string `json:"model"`
		Prompt *string `json:"prompt"`
		Role   *string `json:"role"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	e.mu.Lock()
	settings := AgentConfig{}
	found := false
	for _, agent := range e.Config.Agents {
		if strings.EqualFold(agent.Address, address) {
			settings, found = agent, true
			break
		}
	}
	e.mu.Unlock()
	if !found {
		writeError(writer, http.StatusNotFound, "unknown_agent", "no such agent")
		return
	}
	updated := settings
	if body.Model != nil {
		updated.Model = strings.TrimSpace(*body.Model)
	}
	if body.Prompt != nil {
		updated.Prompt = *body.Prompt
	}
	if body.Role != nil {
		updated.Role = strings.TrimSpace(*body.Role)
	}
	if updated == settings {
		writeJSON(writer, http.StatusOK, settings)
		return
	}
	// The new settings are built before they are written: a model this machine
	// cannot resolve must not end up in the file, where the next reload would fail
	// on it.
	if _, err := e.brainFor(updated, e.options, e.getenv); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_agent", err.Error())
		return
	}
	if err := e.updateConfig(func(config *Config) {
		for index, agent := range config.Agents {
			if strings.EqualFold(agent.Address, address) {
				config.Agents[index] = updated
				return
			}
		}
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	// Persisting is not applying: the running agent keeps its old brain until it is
	// swapped, which happens between turns.
	if _, err := e.applyAgent(updated); err != nil {
		writeError(writer, http.StatusInternalServerError, "apply_failed", err.Error())
		return
	}
	e.publish(Event{Kind: EventAgent, Action: "settings", Detail: updated.Address})
	writeJSON(writer, http.StatusOK, updated)
}

// handleReload re-reads the environment's configuration and applies it to the
// running agents, so an operator who edits server.json does not have to restart.
func (e *Environment) handleReload(writer http.ResponseWriter, request *http.Request) {
	report, err := e.Reload(request.Context())
	if err != nil {
		// Nothing was applied: the file could not be read or is not usable as a whole.
		writeError(writer, http.StatusBadRequest, "reload_failed", err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

// handleBeginPairing mints a code for a device the operator is adding.
func (e *Environment) handleBeginPairing(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	code, err := e.state.BeginPairing(body.Name)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// The code is returned to the caller that started the handshake and never
	// published on the change feed: it is a credential for two minutes.
	writeJSON(writer, http.StatusCreated, map[string]any{"code": code.code, "name": code.name, "expiresAt": code.expiresAt.UnixMilli()})
}

// handleCompletePairing registers the device that presents a live code.
func (e *Environment) handleCompletePairing(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	host, err := e.state.CompletePairing(body.Code)
	if err != nil {
		writeError(writer, http.StatusConflict, "pairing_failed", err.Error())
		return
	}
	e.persistHosts()
	e.publish(Event{Kind: EventHost, Action: "paired", Host: &host})
	writeJSON(writer, http.StatusOK, host)
}

func (e *Environment) handleSetDefaultHost(writer http.ResponseWriter, request *http.Request) {
	host, err := e.state.SetDefaultHost(request.PathValue("host"))
	if err != nil {
		writeError(writer, http.StatusNotFound, "unknown_host", err.Error())
		return
	}
	e.persistHosts()
	e.publish(Event{Kind: EventHost, Action: "default", Host: &host})
	writeJSON(writer, http.StatusOK, host)
}

func (e *Environment) handleRemoveHost(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("host")
	if err := e.state.RemoveHost(name); err != nil {
		writeError(writer, http.StatusConflict, "remove_failed", err.Error())
		return
	}
	e.persistHosts()
	e.publish(Event{Kind: EventHost, Action: "removed", Detail: name})
	writeJSON(writer, http.StatusOK, map[string]any{"removed": name})
}

// updateConfig applies mutate and persists the result, both under the mutex, so
// concurrent handlers cannot lose each other's configuration writes or marshal a
// struct another goroutine is mutating.
func (e *Environment) updateConfig(mutate func(*Config)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	mutate(&e.Config)
	if strings.TrimSpace(e.ConfigDir) == "" {
		return nil
	}
	return Save(e.ConfigDir, e.Config)
}

// persistHosts writes the host list back to configuration.
func (e *Environment) persistHosts() {
	_ = e.updateConfig(func(config *Config) { config.Hosts = e.state.configHosts() })
}
