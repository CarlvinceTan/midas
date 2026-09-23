package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/internal/sse"
)

var toolCallCounter atomic.Uint64

type streamChunk struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             *string `json:"text"`
				Thought          bool    `json:"thought"`
				ThoughtSignature string  `json:"thoughtSignature"`
				FunctionCall     *struct {
					ID   string         `json:"id"`
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

type activeBlock struct {
	kind     string
	index    int
	text     *ai.TextContent
	thinking *ai.ThinkingContent
}

func consumeStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, model ai.Model, stream *ai.AssistantStream) {
	defer cancel()
	defer body.Close()

	output := ai.AssistantMessage{
		Role: ai.RoleAssistant, API: model.API, Provider: model.Provider, Model: model.ID,
		StopReason: ai.StopPending, Timestamp: ai.UnixMillis(time.Now()),
	}
	pushStart(stream, output)
	var current *activeBlock
	err := sse.Scan(body, func(data []byte) error {
		var chunk streamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return fmt.Errorf("google: decode stream chunk: %w", err)
		}
		if output.ResponseID == "" {
			output.ResponseID = chunk.ResponseID
		}
		if len(chunk.Candidates) > 0 {
			candidate := chunk.Candidates[0]
			for _, part := range candidate.Content.Parts {
				if part.Text != nil {
					kind := "text"
					if part.Thought {
						kind = "thinking"
					}
					if current == nil || current.kind != kind {
						finishBlock(&output, current, stream)
						current = startBlock(&output, kind, stream)
					}
					if kind == "thinking" {
						current.thinking.Thinking += *part.Text
						if part.ThoughtSignature != "" {
							current.thinking.ThinkingSignature = part.ThoughtSignature
						}
						pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventThinkingDelta, ContentIndex: current.index, Delta: *part.Text})
					} else {
						current.text.Text += *part.Text
						if part.ThoughtSignature != "" {
							current.text.TextSignature = part.ThoughtSignature
						}
						pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventTextDelta, ContentIndex: current.index, Delta: *part.Text})
					}
				}
				if part.FunctionCall != nil {
					finishBlock(&output, current, stream)
					current = nil
					id := part.FunctionCall.ID
					if id == "" || hasToolCallID(output.Content, id) {
						id = fmt.Sprintf("%s_%d_%d", part.FunctionCall.Name, time.Now().UnixMilli(), toolCallCounter.Add(1))
					}
					arguments := part.FunctionCall.Args
					if arguments == nil {
						arguments = map[string]any{}
					}
					call := &ai.ToolCall{
						Type: "toolCall", ID: id, Name: part.FunctionCall.Name, Arguments: arguments,
						ThoughtSignature: part.ThoughtSignature,
					}
					output.Content = append(output.Content, call)
					index := len(output.Content) - 1
					pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallStart, ContentIndex: index})
					encoded, _ := json.Marshal(arguments)
					pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallDelta, ContentIndex: index, Delta: string(encoded)})
					copy := *call
					copy.Arguments = cloneArguments(arguments)
					pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallEnd, ContentIndex: index, ToolCall: &copy})
				}
			}
			if candidate.FinishReason != "" {
				mapStop(&output, candidate.FinishReason)
			}
		}
		if chunk.UsageMetadata != nil {
			reasoning := chunk.UsageMetadata.ThoughtsTokenCount
			output.Usage = ai.Usage{
				Input:       max(0, chunk.UsageMetadata.PromptTokenCount-chunk.UsageMetadata.CachedContentTokenCount),
				Output:      chunk.UsageMetadata.CandidatesTokenCount + reasoning,
				CacheRead:   chunk.UsageMetadata.CachedContentTokenCount,
				Reasoning:   &reasoning,
				TotalTokens: chunk.UsageMetadata.TotalTokenCount,
			}
			ai.CalculateCost(model, &output.Usage)
		}
		return nil
	})
	finishBlock(&output, current, stream)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && output.StopReason == ai.StopPending {
		err = errors.New("google: stream ended without a finish reason")
	}
	if err != nil || output.StopReason == ai.StopError || output.StopReason == ai.StopAborted {
		if err != nil {
			output.StopReason = ai.StopError
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				output.StopReason = ai.StopAborted
			}
			output.ErrorMessage = err.Error()
		} else if output.ErrorMessage == "" {
			output.ErrorMessage = "provider stopped with: " + output.RawStopReason
		}
		final := ai.CloneAssistantMessage(output)
		stream.Push(ai.AssistantEvent{Type: ai.EventError, Reason: final.StopReason, Error: &final})
		return
	}
	final := ai.CloneAssistantMessage(output)
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: final.StopReason, Message: &final})
}

func startBlock(output *ai.AssistantMessage, kind string, stream *ai.AssistantStream) *activeBlock {
	block := &activeBlock{kind: kind, index: len(output.Content)}
	if kind == "thinking" {
		block.thinking = &ai.ThinkingContent{Type: "thinking"}
		output.Content = append(output.Content, block.thinking)
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingStart, ContentIndex: block.index})
	} else {
		block.text = &ai.TextContent{Type: "text"}
		output.Content = append(output.Content, block.text)
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextStart, ContentIndex: block.index})
	}
	return block
}

func finishBlock(output *ai.AssistantMessage, block *activeBlock, stream *ai.AssistantStream) {
	if block == nil {
		return
	}
	if block.kind == "thinking" {
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingEnd, ContentIndex: block.index, Content: block.thinking.Thinking})
	} else {
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextEnd, ContentIndex: block.index, Content: block.text.Text})
	}
}

func mapStop(output *ai.AssistantMessage, reason string) {
	output.RawStopReason = reason
	switch reason {
	case "STOP":
		output.StopReason = ai.StopComplete
		if hasToolCall(output.Content) {
			output.StopReason = ai.StopToolUse
		}
	case "MAX_TOKENS":
		output.StopReason = ai.StopLength
	default:
		output.StopReason = ai.StopError
		output.ErrorMessage = "provider stopped with: " + reason
	}
}

func hasToolCall(content []ai.Content) bool {
	for _, block := range content {
		switch block.(type) {
		case ai.ToolCall, *ai.ToolCall:
			return true
		}
	}
	return false
}

func hasToolCallID(content []ai.Content, id string) bool {
	for _, block := range content {
		switch block := block.(type) {
		case ai.ToolCall:
			if block.ID == id {
				return true
			}
		case *ai.ToolCall:
			if block != nil && block.ID == id {
				return true
			}
		}
	}
	return false
}

func cloneArguments(input map[string]any) map[string]any {
	data, _ := json.Marshal(input)
	var output map[string]any
	_ = json.Unmarshal(data, &output)
	return output
}

func pushStart(stream *ai.AssistantStream, output ai.AssistantMessage) {
	snapshot := ai.CloneAssistantMessage(output)
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &snapshot})
}

func pushUpdate(stream *ai.AssistantStream, output *ai.AssistantMessage, event ai.AssistantEvent) {
	snapshot := ai.CloneAssistantMessage(*output)
	event.Partial = &snapshot
	stream.Push(event)
}
