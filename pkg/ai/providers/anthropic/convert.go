package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func buildRequest(model ai.Model, context ai.Context, options ai.StreamOptions) (map[string]any, error) {
	messages, err := convertMessages(context.Messages)
	if err != nil {
		return nil, err
	}
	maximum := options.MaxTokens
	if maximum == 0 {
		maximum = model.MaxTokens
	}
	if maximum == 0 {
		maximum = 4096
	}
	payload := map[string]any{
		"model":      model.ID,
		"messages":   messages,
		"max_tokens": maximum,
		"stream":     true,
	}
	cacheControl := anthropicCacheControl(options.CacheRetention)
	if context.SystemPrompt != "" {
		block := map[string]any{"type": "text", "text": context.SystemPrompt}
		if cacheControl != nil {
			block["cache_control"] = cacheControl
		}
		payload["system"] = []any{block}
	}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}
	if len(context.Tools) > 0 {
		tools, err := convertTools(context.Tools)
		if err != nil {
			return nil, err
		}
		if cacheControl != nil && len(tools) > 0 {
			tools[len(tools)-1].(map[string]any)["cache_control"] = cacheControl
		}
		payload["tools"] = tools
	}
	markAnthropicConversationCache(messages, cacheControl)
	if model.Reasoning && options.Reasoning != "" && options.Reasoning != ai.ThinkingOff {
		effort := string(options.Reasoning)
		if mapped := model.ThinkingLevelMap[options.Reasoning]; mapped != nil {
			effort = *mapped
		}
		payload["thinking"] = map[string]any{"type": "adaptive"}
		payload["output_config"] = map[string]any{"effort": effort}
	}
	for key, value := range model.SamplingParams {
		payload[key] = value
	}
	for key, value := range options.SamplingParams {
		payload[key] = value
	}
	return payload, nil
}

func anthropicCacheControl(retention ai.CacheRetention) map[string]any {
	if retention == ai.CacheNone {
		return nil
	}
	control := map[string]any{"type": "ephemeral"}
	if retention == ai.CacheLong {
		control["ttl"] = "1h"
	}
	return control
}

func markAnthropicConversationCache(messages []any, cacheControl map[string]any) {
	if cacheControl == nil {
		return
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok || len(content) == 0 {
			continue
		}
		block, ok := content[len(content)-1].(map[string]any)
		if ok {
			block["cache_control"] = cacheControl
		}
		return
	}
}

func convertMessages(messages []ai.Message) ([]any, error) {
	result := make([]any, 0, len(messages))
	for _, message := range messages {
		var converted map[string]any
		switch message := message.(type) {
		case ai.UserMessage:
			converted = convertUser(message)
		case *ai.UserMessage:
			if message != nil {
				converted = convertUser(*message)
			}
		case ai.AssistantMessage:
			var err error
			converted, err = convertAssistant(message)
			if err != nil {
				return nil, err
			}
		case *ai.AssistantMessage:
			if message != nil {
				var err error
				converted, err = convertAssistant(*message)
				if err != nil {
					return nil, err
				}
			}
		case ai.ToolResultMessage:
			converted = convertToolResult(message)
		case *ai.ToolResultMessage:
			if message != nil {
				converted = convertToolResult(*message)
			}
		}
		if converted != nil {
			result = appendOrMerge(result, converted)
		}
	}
	return result, nil
}

func appendOrMerge(messages []any, message map[string]any) []any {
	if len(messages) == 0 {
		return append(messages, message)
	}
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != message["role"] {
		return append(messages, message)
	}
	lastContent, lastOK := last["content"].([]any)
	nextContent, nextOK := message["content"].([]any)
	if !lastOK || !nextOK {
		return append(messages, message)
	}
	last["content"] = append(lastContent, nextContent...)
	return messages
}

func convertUser(message ai.UserMessage) map[string]any {
	content := make([]any, 0)
	if text, ok := message.Content.Text(); ok {
		content = append(content, map[string]any{"type": "text", "text": text})
	} else {
		for _, part := range message.Content.Parts() {
			switch part := part.(type) {
			case ai.TextContent:
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			case *ai.TextContent:
				if part != nil {
					content = append(content, map[string]any{"type": "text", "text": part.Text})
				}
			case ai.ImageContent:
				content = append(content, anthropicImage(part))
			case *ai.ImageContent:
				if part != nil {
					content = append(content, anthropicImage(*part))
				}
			}
		}
	}
	return map[string]any{"role": "user", "content": content}
}

func convertAssistant(message ai.AssistantMessage) (map[string]any, error) {
	content := make([]any, 0, len(message.Content))
	for _, block := range message.Content {
		switch block := block.(type) {
		case ai.TextContent:
			content = append(content, map[string]any{"type": "text", "text": block.Text})
		case *ai.TextContent:
			if block != nil {
				content = append(content, map[string]any{"type": "text", "text": block.Text})
			}
		case ai.ThinkingContent:
			content = append(content, anthropicThinking(block))
		case *ai.ThinkingContent:
			if block != nil {
				content = append(content, anthropicThinking(*block))
			}
		case ai.ToolCall:
			content = append(content, anthropicToolCall(block))
		case *ai.ToolCall:
			if block != nil {
				content = append(content, anthropicToolCall(*block))
			}
		}
	}
	return map[string]any{"role": "assistant", "content": content}, nil
}

func anthropicThinking(block ai.ThinkingContent) map[string]any {
	if block.Redacted {
		return map[string]any{"type": "redacted_thinking", "data": block.ThinkingSignature}
	}
	return map[string]any{
		"type": "thinking", "thinking": block.Thinking, "signature": block.ThinkingSignature,
	}
}

func anthropicToolCall(call ai.ToolCall) map[string]any {
	return map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Arguments}
}

func convertToolResult(message ai.ToolResultMessage) map[string]any {
	content := make([]any, 0, len(message.Content))
	for _, part := range message.Content {
		switch part := part.(type) {
		case ai.TextContent:
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case *ai.TextContent:
			if part != nil {
				content = append(content, map[string]any{"type": "text", "text": part.Text})
			}
		case ai.ImageContent:
			content = append(content, anthropicImage(part))
		case *ai.ImageContent:
			if part != nil {
				content = append(content, anthropicImage(*part))
			}
		}
	}
	return map[string]any{
		"role": "user",
		"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": message.ToolCallID, "content": content, "is_error": message.IsError,
		}},
	}
}

func anthropicImage(image ai.ImageContent) map[string]any {
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": image.MIMEType, "data": image.Data},
	}
}

func convertTools(tools []ai.Tool) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		inputSchema := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Parameters) > 0 {
			if err := json.Unmarshal(tool.Parameters, &inputSchema); err != nil {
				return nil, fmt.Errorf("anthropic: invalid schema for tool %s: %w", tool.Name, err)
			}
		}
		result = append(result, map[string]any{
			"name": tool.Name, "description": tool.Description, "input_schema": inputSchema,
		})
	}
	return result, nil
}
