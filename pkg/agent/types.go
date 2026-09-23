// Package agent runs provider-neutral model/tool loops on top of package ai.
package agent

import (
	"context"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

type ToolExecutionMode string

const (
	ToolExecutionParallel   ToolExecutionMode = "parallel"
	ToolExecutionSequential ToolExecutionMode = "sequential"
)

type CacheWarmingMode string

const (
	CacheWarmingOff       CacheWarmingMode = "off"
	CacheWarmingStreaming CacheWarmingMode = "streaming"
	CacheWarmingIdle      CacheWarmingMode = "idle"
)

type ToolResult struct {
	Content        []ai.Content
	Details        map[string]any
	Usage          *ai.Usage
	AddedToolNames []string
	Terminate      bool
}

type ToolUpdate func(ToolResult)

// Tool is the executable counterpart to an ai.Tool definition.
type Tool interface {
	Definition() ai.Tool
	Execute(ctx context.Context, call ai.ToolCall, update ToolUpdate) (ToolResult, error)
}

// SequentialTool can opt a tool out of otherwise parallel batch execution.
type SequentialTool interface {
	Sequential() bool
}

type Context struct {
	SystemPrompt string
	Messages     []ai.Message
	Tools        []Tool
}

type Config struct {
	Provider      ai.Streamer
	Model         ai.Model
	StreamOptions ai.StreamOptions
	ToolExecution ToolExecutionMode
	Steering      *SteeringQueue
	CacheWarming  CacheWarmingMode
	CacheWarmer   *CacheWarmer
	OnCacheWarm   func(ai.Usage)
}

type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageUpdate       EventType = "message_update"
	EventMessageEnd          EventType = "message_end"
	EventNotice              EventType = "notice"
	EventCompaction          EventType = "compaction"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
)

type Event struct {
	Type EventType
	Err  error
	// Notice carries a short message Midas reports about the run itself rather
	// than model output.
	Notice string
	// TokensBefore and TokensAfter describe a compaction: the context size that
	// was replaced, and the size afterwards.
	TokensBefore int
	TokensAfter  int

	Messages    []ai.Message
	Message     ai.Message
	ToolResults []ai.ToolResultMessage

	AssistantEvent *ai.AssistantEvent
	ToolCallID     string
	ToolName       string
	Arguments      map[string]any
	PartialResult  *ToolResult
	Result         *ToolResult
	IsError        bool
}

type Stream struct {
	*ai.EventStream[Event, []ai.Message]
}

func newStream() *Stream {
	return &Stream{EventStream: ai.NewEventStream(func(event Event) ([]ai.Message, bool) {
		return event.Messages, event.Type == EventAgentEnd
	})}
}
