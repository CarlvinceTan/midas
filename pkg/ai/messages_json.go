package ai

import (
	"encoding/json"
	"fmt"
)

// MarshalMessages serializes provider-neutral history without adding a
// provider-specific envelope.
func MarshalMessages(messages []Message) ([]byte, error) { return json.Marshal(messages) }

// UnmarshalMessages restores the closed Message and Content interfaces from
// their role/type discriminators.
func UnmarshalMessages(data []byte) ([]Message, error) {
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	result := make([]Message, 0, len(records))
	for index, record := range records {
		var header struct {
			Role Role `json:"role"`
		}
		if err := json.Unmarshal(record, &header); err != nil {
			return nil, fmt.Errorf("ai: message %d: %w", index, err)
		}
		switch header.Role {
		case RoleUser:
			var wire struct {
				Role      Role            `json:"role"`
				Content   json.RawMessage `json:"content"`
				Timestamp int64           `json:"timestamp"`
				Agent     string          `json:"agent,omitempty"`
				Thinking  ThinkingLevel   `json:"thinking,omitempty"`
				// Synthetic is carried across a round trip: dropping it would turn a
				// context message Midas wrote for the model into a user prompt on
				// the next resume.
				Synthetic bool `json:"synthetic,omitempty"`
			}
			if err := json.Unmarshal(record, &wire); err != nil {
				return nil, err
			}
			var text string
			if json.Unmarshal(wire.Content, &text) == nil {
				result = append(result, UserMessage{Role: RoleUser, Content: TextUserContent(text), Timestamp: wire.Timestamp, Agent: wire.Agent, Thinking: wire.Thinking, Synthetic: wire.Synthetic})
				continue
			}
			parts, err := unmarshalContent(wire.Content)
			if err != nil {
				return nil, fmt.Errorf("ai: user message %d: %w", index, err)
			}
			content, err := MultipartUserContent(parts...)
			if err != nil {
				return nil, err
			}
			result = append(result, UserMessage{Role: RoleUser, Content: content, Timestamp: wire.Timestamp, Agent: wire.Agent, Thinking: wire.Thinking, Synthetic: wire.Synthetic})
		case RoleAssistant:
			var value AssistantMessage
			parts, scalar, err := splitMessageContent(record)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(scalar, &value); err != nil {
				return nil, err
			}
			value.Content, err = unmarshalContent(parts)
			if err != nil {
				return nil, fmt.Errorf("ai: assistant message %d: %w", index, err)
			}
			result = append(result, value)
		case RoleToolResult:
			var value ToolResultMessage
			parts, scalar, err := splitMessageContent(record)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(scalar, &value); err != nil {
				return nil, err
			}
			value.Content, err = unmarshalContent(parts)
			if err != nil {
				return nil, fmt.Errorf("ai: tool message %d: %w", index, err)
			}
			result = append(result, value)
		default:
			return nil, fmt.Errorf("ai: message %d has invalid role %q", index, header.Role)
		}
	}
	return result, nil
}

func splitMessageContent(record json.RawMessage) (json.RawMessage, []byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(record, &object); err != nil {
		return nil, nil, err
	}
	parts := object["content"]
	object["content"] = json.RawMessage("[]")
	scalar, err := json.Marshal(object)
	return parts, scalar, err
}

func unmarshalContent(data []byte) ([]Content, error) {
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	result := make([]Content, 0, len(records))
	for index, record := range records {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(record, &header); err != nil {
			return nil, err
		}
		switch header.Type {
		case "text":
			var value TextContent
			if err := json.Unmarshal(record, &value); err != nil {
				return nil, err
			}
			result = append(result, value)
		case "thinking":
			var value ThinkingContent
			if err := json.Unmarshal(record, &value); err != nil {
				return nil, err
			}
			result = append(result, value)
		case "image":
			var value ImageContent
			if err := json.Unmarshal(record, &value); err != nil {
				return nil, err
			}
			result = append(result, value)
		case "toolCall":
			var value ToolCall
			if err := json.Unmarshal(record, &value); err != nil {
				return nil, err
			}
			result = append(result, value)
		default:
			return nil, fmt.Errorf("ai: content %d has invalid type %q", index, header.Type)
		}
	}
	return result, nil
}
