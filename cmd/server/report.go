package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/protocol"
)

// reporter writes one run to a caller. Prose mode streams the assistant text and
// ends with a usage footer; JSON mode writes the protocol records defined by
// pkg/protocol, one per line, so another program can consume the same stream the
// HTTP API will serve.
type reporter struct {
	output io.Writer
	json   bool

	mu        sync.Mutex
	wroteText bool
	runID     string
	summary   protocol.Summary
}

func newReporter(output io.Writer, asJSON bool) *reporter {
	return &reporter{output: output, json: asJSON}
}

// start records the run identity any later record carries.
func (r *reporter) start(runID, sessionID string, model ai.Model) {
	r.runID = runID
	if r.json {
		r.emit(protocol.RunStarted(runID, sessionID, model.Provider+"/"+model.ID))
	}
}

func (r *reporter) notice(message string) {
	if r.json {
		r.emit(protocol.NewRecord(protocol.EventNotice, func(record *protocol.Record) { record.Message = message }))
		return
	}
	fmt.Fprintf(r.output, "\n[%s]\n", message)
}

func (r *reporter) event(event agent.Event) {
	record, ok := protocol.FromAgentEvent(event)
	if !ok {
		return
	}
	if record.Type == protocol.EventUsage && record.Usage != nil {
		r.summary.Add(*record.Usage)
	}
	if record.Type == protocol.EventToolStart && !r.json {
		fmt.Fprintf(r.output, "\n· %s\n", record.Tool)
		return
	}
	if r.json {
		record.RunID = r.runID
		r.emit(record)
		return
	}
	if record.Type == protocol.EventText {
		r.text(record.Delta)
	}
}

// finish closes the stream with the run's totals.
func (r *reporter) finish(status protocol.RunStatus, failure string) {
	if r.json {
		finished := protocol.RunFinished(r.runID, status, r.summary, failure)
		finished.SessionID = ""
		r.emit(finished)
		return
	}
	r.mu.Lock()
	wroteText := r.wroteText
	r.mu.Unlock()
	if wroteText {
		fmt.Fprintln(r.output)
	}
	fmt.Fprintln(r.output, footer(r.summary))
}

func (r *reporter) text(delta string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wroteText = true
	fmt.Fprint(r.output, delta)
}

func (r *reporter) emit(record protocol.Record) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintln(r.output, string(encoded))
}

// footer is the human-readable cache accounting: how much of the prompt came
// from the provider's cache, and the resulting hit rate.
func footer(summary protocol.Summary) string {
	line := fmt.Sprintf("cache %.1f%%", summary.CacheHitRate*100)
	if prompt := summary.CacheRead + summary.CacheWrite + summary.Input; prompt > 0 {
		line += fmt.Sprintf(" • %d read / %d written / %d uncached", summary.CacheRead, summary.CacheWrite, summary.Input)
	}
	return fmt.Sprintf("%s • %d output • %d calls", line, summary.Output, summary.Calls)
}
