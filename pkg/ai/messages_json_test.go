package ai

import (
	"reflect"
	"testing"
	"time"
)

func TestMessagesJSONRoundTrip(t *testing.T) {
	multipart, err := MultipartUserContent(NewText("look"), NewImage("abc", "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	messages := []Message{
		UserMessage{Role: RoleUser, Content: multipart, Timestamp: 1, Agent: "orchestrator", Thinking: ThinkingHigh},
		// Synthetic marks a message Midas wrote for the model; losing it on a
		// round trip would turn a checkpoint into an ordinary user prompt.
		NewSyntheticMessage("compaction checkpoint", time.UnixMilli(4)),
		AssistantMessage{Role: RoleAssistant, Content: []Content{NewThinking("hmm"), NewText("answer"), NewToolCall("call-1", "read", map[string]any{"path": "a.go"})}, Provider: "fake", Model: "m", StopReason: StopComplete, Timestamp: 2},
		ToolResultMessage{Role: RoleToolResult, ToolCallID: "call-1", ToolName: "read", Content: []Content{NewText("body")}, Details: map[string]any{"ok": true}, Timestamp: 3},
	}
	data, err := MarshalMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalMessages(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, messages) {
		t.Fatalf("round trip:\n got %#v\nwant %#v", restored, messages)
	}
}
