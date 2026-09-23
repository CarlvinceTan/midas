package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/chat"
	"github.com/CarlvinceTan/midas/pkg/protocol"
)

// run is one in-flight or recently finished agent run. Records are buffered so a
// client that attaches late, or reattaches, still sees the whole stream.
type run struct {
	id        string
	sessionID string
	model     string
	startedAt time.Time
	now       func() time.Time
	chat      *chat.Chat

	mu         sync.Mutex
	status     protocol.RunStatus
	finishedAt time.Time
	failure    string
	summary    protocol.Summary
	records    []protocol.Record
	closed     bool
	changed    chan struct{}
}

// append buffers a record and wakes every attached stream.
func (r *run) append(record protocol.Record) {
	r.mu.Lock()
	r.records = append(r.records, record)
	r.wakeLocked()
	r.mu.Unlock()
}

func (r *run) addUsage(usage protocol.Usage) {
	r.mu.Lock()
	r.summary.Add(usage)
	r.mu.Unlock()
}

// finish closes the run with its final status and totals.
func (r *run) finish(status protocol.RunStatus, failure string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.status = status
	r.failure = failure
	r.finishedAt = r.now()
	r.records = append(r.records, protocol.RunFinished(r.id, status, r.summary, failure))
	r.closed = true
	r.wakeLocked()
	r.mu.Unlock()
}

// wakeLocked signals waiters by replacing the change channel, so a reader that is
// mid-await always observes the latest state.
func (r *run) wakeLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// snapshot returns the buffered records, the current position, and a channel that
// closes when more arrive or the run ends.
func (r *run) snapshot(from int) ([]protocol.Record, int, <-chan struct{}, bool, protocol.RunInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := append([]protocol.Record(nil), r.records[from:]...)
	return records, len(r.records), r.changed, r.closed, r.infoLocked()
}

func (r *run) info() protocol.RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.infoLocked()
}

func (r *run) infoLocked() protocol.RunInfo {
	info := protocol.RunInfo{
		ID: r.id, SessionID: r.sessionID, Status: r.status, Model: r.model,
		StartedAt: r.startedAt, Error: r.failure,
	}
	if !r.finishedAt.IsZero() {
		info.FinishedAt = r.finishedAt
	}
	return info
}

// handleEvents streams a run's records as NDJSON, or as SSE when the client asks
// for it. It replays from the beginning and closes once the run has finished and
// every record has been written.
func (s *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	started, ok := s.lookup(request.PathValue("runID"))
	if !ok {
		writeError(writer, http.StatusNotFound, "unknown_run", "no such run on this server")
		return
	}
	sse := prefersEventStream(request)
	if sse {
		writer.Header().Set("Content-Type", "text/event-stream")
	} else {
		writer.Header().Set("Content-Type", "application/x-ndjson")
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	flusher, _ := writer.(http.Flusher)

	position := 0
	for {
		records, next, changed, closed, _ := started.snapshot(position)
		for _, record := range records {
			if err := writeRecord(writer, record, sse); err != nil {
				return
			}
		}
		position = next
		if flusher != nil {
			flusher.Flush()
		}
		if closed {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-changed:
		}
	}
}

func writeRecord(writer http.ResponseWriter, record protocol.Record, sse bool) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if sse {
		_, err = writer.Write([]byte("data: " + string(encoded) + "\n\n"))
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

// prefersEventStream reports whether the client asked for SSE.
func prefersEventStream(request *http.Request) bool {
	for _, header := range request.Header.Values("Accept") {
		for _, part := range strings.Split(header, ",") {
			if trimmed := strings.TrimSpace(part); trimmed == "text/event-stream" || strings.HasPrefix(trimmed, "text/event-stream;") {
				return true
			}
		}
	}
	return false
}
