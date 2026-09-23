package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func buildRequest(model ai.Model, context ai.Context, options ai.StreamOptions) (map[string]any, error) {
	contents, err := convertMessages(model, context.Messages)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"contents": contents}
	if context.SystemPrompt != "" {
		payload["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": context.SystemPrompt}}}
	}
	generation := make(map[string]any)
	if options.Temperature != nil {
		generation["temperature"] = *options.Temperature
	}
	if options.MaxTokens > 0 {
		generation["maxOutputTokens"] = options.MaxTokens
	}
	if model.Reasoning {
		generation["thinkingConfig"] = googleThinkingConfig(model, options.Reasoning)
	}
	for key, value := range model.SamplingParams {
		generation[key] = value
	}
	for key, value := range options.SamplingParams {
		generation[key] = value
	}
	if len(generation) > 0 {
		payload["generationConfig"] = generation
	}
	if len(context.Tools) > 0 {
		tools, err := convertTools(context.Tools)
		if err != nil {
			return nil, err
		}
		payload["tools"] = tools
	}
	return payload, nil
}

func googleThinkingConfig(model ai.Model, level ai.ThinkingLevel) map[string]any {
	id := strings.ToLower(model.ID)
	if level == "" || level == ai.ThinkingOff {
		switch {
		case regexp.MustCompile(`gemini-3(?:\.\d+)?-pro`).MatchString(id):
			return map[string]any{"thinkingLevel": "LOW"}
		case strings.Contains(id, "gemini-3") || strings.Contains(id, "gemma-4"):
			return map[string]any{"thinkingLevel": "MINIMAL"}
		default:
			return map[string]any{"thinkingBudget": 0}
		}
	}
	resolved := strings.ToUpper(string(level))
	if mapped := model.ThinkingLevelMap[level]; mapped != nil {
		resolved = strings.ToUpper(*mapped)
	}
	if strings.Contains(id, "gemini-3") || strings.Contains(id, "gemma-4") {
		return map[string]any{"includeThoughts": true, "thinkingLevel": resolved}
	}
	budget := -1
	switch level {
	case ai.ThinkingMinimal:
		budget = 128
	case ai.ThinkingLow:
		budget = 2048
	case ai.ThinkingMedium:
		budget = 8192
	case ai.ThinkingHigh, ai.ThinkingXHigh, ai.ThinkingMax:
		budget = 32768
	}
	return map[string]any{"includeThoughts": true, "thinkingBudget": budget}
}

func convertMessages(model ai.Model, messages []ai.Message) ([]any, error) {
	contents := make([]any, 0, len(messages))
	for _, message := range messages {
		var content map[string]any
		switch message := message.(type) {
		case ai.UserMessage:
			content = googleUser(message)
		case *ai.UserMessage:
			if message != nil {
				content = googleUser(*message)
			}
		case ai.AssistantMessage:
			content = googleAssistant(model, message)
		case *ai.AssistantMessage:
			if message != nil {
				content = googleAssistant(model, *message)
			}
		case ai.ToolResultMessage:
			content = googleToolResult(model, message)
		case *ai.ToolResultMessage:
			if message != nil {
				content = googleToolResult(model, *message)
			}
		}
		if content == nil || len(content["parts"].([]any)) == 0 {
			continue
		}
		if content["role"] == "user" && len(contents) > 0 {
			last := contents[len(contents)-1].(map[string]any)
			if last["role"] == "user" && containsFunctionResponse(last["parts"].([]any)) && containsFunctionResponse(content["parts"].([]any)) {
				last["parts"] = append(last["parts"].([]any), content["parts"].([]any)...)
				continue
			}
		}
		contents = append(contents, content)
	}
	return contents, nil
}

func googleUser(message ai.UserMessage) map[string]any {
	parts := make([]any, 0)
	if text, ok := message.Content.Text(); ok {
		parts = append(parts, map[string]any{"text": text})
	} else {
		for _, part := range message.Content.Parts() {
			switch part := part.(type) {
			case ai.TextContent:
				parts = append(parts, map[string]any{"text": part.Text})
			case *ai.TextContent:
				if part != nil {
					parts = append(parts, map[string]any{"text": part.Text})
				}
			case ai.ImageContent:
				parts = append(parts, googleImage(part))
			case *ai.ImageContent:
				if part != nil {
					parts = append(parts, googleImage(*part))
				}
			}
		}
	}
	return map[string]any{"role": "user", "parts": parts}
}

func googleAssistant(model ai.Model, message ai.AssistantMessage) map[string]any {
	parts := make([]any, 0, len(message.Content))
	sameModel := message.Provider == model.Provider && message.Model == model.ID
	for _, block := range message.Content {
		switch block := block.(type) {
		case ai.TextContent:
			if part := googleTextPart(block, sameModel); part != nil {
				parts = append(parts, part)
			}
		case *ai.TextContent:
			if block != nil {
				if part := googleTextPart(*block, sameModel); part != nil {
					parts = append(parts, part)
				}
			}
		case ai.ThinkingContent:
			if part := googleThinkingPart(block, sameModel); part != nil {
				parts = append(parts, part)
			}
		case *ai.ThinkingContent:
			if block != nil {
				if part := googleThinkingPart(*block, sameModel); part != nil {
					parts = append(parts, part)
				}
			}
		case ai.ToolCall:
			parts = append(parts, googleFunctionCall(model, block, sameModel))
		case *ai.ToolCall:
			if block != nil {
				parts = append(parts, googleFunctionCall(model, *block, sameModel))
			}
		}
	}
	return map[string]any{"role": "model", "parts": parts}
}

func googleTextPart(block ai.TextContent, sameModel bool) map[string]any {
	part := map[string]any{"text": block.Text}
	if sameModel && validSignature(block.TextSignature) {
		part["thoughtSignature"] = block.TextSignature
	}
	if strings.TrimSpace(block.Text) == "" && part["thoughtSignature"] == nil {
		return nil
	}
	return part
}

func googleThinkingPart(block ai.ThinkingContent, sameModel bool) map[string]any {
	if !sameModel {
		if strings.TrimSpace(block.Thinking) == "" {
			return nil
		}
		return map[string]any{"text": block.Thinking}
	}
	part := map[string]any{"thought": true, "text": block.Thinking}
	if validSignature(block.ThinkingSignature) {
		part["thoughtSignature"] = block.ThinkingSignature
	}
	if strings.TrimSpace(block.Thinking) == "" && part["thoughtSignature"] == nil {
		return nil
	}
	return part
}

func googleFunctionCall(model ai.Model, call ai.ToolCall, sameModel bool) map[string]any {
	functionCall := map[string]any{"name": call.Name, "args": call.Arguments}
	if requiresToolCallID(model.ID) {
		functionCall["id"] = normalizeToolCallID(call.ID)
	}
	part := map[string]any{"functionCall": functionCall}
	if sameModel && validSignature(call.ThoughtSignature) {
		part["thoughtSignature"] = call.ThoughtSignature
	}
	return part
}

func googleToolResult(model ai.Model, message ai.ToolResultMessage) map[string]any {
	var text []string
	var images []ai.ImageContent
	for _, part := range message.Content {
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
	textResult := strings.Join(text, "\n")
	if textResult == "" && len(images) > 0 {
		textResult = "(see attached image)"
	}
	key := "output"
	if message.IsError {
		key = "error"
	}
	response := map[string]any{"name": message.ToolName, "response": map[string]any{key: textResult}}
	if requiresToolCallID(model.ID) {
		response["id"] = normalizeToolCallID(message.ToolCallID)
	}
	return map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": response}}}
}

func containsFunctionResponse(parts []any) bool {
	for _, raw := range parts {
		if part, ok := raw.(map[string]any); ok && part["functionResponse"] != nil {
			return true
		}
	}
	return false
}

func googleImage(image ai.ImageContent) map[string]any {
	return map[string]any{"inlineData": map[string]any{"mimeType": image.MIMEType, "data": image.Data}}
}

func convertTools(tools []ai.Tool) ([]any, error) {
	declarations := make([]any, 0, len(tools))
	for _, tool := range tools {
		schema := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Parameters) > 0 {
			if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("google: invalid schema for tool %s: %w", tool.Name, err)
			}
		}
		declarations = append(declarations, map[string]any{
			"name": tool.Name, "description": tool.Description, "parametersJsonSchema": schema,
		})
	}
	return []any{map[string]any{"functionDeclarations": declarations}}, nil
}

func requiresToolCallID(modelID string) bool {
	id := strings.ToLower(modelID)
	if strings.HasPrefix(id, "claude-") || strings.HasPrefix(id, "gpt-oss-") {
		return true
	}
	var major int
	_, _ = fmt.Sscanf(id, "gemini-%d", &major)
	return major >= 3
}

func normalizeToolCallID(id string) string {
	var output strings.Builder
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			output.WriteRune(character)
		} else {
			output.WriteByte('_')
		}
		if output.Len() == 64 {
			break
		}
	}
	return output.String()
}

func validSignature(signature string) bool {
	if signature == "" || len(signature)%4 != 0 {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(signature)
	return err == nil
}
