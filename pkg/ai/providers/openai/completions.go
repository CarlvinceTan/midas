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

func buildCompletionsRequest(model ai.Model, context ai.Context, options ai.StreamOptions) (map[string]any, error) {
	messages, err := convertCompletionMessages(model, context)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":          model.ID,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if options.SessionID != "" && options.CacheRetention != ai.CacheNone {
		// OpenAI-compatible gateways route cache lookups by this key, so a stable
		// session id keeps a conversation on the same cached prefix.
		payload["prompt_cache_key"] = clampPromptCacheKey(options.SessionID)
	}
	if options.MaxTokens > 0 {
		payload["max_completion_tokens"] = options.MaxTokens
	}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}
	if len(context.Tools) > 0 {
		tools, err := convertCompletionTools(context.Tools)
		if err != nil {
			return nil, err
		}
		payload["tools"] = tools
	}
	if model.Reasoning && options.Reasoning != "" && options.Reasoning != ai.ThinkingOff {
		effort := string(options.Reasoning)
		if mapped := model.ThinkingLevelMap[options.Reasoning]; mapped != nil {
			effort = *mapped
		}
		payload["reasoning_effort"] = effort
	}
	for key, value := range model.SamplingParams {
		payload[key] = value
	}
	for key, value := range options.SamplingParams {
		payload[key] = value
	}
	return payload, nil
}

func convertCompletionMessages(model ai.Model, context ai.Context) ([]any, error) {
	result := make([]any, 0, len(context.Messages)+1)
	if context.SystemPrompt != "" {
		result = append(result, map[string]any{"role": "system", "content": context.SystemPrompt})
	}
	for _, message := range context.Messages {
		switch message := message.(type) {
		case ai.UserMessage:
			result = append(result, completionUser(message))
		case *ai.UserMessage:
			if message != nil {
				result = append(result, completionUser(*message))
			}
		case ai.AssistantMessage:
			converted, err := completionAssistant(message)
			if err != nil {
				return nil, err
			}
			result = append(result, converted)
		case *ai.AssistantMessage:
			if message != nil {
				converted, err := completionAssistant(*message)
				if err != nil {
					return nil, err
				}
				result = append(result, converted)
			}
		case ai.ToolResultMessage:
			result = append(result, completionToolResult(model, message))
		case *ai.ToolResultMessage:
			if message != nil {
				result = append(result, completionToolResult(model, *message))
			}
		}
	}
	return result, nil
}

func completionUser(message ai.UserMessage) map[string]any {
	if text, ok := message.Content.Text(); ok {
		return map[string]any{"role": "user", "content": text}
	}
	content := make([]any, 0)
	for _, part := range message.Content.Parts() {
		switch part := part.(type) {
		case ai.TextContent:
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case *ai.TextContent:
			if part != nil {
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			}
		case ai.ImageContent:
			content = append(content, completionImage(part))
		case *ai.ImageContent:
			if part != nil {
				content = append(content, completionImage(*part))
			}
		}
	}
	return map[string]any{"role": "user", "content": content}
}

func completionAssistant(message ai.AssistantMessage) (map[string]any, error) {
	result := map[string]any{"role": "assistant"}
	var text, thinking strings.Builder
	var calls []any
	for _, block := range message.Content {
		switch block := block.(type) {
		case ai.TextContent:
			text.WriteString(block.Text)
		case *ai.TextContent:
			if block != nil {
				text.WriteString(block.Text)
			}
		case ai.ThinkingContent:
			thinking.WriteString(block.Thinking)
		case *ai.ThinkingContent:
			if block != nil {
				thinking.WriteString(block.Thinking)
			}
		case ai.ToolCall:
			call, err := completionToolCall(block)
			if err != nil {
				return nil, err
			}
			calls = append(calls, call)
		case *ai.ToolCall:
			if block != nil {
				call, err := completionToolCall(*block)
				if err != nil {
					return nil, err
				}
				calls = append(calls, call)
			}
		}
	}
	if text.Len() > 0 {
		result["content"] = text.String()
	} else {
		result["content"] = nil
	}
	if thinking.Len() > 0 {
		result["reasoning_content"] = thinking.String()
	}
	if len(calls) > 0 {
		result["tool_calls"] = calls
	}
	return result, nil
}

func completionToolCall(call ai.ToolCall) (map[string]any, error) {
	arguments, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("openai: encode arguments for tool %s: %w", call.Name, err)
	}
	return map[string]any{
		"id":   call.ID,
		"type": "function",
		"function": map[string]any{
			"name":      call.Name,
			"arguments": string(arguments),
		},
	}, nil
}

func completionToolResult(model ai.Model, message ai.ToolResultMessage) map[string]any {
	return map[string]any{
		"role":         "tool",
		"tool_call_id": message.ToolCallID,
		"content":      completionToolOutput(model, message.Content),
	}
}

func completionToolOutput(model ai.Model, content []ai.Content) any {
	if !supportsImages(model) {
		return toolResultOutput(model, content)
	}
	var parts []any
	for _, part := range content {
		switch part := part.(type) {
		case ai.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		case *ai.TextContent:
			if part != nil {
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
		case ai.ImageContent:
			parts = append(parts, completionImage(part))
		case *ai.ImageContent:
			if part != nil {
				parts = append(parts, completionImage(*part))
			}
		}
	}
	if len(parts) == 0 {
		return "(no tool output)"
	}
	return parts
}

func completionImage(image ai.ImageContent) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:" + image.MIMEType + ";base64," + image.Data,
		},
	}
}

func convertCompletionTools(tools []ai.Tool) ([]any, error) {
	converted, err := convertTools(tools)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, len(converted))
	for _, raw := range converted {
		function := raw.(map[string]any)
		delete(function, "type")
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result, nil
}

type completionChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent string  `json:"reasoning_content"`
			Reasoning        string  `json:"reasoning"`
			ReasoningText    string  `json:"reasoning_text"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string           `json:"finish_reason"`
		Usage        *completionUsage `json:"usage"`
	} `json:"choices"`
	Usage *completionUsage `json:"usage"`
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	PromptDetails    struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type completionToolSlot struct {
	call *ai.ToolCall
	json string
}

func consumeCompletionsStream(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, model ai.Model, stream *ai.AssistantStream) {
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
	var text *ai.TextContent
	var thinking *ai.ThinkingContent
	toolSlots := make(map[int]*completionToolSlot)
	hasFinishReason := false

	err := sse.Scan(body, func(data []byte) error {
		if string(data) == "[DONE]" {
			return nil
		}
		var chunk completionChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return fmt.Errorf("openai: decode completion chunk: %w", err)
		}
		if output.ResponseID == "" {
			output.ResponseID = chunk.ID
		}
		if chunk.Model != "" && chunk.Model != model.ID && output.ResponseModel == "" {
			output.ResponseModel = chunk.Model
		}
		usage := chunk.Usage
		if usage == nil && len(chunk.Choices) > 0 {
			usage = chunk.Choices[0].Usage
		}
		if usage != nil {
			applyCompletionUsage(&output, *usage, model)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			hasFinishReason = true
			mapCompletionStop(&output, choice.FinishReason)
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			if text == nil {
				text = &ai.TextContent{Type: "text"}
				output.Content = append(output.Content, text)
				pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventTextStart, ContentIndex: len(output.Content) - 1})
			}
			text.Text += *choice.Delta.Content
			pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventTextDelta, ContentIndex: contentIndex(output.Content, text), Delta: *choice.Delta.Content})
		}
		reasoningDelta := choice.Delta.ReasoningContent
		reasoningField := "reasoning_content"
		if reasoningDelta == "" {
			reasoningDelta = choice.Delta.Reasoning
			reasoningField = "reasoning"
		}
		if reasoningDelta == "" {
			reasoningDelta = choice.Delta.ReasoningText
			reasoningField = "reasoning_text"
		}
		if reasoningDelta != "" {
			if thinking == nil {
				thinking = &ai.ThinkingContent{Type: "thinking", ThinkingSignature: reasoningField}
				output.Content = append(output.Content, thinking)
				pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventThinkingStart, ContentIndex: len(output.Content) - 1})
			}
			thinking.Thinking += reasoningDelta
			pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventThinkingDelta, ContentIndex: contentIndex(output.Content, thinking), Delta: reasoningDelta})
		}
		for _, delta := range choice.Delta.ToolCalls {
			slot := toolSlots[delta.Index]
			if slot == nil {
				call := &ai.ToolCall{Type: "toolCall", ID: delta.ID, Name: delta.Function.Name, Arguments: map[string]any{}}
				output.Content = append(output.Content, call)
				slot = &completionToolSlot{call: call}
				toolSlots[delta.Index] = slot
				pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallStart, ContentIndex: len(output.Content) - 1})
			}
			if slot.call.ID == "" {
				slot.call.ID = delta.ID
			}
			if slot.call.Name == "" {
				slot.call.Name = delta.Function.Name
			}
			slot.json += delta.Function.Arguments
			slot.call.Arguments = parseArguments(slot.json)
			pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallDelta, ContentIndex: contentIndex(output.Content, slot.call), Delta: delta.Function.Arguments})
		}
		return nil
	})

	for contentIndex, block := range output.Content {
		switch block := block.(type) {
		case *ai.TextContent:
			pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventTextEnd, ContentIndex: contentIndex, Content: block.Text})
		case *ai.ThinkingContent:
			pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventThinkingEnd, ContentIndex: contentIndex, Content: block.Thinking})
		case *ai.ToolCall:
			for _, slot := range toolSlots {
				if slot.call != block {
					continue
				}
				call := *slot.call
				call.Arguments = parseArguments(slot.json)
				slot.call.Arguments = parseArguments(slot.json)
				pushUpdate(stream, &output, ai.AssistantEvent{Type: ai.EventToolCallEnd, ContentIndex: contentIndex, ToolCall: &call})
				break
			}
		}
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && !hasFinishReason {
		err = errors.New("openai: stream ended without finish_reason")
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

func applyCompletionUsage(output *ai.AssistantMessage, usage completionUsage, model ai.Model) {
	cached := usage.PromptDetails.CachedTokens
	if cached == 0 {
		cached = usage.CachedTokens
	}
	reasoning := usage.CompletionDetails.ReasoningTokens
	output.Usage = ai.Usage{
		Input:       max(0, usage.PromptTokens-cached),
		Output:      usage.CompletionTokens,
		CacheRead:   cached,
		Reasoning:   &reasoning,
		TotalTokens: usage.TotalTokens,
	}
	ai.CalculateCost(model, &output.Usage)
}

func mapCompletionStop(output *ai.AssistantMessage, reason string) {
	output.RawStopReason = reason
	switch reason {
	case "stop":
		output.StopReason = ai.StopComplete
	case "length":
		output.StopReason = ai.StopLength
	case "tool_calls", "function_call":
		output.StopReason = ai.StopToolUse
	case "content_filter", "network_error":
		output.StopReason = ai.StopError
		output.ErrorMessage = "provider finish_reason: " + reason
	default:
		output.StopReason = ai.StopError
		output.ErrorMessage = "unknown provider finish_reason: " + reason
	}
}

func contentIndex(content []ai.Content, target ai.Content) int {
	for index, block := range content {
		if block == target {
			return index
		}
	}
	return -1
}
