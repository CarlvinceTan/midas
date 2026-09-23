package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/ai/providers/internal/sse"
)

type responseEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Code        any             `json:"code"`
	Message     string          `json:"message"`
	Item        responseItem    `json:"item"`
	Response    responsePayload `json:"response"`
}

type responseItem struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	Namespace        string `json:"namespace"`
	Input            string `json:"input"`
	Phase            string `json:"phase"`
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type responsePayload struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	ServiceTier       string `json:"service_tier"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

type outputSlot struct {
	kind     string
	index    int
	text     *ai.TextContent
	thinking *ai.ThinkingContent
	toolCall *ai.ToolCall
	json     string
}

func consumeResponsesStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, model ai.Model, stream *ai.AssistantStream) {
	defer cancel()
	defer body.Close()

	output := ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		StopReason: ai.StopPending,
		Timestamp:  ai.UnixMillis(time.Now()),
	}
	pushStart(stream, output)

	slots := make(map[int]*outputSlot)
	sawTerminal := false
	err := sse.Scan(body, func(data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		var event responseEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("openai: decode stream event: %w", err)
		}
		terminal, err := applyEvent(&output, slots, event, data, model, stream)
		if terminal {
			sawTerminal = true
		}
		return err
	})
	if err == nil && !sawTerminal {
		err = errTerminalMissing
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
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

func applyEvent(output *ai.AssistantMessage, slots map[int]*outputSlot, event responseEvent, raw []byte, model ai.Model, stream *ai.AssistantStream) (bool, error) {
	switch event.Type {
	case "response.created":
		output.ResponseID = event.Response.ID
	case "response.output_item.added":
		createSlot(output, slots, event.OutputIndex, event.Item, stream)
	case "response.output_text.delta", "response.refusal.delta":
		if slot := slotOf(slots, event.OutputIndex, "text"); slot != nil {
			slot.text.Text += event.Delta
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextDelta, ContentIndex: slot.index, Delta: event.Delta})
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if slot := slotOf(slots, event.OutputIndex, "thinking"); slot != nil {
			slot.thinking.Thinking += event.Delta
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingDelta, ContentIndex: slot.index, Delta: event.Delta})
		}
	case "response.reasoning_summary_part.done":
		if slot := slotOf(slots, event.OutputIndex, "thinking"); slot != nil {
			slot.thinking.Thinking += "\n\n"
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingDelta, ContentIndex: slot.index, Delta: "\n\n"})
		}
	case "response.function_call_arguments.delta":
		if slot := slotOf(slots, event.OutputIndex, "toolCall"); slot != nil {
			slot.json += event.Delta
			slot.toolCall.Arguments = parseArguments(slot.json)
			pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallDelta, ContentIndex: slot.index, Delta: event.Delta})
		}
	case "response.function_call_arguments.done":
		if slot := slotOf(slots, event.OutputIndex, "toolCall"); slot != nil {
			if strings.HasPrefix(event.Arguments, slot.json) {
				delta := strings.TrimPrefix(event.Arguments, slot.json)
				if delta != "" {
					pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallDelta, ContentIndex: slot.index, Delta: delta})
				}
			}
			slot.json = event.Arguments
			slot.toolCall.Arguments = parseArguments(slot.json)
		}
	case "response.output_item.done":
		finishSlot(output, slots, event.OutputIndex, event.Item, raw, stream)
	case "response.completed", "response.incomplete":
		finalizeResponse(output, event.Response, model)
		return true, nil
	case "response.failed":
		if event.Response.Error != nil {
			return true, fmt.Errorf("openai: %v: %s", event.Response.Error.Code, event.Response.Error.Message)
		}
		return true, errors.New("openai: response failed without error details")
	case "error":
		return true, fmt.Errorf("openai: error code %v: %s", event.Code, event.Message)
	}
	return false, nil
}

func createSlot(output *ai.AssistantMessage, slots map[int]*outputSlot, outputIndex int, item responseItem, stream *ai.AssistantStream) *outputSlot {
	if existing := slots[outputIndex]; existing != nil {
		return existing
	}
	switch item.Type {
	case "message":
		block := &ai.TextContent{Type: "text"}
		output.Content = append(output.Content, block)
		slot := &outputSlot{kind: "text", index: len(output.Content) - 1, text: block}
		slots[outputIndex] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextStart, ContentIndex: slot.index})
		return slot
	case "reasoning":
		block := &ai.ThinkingContent{Type: "thinking"}
		output.Content = append(output.Content, block)
		slot := &outputSlot{kind: "thinking", index: len(output.Content) - 1, thinking: block}
		slots[outputIndex] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingStart, ContentIndex: slot.index})
		return slot
	case "function_call":
		block := &ai.ToolCall{
			Type:      "toolCall",
			ID:        item.CallID + "|" + item.ID,
			Name:      item.Name,
			Arguments: parseArguments(item.Arguments),
			Namespace: item.Namespace,
		}
		output.Content = append(output.Content, block)
		slot := &outputSlot{kind: "toolCall", index: len(output.Content) - 1, toolCall: block, json: item.Arguments}
		slots[outputIndex] = slot
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallStart, ContentIndex: slot.index})
		return slot
	}
	return nil
}

func finishSlot(output *ai.AssistantMessage, slots map[int]*outputSlot, outputIndex int, item responseItem, raw []byte, stream *ai.AssistantStream) {
	slot := slots[outputIndex]
	if slot == nil {
		slot = createSlot(output, slots, outputIndex, item, stream)
	}
	if slot == nil {
		return
	}
	switch {
	case item.Type == "message" && slot.kind == "text":
		var text strings.Builder
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			} else {
				text.WriteString(content.Refusal)
			}
		}
		if text.Len() > 0 {
			slot.text.Text = text.String()
		}
		signature, _ := json.Marshal(map[string]any{"v": 1, "id": item.ID, "phase": item.Phase})
		if item.Phase == "" {
			signature, _ = json.Marshal(map[string]any{"v": 1, "id": item.ID})
		}
		slot.text.TextSignature = string(signature)
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventTextEnd, ContentIndex: slot.index, Content: slot.text.Text})
	case item.Type == "reasoning" && slot.kind == "thinking":
		var summary strings.Builder
		for index, part := range item.Summary {
			if index > 0 {
				summary.WriteString("\n\n")
			}
			summary.WriteString(part.Text)
		}
		if summary.Len() > 0 {
			slot.thinking.Thinking = summary.String()
		}
		var envelope struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(raw, &envelope) == nil && len(envelope.Item) > 0 {
			slot.thinking.ThinkingSignature = string(envelope.Item)
		}
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventThinkingEnd, ContentIndex: slot.index, Content: slot.thinking.Thinking})
	case item.Type == "function_call" && slot.kind == "toolCall":
		slot.toolCall.ID = item.CallID + "|" + item.ID
		slot.toolCall.Name = item.Name
		slot.toolCall.Namespace = item.Namespace
		arguments := item.Arguments
		if arguments == "" {
			arguments = slot.json
		}
		slot.toolCall.Arguments = parseArguments(arguments)
		call := *slot.toolCall
		call.Arguments = parseArguments(arguments)
		pushUpdate(stream, output, ai.AssistantEvent{Type: ai.EventToolCallEnd, ContentIndex: slot.index, ToolCall: &call})
	}
	delete(slots, outputIndex)
}

func finalizeResponse(output *ai.AssistantMessage, response responsePayload, model ai.Model) {
	if response.ID != "" {
		output.ResponseID = response.ID
	}
	if response.Usage != nil {
		cached := response.Usage.InputTokensDetails.CachedTokens
		cacheWrite := response.Usage.InputTokensDetails.CacheWriteTokens
		reasoning := response.Usage.OutputTokensDetails.ReasoningTokens
		output.Usage = ai.Usage{
			Input:       max(0, response.Usage.InputTokens-cached-cacheWrite),
			Output:      response.Usage.OutputTokens,
			CacheRead:   cached,
			CacheWrite:  cacheWrite,
			Reasoning:   &reasoning,
			TotalTokens: response.Usage.TotalTokens,
		}
		ai.CalculateCost(model, &output.Usage)
	}
	status := response.Status
	incompleteReason := ""
	if response.IncompleteDetails != nil {
		incompleteReason = response.IncompleteDetails.Reason
	}
	output.RawStopReason = status
	if incompleteReason != "" {
		output.RawStopReason += "." + incompleteReason
	}
	switch status {
	case "", "completed", "in_progress", "queued":
		output.StopReason = ai.StopComplete
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			output.StopReason = ai.StopLength
		} else {
			output.StopReason = ai.StopError
			if incompleteReason == "" {
				output.ErrorMessage = "response incomplete without a provider reason"
			} else {
				output.ErrorMessage = "response incomplete: " + incompleteReason
			}
		}
	default:
		output.StopReason = ai.StopError
		output.ErrorMessage = "response ended with status " + status
	}
	if output.StopReason == ai.StopComplete && hasToolCall(output.Content) {
		output.StopReason = ai.StopToolUse
	}
}

func slotOf(slots map[int]*outputSlot, index int, kind string) *outputSlot {
	slot := slots[index]
	if slot == nil || slot.kind != kind {
		return nil
	}
	return slot
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

func parseArguments(value string) map[string]any {
	if strings.TrimSpace(value) == "" {
		return map[string]any{}
	}
	var arguments map[string]any
	if json.Unmarshal([]byte(value), &arguments) != nil || arguments == nil {
		return map[string]any{}
	}
	return arguments
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
