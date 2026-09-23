package ai

type EventType string

const (
	EventStart         EventType = "start"
	EventTextStart     EventType = "text_start"
	EventTextDelta     EventType = "text_delta"
	EventTextEnd       EventType = "text_end"
	EventThinkingStart EventType = "thinking_start"
	EventThinkingDelta EventType = "thinking_delta"
	EventThinkingEnd   EventType = "thinking_end"
	EventToolCallStart EventType = "toolcall_start"
	EventToolCallDelta EventType = "toolcall_delta"
	EventToolCallEnd   EventType = "toolcall_end"
	EventDone          EventType = "done"
	EventError         EventType = "error"
)

// AssistantEvent is the provider-neutral streaming protocol. Partial is an
// immutable event-time snapshot so consumers can inspect events without racing
// the provider.
type AssistantEvent struct {
	Type         EventType         `json:"type"`
	ContentIndex int               `json:"contentIndex,omitempty"`
	Delta        string            `json:"delta,omitempty"`
	Content      string            `json:"content,omitempty"`
	Partial      *AssistantMessage `json:"partial,omitempty"`
	ToolCall     *ToolCall         `json:"toolCall,omitempty"`
	Reason       StopReason        `json:"reason,omitempty"`
	Message      *AssistantMessage `json:"message,omitempty"`
	Error        *AssistantMessage `json:"error,omitempty"`
}

func (e AssistantEvent) terminal() (*AssistantMessage, bool) {
	switch e.Type {
	case EventDone:
		return e.Message, true
	case EventError:
		return e.Error, true
	default:
		return nil, false
	}
}
