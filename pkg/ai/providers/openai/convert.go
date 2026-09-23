package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func buildRequest(model ai.Model, context ai.Context, options ai.StreamOptions) (map[string]any, error) {
	input, err := convertMessages(model, context)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":  model.ID,
		"input":  input,
		"stream": true,
		"store":  false,
	}
	if options.SessionID != "" && options.CacheRetention != ai.CacheNone {
		payload["prompt_cache_key"] = clampPromptCacheKey(options.SessionID)
	}
	if options.CacheRetention == ai.CacheLong {
		if usesExplicitPromptCacheTTL(model.ID) {
			payload["prompt_cache_options"] = map[string]any{"ttl": "30m"}
		} else {
			payload["prompt_cache_retention"] = "24h"
		}
	}
	if options.MaxTokens > 0 {
		maximum := options.MaxTokens
		if maximum < 16 {
			maximum = 16
		}
		payload["max_output_tokens"] = maximum
	}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}
	if len(context.Tools) > 0 {
		tools, err := convertTools(context.Tools)
		if err != nil {
			return nil, err
		}
		payload["tools"] = tools
	}
	if model.Reasoning {
		level := options.Reasoning
		if level == "" {
			level = ai.ThinkingOff
		}
		if mapped, exists := model.ThinkingLevelMap[level]; !exists || mapped != nil {
			effort := string(level)
			if level == ai.ThinkingOff {
				effort = "none"
			}
			if mapped != nil {
				effort = *mapped
			}
			reasoning := map[string]any{"effort": effort}
			if level != ai.ThinkingOff {
				reasoning["summary"] = "auto"
				payload["include"] = []string{"reasoning.encrypted_content"}
			}
			payload["reasoning"] = reasoning
		}
	}
	for key, value := range model.SamplingParams {
		payload[key] = value
	}
	for key, value := range options.SamplingParams {
		payload[key] = value
	}
	return payload, nil
}

func clampPromptCacheKey(value string) string {
	runes := []rune(value)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

func usesExplicitPromptCacheTTL(modelID string) bool {
	id := strings.ToLower(modelID)
	return strings.HasPrefix(id, "gpt-5.6") || strings.HasPrefix(id, "gpt-6")
}

func convertMessages(model ai.Model, context ai.Context) ([]any, error) {
	result := make([]any, 0, len(context.Messages)+1)
	if context.SystemPrompt != "" {
		role := "system"
		if model.Reasoning {
			role = "developer"
		}
		result = append(result, map[string]any{"role": role, "content": context.SystemPrompt})
	}
	for messageIndex, message := range context.Messages {
		switch message := message.(type) {
		case ai.UserMessage:
			converted, ok := convertUser(message)
			if ok {
				result = append(result, converted)
			}
		case *ai.UserMessage:
			if message != nil {
				converted, ok := convertUser(*message)
				if ok {
					result = append(result, converted)
				}
			}
		case ai.AssistantMessage:
			items, err := convertAssistant(message, messageIndex)
			if err != nil {
				return nil, err
			}
			result = append(result, items...)
		case *ai.AssistantMessage:
			if message != nil {
				items, err := convertAssistant(*message, messageIndex)
				if err != nil {
					return nil, err
				}
				result = append(result, items...)
			}
		case ai.ToolResultMessage:
			result = append(result, convertToolResult(model, message))
		case *ai.ToolResultMessage:
			if message != nil {
				result = append(result, convertToolResult(model, *message))
			}
		}
	}
	return result, nil
}

func convertUser(message ai.UserMessage) (map[string]any, bool) {
	if text, ok := message.Content.Text(); ok {
		return map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": text}},
		}, true
	}
	parts := make([]any, 0)
	for _, part := range message.Content.Parts() {
		switch part := part.(type) {
		case ai.TextContent:
			parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
		case *ai.TextContent:
			if part != nil {
				parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
			}
		case ai.ImageContent:
			parts = append(parts, responseImage(part))
		case *ai.ImageContent:
			if part != nil {
				parts = append(parts, responseImage(*part))
			}
		}
	}
	return map[string]any{"role": "user", "content": parts}, len(parts) > 0
}

func convertAssistant(message ai.AssistantMessage, messageIndex int) ([]any, error) {
	result := make([]any, 0, len(message.Content))
	textIndex := 0
	for _, block := range message.Content {
		switch block := block.(type) {
		case ai.TextContent:
			result = append(result, responseText(block, messageIndex, textIndex))
			textIndex++
		case *ai.TextContent:
			if block != nil {
				result = append(result, responseText(*block, messageIndex, textIndex))
				textIndex++
			}
		case ai.ThinkingContent:
			if item, ok := signedReasoning(block.ThinkingSignature); ok {
				result = append(result, item)
			}
		case *ai.ThinkingContent:
			if block != nil {
				if item, ok := signedReasoning(block.ThinkingSignature); ok {
					result = append(result, item)
				}
			}
		case ai.ToolCall:
			item, err := responseToolCall(block)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		case *ai.ToolCall:
			if block != nil {
				item, err := responseToolCall(*block)
				if err != nil {
					return nil, err
				}
				result = append(result, item)
			}
		}
	}
	return result, nil
}

func responseText(block ai.TextContent, messageIndex, textIndex int) map[string]any {
	id, phase := parseTextSignature(block.TextSignature)
	if id == "" {
		id = fmt.Sprintf("msg_midas_%d", messageIndex)
		if textIndex > 0 {
			id += fmt.Sprintf("_%d", textIndex)
		}
	}
	item := map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": block.Text, "annotations": []any{}}},
		"status":  "completed",
		"id":      id,
	}
	if phase != "" {
		item["phase"] = phase
	}
	return item
}

func parseTextSignature(signature string) (id, phase string) {
	if signature == "" {
		return "", ""
	}
	var parsed struct {
		Version int    `json:"v"`
		ID      string `json:"id"`
		Phase   string `json:"phase"`
	}
	if strings.HasPrefix(signature, "{") && json.Unmarshal([]byte(signature), &parsed) == nil && parsed.Version == 1 {
		return parsed.ID, parsed.Phase
	}
	return signature, ""
}

func signedReasoning(signature string) (any, bool) {
	if signature == "" {
		return nil, false
	}
	var item any
	if json.Unmarshal([]byte(signature), &item) != nil {
		return nil, false
	}
	return item, true
}

func responseToolCall(call ai.ToolCall) (map[string]any, error) {
	arguments, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("openai: encode arguments for tool %s: %w", call.Name, err)
	}
	callID, itemID, _ := strings.Cut(call.ID, "|")
	item := map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      call.Name,
		"arguments": string(arguments),
	}
	if itemID != "" {
		item["id"] = itemID
	}
	if call.Namespace != "" {
		item["namespace"] = call.Namespace
	}
	return item, nil
}

func convertToolResult(model ai.Model, message ai.ToolResultMessage) map[string]any {
	callID, _, _ := strings.Cut(message.ToolCallID, "|")
	return map[string]any{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  toolResultOutput(model, message.Content),
	}
}

func toolResultOutput(model ai.Model, content []ai.Content) any {
	var text []string
	var images []ai.ImageContent
	for _, part := range content {
		switch part := part.(type) {
		case ai.TextContent:
			text = append(text, part.Text)
		case *ai.TextContent:
			if part != nil {
				text = append(text, part.Text)
			}
		case ai.ImageContent:
			images = append(images, part)
		case *ai.ImageContent:
			if part != nil {
				images = append(images, *part)
			}
		}
	}
	joined := strings.Join(text, "\n")
	if len(images) == 0 || !supportsImages(model) {
		if joined != "" {
			return joined
		}
		if len(images) > 0 {
			return "(see attached image)"
		}
		return "(no tool output)"
	}
	output := make([]any, 0, len(images)+1)
	if joined != "" {
		output = append(output, map[string]any{"type": "input_text", "text": joined})
	}
	for _, image := range images {
		output = append(output, responseImage(image))
	}
	return output
}

func responseImage(image ai.ImageContent) map[string]any {
	return map[string]any{
		"type":      "input_image",
		"detail":    "auto",
		"image_url": "data:" + image.MIMEType + ";base64," + image.Data,
	}
}

func supportsImages(model ai.Model) bool {
	for _, modality := range model.Input {
		if modality == ai.ModalityImage {
			return true
		}
	}
	return false
}

func convertTools(tools []ai.Tool) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		parameters := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Parameters) > 0 {
			if err := json.Unmarshal(tool.Parameters, &parameters); err != nil {
				return nil, fmt.Errorf("openai: invalid schema for tool %s: %w", tool.Name, err)
			}
		}
		result = append(result, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  parameters,
		})
	}
	return result, nil
}
