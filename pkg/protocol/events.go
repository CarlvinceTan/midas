package protocol

import (
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// Record types emitted by a run. A consumer switches on Type and ignores fields
// it does not know, which is what keeps the stream forward-compatible.
const (
	EventRunStarted  = "run_started"
	EventText        = "text"
	EventToolStart   = "tool_start"
	EventToolEnd     = "tool_end"
	EventUsage       = "usage"
	EventNotice      = "notice"
	EventCompaction  = "compaction"
	EventRunFinished = "run_finished"
)

// EventTypes is every record type in the contract, in the order the OpenAPI and
// JSON Schema documents list them.
func EventTypes() []string {
	return []string{
		EventRunStarted, EventText, EventToolStart, EventToolEnd,
		EventUsage, EventNotice, EventCompaction, EventRunFinished,
	}
}

// Record is one line of a run's event stream. It is a single struct rather than
// an interface so decoding never needs type discovery: Type selects which fields
// are meaningful, and everything else is omitted from the JSON.
type Record struct {
	Envelope
	Type string `json:"type"`
	// RunID and SessionID identify what the record belongs to.
	RunID     string `json:"runID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	// Model names the model serving the run, set on EventRunStarted.
	Model string `json:"model,omitempty"`
	// Delta carries an assistant text fragment for EventText.
	Delta string `json:"delta,omitempty"`
	// Tool, CallID, and IsError describe tool activity.
	Tool    string `json:"tool,omitempty"`
	CallID  string `json:"callID,omitempty"`
	IsError bool   `json:"isError,omitempty"`
	// Status is set on EventRunFinished.
	Status RunStatus `json:"status,omitempty"`
	// Message carries a human-readable notice.
	Message string `json:"message,omitempty"`
	// TokensBefore and TokensAfter describe a compaction: the context the
	// checkpoint replaced, and the context left behind.
	TokensBefore int `json:"tokensBefore,omitempty"`
	TokensAfter  int `json:"tokensAfter,omitempty"`
	// Failure explains a failed or aborted run.
	Failure string `json:"failure,omitempty"`
	// Usage is set on EventUsage: one completed assistant turn.
	Usage *Usage `json:"usage,omitempty"`
	// Summary is set on EventRunFinished: the run's totals.
	Summary *Summary `json:"summary,omitempty"`
}

// Usage is one turn's token and cost accounting, including the provider's cache
// split so a consumer can see how much of the prompt was served from cache.
type Usage struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Total      int     `json:"total"`
	CostUSD    float64 `json:"costUSD,omitempty"`
}

// UsageFromAI converts the core usage type.
func UsageFromAI(usage ai.Usage) Usage {
	return Usage{
		Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead,
		CacheWrite: usage.CacheWrite, Total: usage.TotalTokens, CostUSD: usage.Cost.Total,
	}
}

// PromptTokens is every prompt token the provider billed for: cache reads,
// cache writes, and uncached input.
func (u Usage) PromptTokens() int { return u.CacheRead + u.CacheWrite + u.Input }

// CacheHitRate is the share of prompt tokens served from the provider's cache.
func (u Usage) CacheHitRate() float64 {
	if total := u.PromptTokens(); total > 0 {
		return float64(u.CacheRead) / float64(total)
	}
	return 0
}

// Summary totals a run's usage across every assistant turn in it.
type Summary struct {
	Calls      int     `json:"calls"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Total      int     `json:"total"`
	CostUSD    float64 `json:"costUSD,omitempty"`
	// CacheHitRate is the run's overall cache hit rate, precomputed so a UI does
	// not have to redo the arithmetic.
	CacheHitRate float64 `json:"cacheHitRate"`
}

// Add folds one turn's usage into the summary and refreshes the hit rate.
func (s *Summary) Add(usage Usage) {
	s.Calls++
	s.Input += usage.Input
	s.Output += usage.Output
	s.CacheRead += usage.CacheRead
	s.CacheWrite += usage.CacheWrite
	s.Total += usage.Total
	s.CostUSD += usage.CostUSD
	prompt := s.CacheRead + s.CacheWrite + s.Input
	if prompt > 0 {
		s.CacheHitRate = float64(s.CacheRead) / float64(prompt)
	}
}

// FromAgentEvent translates one core event into a record. ok is false for core
// events that are internal bookkeeping and never part of the contract.
func FromAgentEvent(event agent.Event) (record Record, ok bool) {
	switch event.Type {
	case agent.EventMessageUpdate:
		if event.AssistantEvent == nil || event.AssistantEvent.Type != ai.EventTextDelta {
			return Record{}, false
		}
		return NewRecord(EventText, func(r *Record) { r.Delta = event.AssistantEvent.Delta }), true
	case agent.EventMessageEnd:
		assistant, isAssistant := event.Message.(ai.AssistantMessage)
		if !isAssistant {
			return Record{}, false
		}
		usage := UsageFromAI(assistant.Usage)
		return NewRecord(EventUsage, func(r *Record) { r.Usage = &usage }), true
	case agent.EventToolExecutionStart:
		return NewRecord(EventToolStart, func(r *Record) {
			r.Tool = event.ToolName
			r.CallID = event.ToolCallID
		}), true
	case agent.EventToolExecutionEnd:
		return NewRecord(EventToolEnd, func(r *Record) {
			r.Tool = event.ToolName
			r.CallID = event.ToolCallID
			r.IsError = event.IsError
		}), true
	case agent.EventNotice:
		return NewRecord(EventNotice, func(r *Record) { r.Message = event.Notice }), true
	case agent.EventCompaction:
		return NewRecord(EventCompaction, func(r *Record) {
			r.TokensBefore = event.TokensBefore
			r.TokensAfter = event.TokensAfter
			if event.Err != nil {
				r.Failure = event.Err.Error()
			}
		}), true
	default:
		return Record{}, false
	}
}

// NewRecord builds a record of the given type and applies its field options.
func NewRecord(kind string, apply ...func(*Record)) Record {
	record := Record{Envelope: NewEnvelope(), Type: kind}
	for _, set := range apply {
		set(&record)
	}
	return record
}

// RunStarted is the first record of a stream.
func RunStarted(runID, sessionID, model string) Record {
	return NewRecord(EventRunStarted, func(r *Record) {
		r.RunID = runID
		r.SessionID = sessionID
		r.Model = model
	})
}

// RunFinished is the last record of a stream, carrying the run's totals and the
// reason it stopped.
func RunFinished(runID string, status RunStatus, summary Summary, failure string) Record {
	return NewRecord(EventRunFinished, func(r *Record) {
		r.RunID = runID
		r.Status = status
		r.Summary = &summary
		r.Failure = failure
	})
}
