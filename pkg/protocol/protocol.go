// Package protocol is the wire contract for driving a Midas agent from outside
// the process: the records a run emits, the requests that start, steer, and stop
// one, and the route table those requests live on.
//
// It is deliberately transport-free. Nothing here imports net/http, so the same
// definitions serve the stdout stream, an HTTP handler, or an agent talking to
// another agent. pkg/agent stays unaware of all of it: cmd/server and any future
// server adapter translate core events into these records.
//
// api/openapi.yaml and api/events.schema.json describe this package for callers
// that are not written in Go. protocol_test.go fails when the three disagree.
package protocol

import (
	"errors"
	"strings"
	"time"
)

// Version is the contract version. It is carried in the envelope of every
// record so a consumer can reject a stream it does not understand.
const Version = "1"

// Envelope is the shared header of every record and response.
type Envelope struct {
	Version string `json:"v"`
}

// NewEnvelope returns the header every record embeds.
func NewEnvelope() Envelope { return Envelope{Version: Version} }

// RunRequest starts one run. Prompt is the only required field; everything else
// falls back to the server's own configuration.
type RunRequest struct {
	SessionID string `json:"sessionID,omitempty"`
	Prompt    string `json:"prompt"`
	// CWD pins the working directory for this run, overriding the server's own.
	CWD string `json:"cwd,omitempty"`
	// Model overrides the model for this run, as provider/model.
	Model string `json:"model,omitempty"`
	// ReadOnly exposes only the inspection tools.
	ReadOnly bool `json:"readOnly,omitempty"`
	// NoCompact disables summarising older context for this run.
	NoCompact bool `json:"noCompact,omitempty"`
}

// Validate reports whether the request can be served.
func (r RunRequest) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("prompt is required")
	}
	return nil
}

// SteerRequest admits a message into a run that is already going.
type SteerRequest struct {
	Text string `json:"text"`
}

// Validate reports whether the request can be served.
func (r SteerRequest) Validate() error {
	if strings.TrimSpace(r.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}

// AbortRequest stops a run. Reason is optional and only used for reporting.
type AbortRequest struct {
	Reason string `json:"reason,omitempty"`
}

// RunStatus is where a run is in its lifecycle.
type RunStatus string

const (
	RunRunning RunStatus = "running"
	RunDone    RunStatus = "finished"
	RunFailed  RunStatus = "failed"
	RunAborted RunStatus = "aborted"
)

// RunInfo describes a run without its events.
type RunInfo struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionID,omitempty"`
	Status    RunStatus `json:"status"`
	Model     string    `json:"model,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	// FinishedAt is absent while the run is still going.
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Error      string    `json:"error,omitempty"`
}

// Error is the body of a failed control request.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error implements error so handlers can return it directly.
func (e Error) Error() string { return e.Message }

// Route is one control-plane endpoint. The set is the contract: the OpenAPI
// document and the server's mux are both checked against it.
type Route struct {
	Method      string
	Path        string
	OperationID string
}

// ControlRoutes is every endpoint a Midas server exposes. The event stream is
// the only one that stays open; the rest answer once.
func ControlRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/v1/health", OperationID: "getHealth"},
		{Method: "POST", Path: "/v1/runs", OperationID: "startRun"},
		{Method: "GET", Path: "/v1/runs/{runID}", OperationID: "getRun"},
		{Method: "POST", Path: "/v1/runs/{runID}/steer", OperationID: "steerRun"},
		{Method: "POST", Path: "/v1/runs/{runID}/abort", OperationID: "abortRun"},
		{Method: "GET", Path: "/v1/runs/{runID}/events", OperationID: "streamRunEvents"},
	}
}
