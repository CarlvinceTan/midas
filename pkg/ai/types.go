// Package ai defines provider-neutral language-model types and streaming
// contracts. Provider adapters belong in subpackages so the agent can depend on
// this package without importing a provider SDK.
package ai

import (
	"encoding/json"
	"fmt"
	"time"
)

// Role identifies the author of a transcript message.
type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// Content is one item in a message. The closed interface makes invalid content
// kinds a compile-time error while still allowing normal JSON marshaling.
type Content interface {
	content()
}

type TextContent struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
}

func NewText(text string) TextContent {
	return TextContent{Type: "text", Text: text}
}

func (TextContent) content() {}

type ThinkingContent struct {
	Type              string `json:"type"`
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}

func NewThinking(thinking string) ThinkingContent {
	return ThinkingContent{Type: "thinking", Thinking: thinking}
}

func (ThinkingContent) content() {}

type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MIMEType string `json:"mimeType"`
}

func NewImage(data, mimeType string) ImageContent {
	return ImageContent{Type: "image", Data: data, MIMEType: mimeType}
}

func (ImageContent) content() {}

type ToolCall struct {
	Type             string         `json:"type"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
	Namespace        string         `json:"namespace,omitempty"`
}

func NewToolCall(id, name string, arguments map[string]any) ToolCall {
	return ToolCall{Type: "toolCall", ID: id, Name: name, Arguments: arguments}
}

func (ToolCall) content() {}

// UserContent is either the provider-neutral shorthand string or a list of
// text/image blocks. Its JSON representation matches that union directly.
type UserContent struct {
	text  *string
	parts []Content
}

func TextUserContent(text string) UserContent {
	return UserContent{text: &text}
}

func MultipartUserContent(parts ...Content) (UserContent, error) {
	for _, part := range parts {
		switch part.(type) {
		case TextContent, ImageContent:
		default:
			return UserContent{}, fmt.Errorf("ai: user content cannot contain %T", part)
		}
	}
	return UserContent{parts: append([]Content(nil), parts...)}, nil
}

func (c UserContent) IsText() bool { return c.text != nil }

func (c UserContent) Text() (string, bool) {
	if c.text == nil {
		return "", false
	}
	return *c.text, true
}

func (c UserContent) Parts() []Content {
	return append([]Content(nil), c.parts...)
}

func (c UserContent) MarshalJSON() ([]byte, error) {
	if c.text != nil {
		return json.Marshal(*c.text)
	}
	if c.parts == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(c.parts)
}

// Message is one item in the provider-neutral conversation transcript.
type Message interface {
	message()
}

type UserMessage struct {
	Role      Role          `json:"role"`
	Content   UserContent   `json:"content"`
	Timestamp int64         `json:"timestamp"`
	Agent     string        `json:"agent,omitempty"`
	Thinking  ThinkingLevel `json:"thinking,omitempty"`
	// Synthetic marks a message Midas authored for the model rather than the
	// user, such as a compaction checkpoint. Synthetic messages are not prompts:
	// undo, prompt detection, and the transcript treat them as context.
	Synthetic bool `json:"synthetic,omitempty"`
}

func NewUserMessage(text string, at time.Time) UserMessage {
	return UserMessage{Role: RoleUser, Content: TextUserContent(text), Timestamp: UnixMillis(at)}
}

// NewSyntheticMessage builds a context message Midas wrote for the model.
func NewSyntheticMessage(text string, at time.Time) UserMessage {
	message := NewUserMessage(text, at)
	message.Synthetic = true
	return message
}

func (UserMessage) message() {}

type AssistantMessage struct {
	Role                  Role            `json:"role"`
	Content               []Content       `json:"content"`
	API                   string          `json:"api"`
	Provider              string          `json:"provider"`
	Model                 string          `json:"model"`
	ResponseModel         string          `json:"responseModel,omitempty"`
	ResponseID            string          `json:"responseId,omitempty"`
	ProviderThinkingLevel string          `json:"providerThinkingLevel,omitempty"`
	Diagnostics           []Diagnostic    `json:"diagnostics,omitempty"`
	Usage                 Usage           `json:"usage"`
	StopReason            StopReason      `json:"stopReason"`
	Deferred              *DeferredHandle `json:"deferred,omitempty"`
	ErrorMessage          string          `json:"errorMessage,omitempty"`
	RawStopReason         string          `json:"rawStopReason,omitempty"`
	EndTurn               *bool           `json:"endTurn,omitempty"`
	Timestamp             int64           `json:"timestamp"`
}

func (AssistantMessage) message() {}

type ToolResultMessage struct {
	Role           Role           `json:"role"`
	ToolCallID     string         `json:"toolCallId"`
	ToolName       string         `json:"toolName"`
	Content        []Content      `json:"content"`
	Details        map[string]any `json:"details,omitempty"`
	Usage          *Usage         `json:"usage,omitempty"`
	AddedToolNames []string       `json:"addedToolNames,omitempty"`
	IsError        bool           `json:"isError"`
	Timestamp      int64          `json:"timestamp"`
}

func (ToolResultMessage) message() {}

// Context is the complete provider-neutral input for one model turn.
type Context struct {
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []Tool    `json:"tools,omitempty"`
}

// Tool describes a callable function. Parameters must contain a JSON Schema.
type Tool struct {
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	Parameters          json.RawMessage `json:"parameters"`
	ConstrainedSampling any             `json:"constrainedSampling,omitempty"`
}

type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type DeferredHandle struct {
	Provider    string         `json:"provider"`
	ModelID     string         `json:"modelId"`
	API         string         `json:"api"`
	ID          string         `json:"id"`
	ExpiresAt   int64          `json:"expiresAt,omitempty"`
	PollAfterMS int64          `json:"pollAfterMs,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

func UnixMillis(t time.Time) int64 { return t.UnixNano() / int64(time.Millisecond) }

// CloneAssistantMessage returns a deep-enough copy for an immutable stream
// snapshot. JSON-like argument and diagnostic values are recursively copied.
func CloneAssistantMessage(message AssistantMessage) AssistantMessage {
	clone := message
	clone.Content = make([]Content, 0, len(message.Content))
	for _, block := range message.Content {
		switch value := block.(type) {
		case TextContent:
			clone.Content = append(clone.Content, value)
		case *TextContent:
			if value != nil {
				copy := *value
				clone.Content = append(clone.Content, &copy)
			}
		case ThinkingContent:
			clone.Content = append(clone.Content, value)
		case *ThinkingContent:
			if value != nil {
				copy := *value
				clone.Content = append(clone.Content, &copy)
			}
		case ImageContent:
			clone.Content = append(clone.Content, value)
		case *ImageContent:
			if value != nil {
				copy := *value
				clone.Content = append(clone.Content, &copy)
			}
		case ToolCall:
			value.Arguments = cloneMap(value.Arguments)
			clone.Content = append(clone.Content, value)
		case *ToolCall:
			if value != nil {
				copy := *value
				copy.Arguments = cloneMap(value.Arguments)
				clone.Content = append(clone.Content, &copy)
			}
		}
	}
	clone.Diagnostics = append([]Diagnostic(nil), message.Diagnostics...)
	if message.Deferred != nil {
		deferred := *message.Deferred
		deferred.Data = cloneMap(message.Deferred.Data)
		clone.Deferred = &deferred
	}
	return clone
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneJSONValue(value)
	}
	return output
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		clone := make([]any, len(value))
		for index := range value {
			clone[index] = cloneJSONValue(value[index])
		}
		return clone
	default:
		return value
	}
}
