package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/internal/sse"
)

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type      string         `json:"type"`
		Text      string         `json:"text"`
		Thinking  string         `json:"thinking"`
		Signature string         `json:"signature"`
		Data      string         `json:"data"`
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Input     map[string]any `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
		StopDetails *struct {
			Explanation string `json:"explanation"`
		} `json:"stop_details"`
	} `json:"delta"`
	Usage usage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type usage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1HInputTokens int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokenDetails *struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type contentSlot struct {
	kind     string
	index    int
	text     *ai.TextContent
	thinking *ai.ThinkingContent
	toolCall *ai.ToolCall
	json     string
}

func consumeStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, model ai.Model, stream *ai.AssistantStream) {
	defer cancel()
	defer body.Close()

	output := ai.AssistantMessage{
		Role: ai.RoleAssistant, API: model.API, Provider: model.Provider, Model: model.ID,
		StopReason: ai.StopPending, Timestamp: ai.UnixMillis(time.Now()),
	}
	pushStart(stream, output)
	slots := make(map[int]*contentSlot)
	sawMessageStop := false
	err := sse.Scan(body, func(data []byte) error {
		var event streamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("anthropic: decode stream event: %w", err)
		}
		switch event.Type {
		case "ping":
			return nil
		case "error":
			return fmt.Errorf("anthropic: %s: %s", event.Error.Type, event.Error.Message)
		case "message_start":
			output.ResponseID = event.Message.ID
			if event.Message.Model != "" && event.Message.Model != model.ID {
				output.ResponseModel = event.Message.Model
			}
			applyUsage(&output, event.Message.Usage, model)
		case "content_block_start":
			startBlock(&output, slots, event, stream)
		case "content_block_delta":
			updateBlock(&output, slots[event.Index], event, stream)
		case "content_block_stop":
			stopBlock(&output, slots[event.Index], stream)
			delete(slots, event.Index)
		case "message_delta":
			if event.Delta.StopReason != "" {
				mapStop(&output, event.Delta.StopReason, event.Delta.StopDetails)
			}
			applyUsage(&output, event.Usage, model)
		case "message_stop":
			sawMessageStop = true
		}
		return nil
	})
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && !sawMessageStop {
		err = errors.New("anthropic: stream ended before message_stop")
	}
	if err == nil && output.StopReason == ai.StopPending {
		err = errors.New("anthropic: stream ended without a stop reason")
	}
	if err != nil || output.StopReason == ai.StopError || output.StopReason == ai.StopAborted {
		if err != nil {
			output.StopReason = ai.StopError
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				output.StopReason = ai.StopAborted
			}
			output.ErrorMessage = err.Error()
		}
		final := ai.CloneAssistantMessage(output)
		stream.Push(ai.AssistantEvent{Type: ai.EventError, Reason: final.StopReason, Error: &final})
		return
	}
	final := ai.CloneAssistantMessage(output)
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: final.StopReason, Message: &final})
}

func startBlock(output *ai.AssistantMessage, slots map[int]*contentSlot, event streamEvent, stream *ai.AssistantStream) {
	switch event.ContentBlock.Type {
	case "text":
		block := &ai.TextContent{Type: "text", Text: event.ContentBlock.Text}
		output.Content = append(output.Content, block)
		slot := &contentSlot{kind: "text", index: len(output.Content) - 1, text: block}
		slots[event.Index] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextStart, ContentIndex: slot.index})
	case "thinking":
		block := &ai.ThinkingContent{Type: "thinking", Thinking: event.ContentBlock.Thinking, ThinkingSignature: event.ContentBlock.Signature}
		output.Content = append(output.Content, block)
		slot := &contentSlot{kind: "thinking", index: len(output.Content) - 1, thinking: block}
		slots[event.Index] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingStart, ContentIndex: slot.index})
	case "redacted_thinking":
		block := &ai.ThinkingContent{Type: "thinking", Thinking: "[Reasoning redacted]", ThinkingSignature: event.ContentBlock.Data, Redacted: true}
		output.Content = append(output.Content, block)
		slot := &contentSlot{kind: "thinking", index: len(output.Content) - 1, thinking: block}
		slots[event.Index] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingStart, ContentIndex: slot.index})
	case "tool_use":
		arguments := event.ContentBlock.Input
		if arguments == nil {
			arguments = map[string]any{}
		}
		block := &ai.ToolCall{Type: "toolCall", ID: event.ContentBlock.ID, Name: event.ContentBlock.Name, Arguments: arguments}
		output.Content = append(output.Content, block)
		slot := &contentSlot{kind: "toolCall", index: len(output.Content) - 1, toolCall: block}
		slots[event.Index] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallStart, ContentIndex: slot.index})
	}
}

func updateBlock(output *ai.AssistantMessage, slot *contentSlot, event streamEvent, stream *ai.AssistantStream) {
	if slot == nil {
		return
	}
	switch event.Delta.Type {
	case "text_delta":
		if slot.kind == "text" {
			slot.text.Text += event.Delta.Text
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextDelta, ContentIndex: slot.index, Delta: event.Delta.Text})
		}
	case "thinking_delta":
		if slot.kind == "thinking" {
			slot.thinking.Thinking += event.Delta.Thinking
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingDelta, ContentIndex: slot.index, Delta: event.Delta.Thinking})
		}
	case "signature_delta":
		if slot.kind == "thinking" {
			slot.thinking.ThinkingSignature += event.Delta.Signature
		}
	case "input_json_delta":
		if slot.kind == "toolCall" {
			slot.json += event.Delta.PartialJSON
			slot.toolCall.Arguments = parseArguments(slot.json)
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallDelta, ContentIndex: slot.index, Delta: event.Delta.PartialJSON})
		}
	}
}

func stopBlock(output *ai.AssistantMessage, slot *contentSlot, stream *ai.AssistantStream) {
	if slot == nil {
		return
	}
	switch slot.kind {
	case "text":
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextEnd, ContentIndex: slot.index, Content: slot.text.Text})
	case "thinking":
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingEnd, ContentIndex: slot.index, Content: slot.thinking.Thinking})
	case "toolCall":
		if slot.json != "" {
			slot.toolCall.Arguments = parseArguments(slot.json)
		}
		call := *slot.toolCall
		call.Arguments = cloneArguments(slot.toolCall.Arguments)
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallEnd, ContentIndex: slot.index, ToolCall: &call})
	}
}

func applyUsage(output *ai.AssistantMessage, value usage, model ai.Model) {
	if value.InputTokens != nil {
		output.Usage.Input = *value.InputTokens
	}
	if value.OutputTokens != nil {
		output.Usage.Output = *value.OutputTokens
	}
	if value.CacheReadInputTokens != nil {
		output.Usage.CacheRead = *value.CacheReadInputTokens
	}
	if value.CacheCreationInputTokens != nil {
		output.Usage.CacheWrite = *value.CacheCreationInputTokens
	}
	if value.CacheCreation != nil {
		long := value.CacheCreation.Ephemeral1HInputTokens
		output.Usage.CacheWrite1H = &long
	}
	if value.OutputTokenDetails != nil {
		reasoning := value.OutputTokenDetails.ThinkingTokens
		output.Usage.Reasoning = &reasoning
	}
	output.Usage.RecalculateTotals()
	ai.CalculateCost(model, &output.Usage)
}

func mapStop(output *ai.AssistantMessage, reason string, details *struct {
	Explanation string `json:"explanation"`
}) {
	output.RawStopReason = reason
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		output.StopReason = ai.StopComplete
	case "max_tokens":
		output.StopReason = ai.StopLength
	case "tool_use":
		output.StopReason = ai.StopToolUse
	case "refusal":
		output.StopReason = ai.StopError
		output.ErrorMessage = "the model refused to complete the request"
		if details != nil && details.Explanation != "" {
			output.ErrorMessage = details.Explanation
		}
	case "sensitive":
		output.StopReason = ai.StopError
		output.ErrorMessage = "provider stopped with: sensitive"
	default:
		output.StopReason = ai.StopError
		output.ErrorMessage = "unknown provider stop reason: " + reason
	}
}

func parseArguments(value string) map[string]any {
	if value == "" {
		return map[string]any{}
	}
	var result map[string]any
	if json.Unmarshal([]byte(value), &result) != nil || result == nil {
		return map[string]any{}
	}
	return result
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
