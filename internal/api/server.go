// Package api serves the Midas agent over HTTP using the contract defined by
// pkg/protocol. It owns the run registry, request decoding, and the event stream;
// the agent itself stays in pkg/agent and internal/chat, and every record it emits
// comes from protocol.FromAgentEvent.
//
// The route table is the contract: Routes() is checked against
// protocol.ControlRoutes() by test, so a handler cannot exist without being
// documented or be documented without being served.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/chat"
	"github.com/CarlvinceTan/midas/internal/storage"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/protocol"
)

// Config is everything the server needs to build a run. It carries no provider
// credentials of its own: ResolveModel supplies a runtime for each request.
type Config struct {
	// Root is the default working directory for runs.
	Root string
	// SessionID names the session a run without one of its own appends to.
	SessionID string
	// Provider, Model, and APIKey are the default runtime.
	Provider ai.Streamer
	Model    ai.Model
	APIKey   string
	// StreamOptions are the defaults for every run.
	StreamOptions ai.StreamOptions
	// CodingTools selects the tool surface handed to the agent.
	CodingTools chat.CodingToolsMode
	// AdditionalTools are extra tools merged into every run's toolset.
	AdditionalTools []agent.Tool
	// Compaction governs context summarisation.
	Compaction agent.CompactionSettings
	// Sessions persists transcripts; optional.
	Sessions *storage.Store
	// ResolveModel overrides the runtime for one request. Returning an empty
	// provider keeps the default.
	ResolveModel func(protocol.RunRequest) (ai.Streamer, ai.Model, string, error)
	// RetainFinished is how long a finished run keeps answering. Zero uses
	// DefaultRetainFinished.
	RetainFinished time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// DefaultRetainFinished is how long finished runs stay addressable.
const DefaultRetainFinished = 15 * time.Minute

// Server serves the agent API. It is safe for concurrent use.
type Server struct {
	config Config
	now    func() time.Time

	mu   sync.Mutex
	runs map[string]*run
	// nextRunID disambiguates run IDs when the clock is coarse or injected, so
	// two runs started in the same instant cannot share an ID.
	nextRunID uint64
}

// NewServer validates the configuration and returns a server.
func NewServer(config Config) (*Server, error) {
	if config.Provider == nil {
		return nil, errors.New("api: a default provider is required")
	}
	if config.Root == "" {
		config.Root = "."
	}
	if config.RetainFinished <= 0 {
		config.RetainFinished = DefaultRetainFinished
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Server{config: config, now: config.Now, runs: make(map[string]*run)}, nil
}

// Routes is every route this server serves, in the shape the contract test
// compares against protocol.ControlRoutes.
func Routes() []protocol.Route {
	return []protocol.Route{
		{Method: "GET", Path: "/v1/health", OperationID: "getHealth"},
		{Method: "POST", Path: "/v1/runs", OperationID: "startRun"},
		{Method: "GET", Path: "/v1/runs/{runID}", OperationID: "getRun"},
		{Method: "POST", Path: "/v1/runs/{runID}/steer", OperationID: "steerRun"},
		{Method: "POST", Path: "/v1/runs/{runID}/abort", OperationID: "abortRun"},
		{Method: "GET", Path: "/v1/runs/{runID}/events", OperationID: "streamRunEvents"},
	}
}

// Handler returns the HTTP handler for the whole API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("POST /v1/runs", s.handleStartRun)
	mux.HandleFunc("GET /v1/runs/{runID}", s.handleGetRun)
	mux.HandleFunc("POST /v1/runs/{runID}/steer", s.handleSteer)
	mux.HandleFunc("POST /v1/runs/{runID}/abort", s.handleAbort)
	mux.HandleFunc("GET /v1/runs/{runID}/events", s.handleEvents)
	return mux
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": protocol.Version})
}

func (s *Server) handleStartRun(writer http.ResponseWriter, request *http.Request) {
	var body protocol.RunRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := body.Validate(); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	started, err := s.startRun(body)
	switch {
	case errors.Is(err, errRunInFlight):
		writeError(writer, http.StatusConflict, "run_in_flight", err.Error())
	case err != nil:
		writeError(writer, http.StatusBadRequest, "run_rejected", err.Error())
	default:
		writeJSON(writer, http.StatusAccepted, started.info())
	}
}

func (s *Server) handleGetRun(writer http.ResponseWriter, request *http.Request) {
	started, ok := s.lookup(request.PathValue("runID"))
	if !ok {
		writeError(writer, http.StatusNotFound, "unknown_run", "no such run on this server")
		return
	}
	writeJSON(writer, http.StatusOK, started.info())
}

func (s *Server) handleSteer(writer http.ResponseWriter, request *http.Request) {
	started, ok := s.lookup(request.PathValue("runID"))
	if !ok {
		writeError(writer, http.StatusNotFound, "unknown_run", "no such run on this server")
		return
	}
	var body protocol.SteerRequest
	if err := decodeJSON(request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := body.Validate(); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := started.chat.Steer(body.Text); err != nil {
		writeError(writer, http.StatusConflict, "run_not_steerable", err.Error())
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAbort(writer http.ResponseWriter, request *http.Request) {
	started, ok := s.lookup(request.PathValue("runID"))
	if !ok {
		writeError(writer, http.StatusNotFound, "unknown_run", "no such run on this server")
		return
	}
	reason := strings.TrimSpace(request.URL.Query().Get("reason"))
	started.chat.Abort(errors.New("aborted by client: " + defaultString(reason, "no reason given")))
	writer.WriteHeader(http.StatusAccepted)
}

// startRun builds the session, registers it, and pumps the agent in the background.
func (s *Server) startRun(body protocol.RunRequest) (*run, error) {
	provider, model, apiKey := s.config.Provider, s.config.Model, s.config.APIKey
	if s.config.ResolveModel != nil {
		resolved, resolvedModel, resolvedKey, err := s.config.ResolveModel(body)
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			provider, model, apiKey = resolved, resolvedModel, resolvedKey
		}
	}
	root := strings.TrimSpace(body.CWD)
	if root == "" {
		root = s.config.Root
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		sessionID = s.config.SessionID
	}
	compaction := s.config.Compaction
	if body.NoCompact {
		compaction.Enabled = false
	}
	// The session is checked for an in-flight run before a provider request is
	// made, so a duplicate start is rejected without paying for a second attempt.
	// The check after the insert below still covers a race between the two.
	s.mu.Lock()
	busy := inFlight(s.runs, sessionID)
	s.mu.Unlock()
	if busy {
		return nil, errRunInFlight
	}
	// No AgentRuntime is installed, which is what keeps a run from delegating: an
	// API run has no subagent tool. Agents in an environment grow the team instead,
	// by creating another agent (see internal/server/create.go).
	session, err := chat.New(chat.Options{
		Root: root, SessionID: sessionID, Provider: provider, Model: model,
		StreamOptions: ai.StreamOptions{APIKey: apiKey, MaxTokens: s.config.StreamOptions.MaxTokens,
			CacheRetention: s.config.StreamOptions.CacheRetention, MaxRetries: s.config.StreamOptions.MaxRetries,
			MaxRetryDelay: s.config.StreamOptions.MaxRetryDelay, SessionID: sessionID},
		CodingTools: s.config.CodingTools, AdditionalTools: s.config.AdditionalTools,
		Compaction: compaction, Sessions: s.config.Sessions,
	})
	if err != nil {
		return nil, err
	}
	stream, err := session.Prompt(requestContext(), body.Prompt)
	if err != nil {
		return nil, err
	}

	s.reap()
	s.mu.Lock()
	s.nextRunID++
	runID := fmt.Sprintf("run_%d_%d", s.now().UnixNano(), s.nextRunID)
	s.mu.Unlock()
	started := &run{
		id:        runID,
		sessionID: sessionID,
		chat:      session,
		model:     model.Provider + "/" + model.ID,
		startedAt: s.now(),
		now:       s.now,
		status:    protocol.RunRunning,
		changed:   make(chan struct{}),
	}
	// Registering the new run and checking for a competitor happen together, so two
	// starts cannot both believe they won.
	s.mu.Lock()
	running := inFlight(s.runs, sessionID)
	if !running {
		s.runs[started.id] = started
	}
	s.mu.Unlock()
	if running {
		stream.Cancel(errRunInFlight)
		return nil, errRunInFlight
	}

	started.append(protocol.RunStarted(started.id, sessionID, started.model))
	go s.pump(started, stream)
	return started, nil
}

// inFlight reports whether a running run already owns a session. The caller holds
// s.mu.
func inFlight(runs map[string]*run, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	for _, other := range runs {
		if other.sessionID == sessionID && other.status == protocol.RunRunning {
			return true
		}
	}
	return false
}

// pump forwards the agent's events into the run's record buffer.
func (s *Server) pump(started *run, stream *chat.Run) {
	ctx := requestContext()
	for {
		event, ok, err := stream.Next(ctx)
		if err != nil {
			started.finish(protocol.RunFailed, err.Error())
			return
		}
		if !ok {
			break
		}
		record, translated := protocol.FromAgentEvent(event)
		if !translated {
			continue
		}
		record.RunID = started.id
		if record.Type == protocol.EventUsage && record.Usage != nil {
			started.addUsage(*record.Usage)
		}
		started.append(record)
	}
	produced, err := stream.Result(ctx)
	status, failure := protocol.RunDone, ""
	if err != nil {
		status, failure = protocol.RunFailed, err.Error()
	}
	// A cancelled or failed provider ends the stream without an error here, so
	// the run's final assistant message decides the status.
	if final, ok := finalAssistant(produced); ok {
		switch final.StopReason {
		case ai.StopAborted:
			status, failure = protocol.RunAborted, reasonOr(final.ErrorMessage, "the run was aborted")
		case ai.StopError:
			status, failure = protocol.RunFailed, reasonOr(final.ErrorMessage, "the run failed")
		}
	}
	started.finish(status, failure)
}

// finalAssistant returns the last assistant message a run produced.
func finalAssistant(messages []ai.Message) (ai.AssistantMessage, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		switch message := messages[index].(type) {
		case ai.AssistantMessage:
			return message, true
		case *ai.AssistantMessage:
			if message != nil {
				return *message, true
			}
		}
	}
	return ai.AssistantMessage{}, false
}

func reasonOr(reason, fallback string) string {
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return trimmed
	}
	return fallback
}

func (s *Server) lookup(id string) (*run, bool) {
	s.reap()
	s.mu.Lock()
	defer s.mu.Unlock()
	started, ok := s.runs[id]
	return started, ok
}

// reap drops finished runs that have been answerable for long enough. It runs on
// every start and on every read, so a server that only answers questions about
// finished runs still forgets them.
func (s *Server) reap() {
	cutoff := s.now().Add(-s.config.RetainFinished)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, started := range s.runs {
		started.mu.Lock()
		finishedAt := started.finishedAt
		status := started.status
		started.mu.Unlock()
		if status != protocol.RunRunning && !finishedAt.IsZero() && finishedAt.Before(cutoff) {
			delete(s.runs, id)
		}
	}
}

var errRunInFlight = errors.New("a run is already in flight for this session")

// requestContext is the base context for agent work. Runs outlive the request
// that started them, so it is deliberately not the request's context.
func requestContext() context.Context { return context.Background() }

func decodeJSON(request *http.Request, target any) error {
	if request.Body == nil {
		return errors.New("a JSON body is required")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, protocol.Error{Code: code, Message: message})
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
