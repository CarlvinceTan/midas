package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestMessagesTextThinkingUsageAndRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("request = %s key=%q", request.URL.Path, request.Header.Get("X-Api-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "claude-test" || body["stream"] != true {
			t.Errorf("body = %#v", body)
		}
		system := body["system"].([]any)[0].(map[string]any)
		if system["text"] != "Be concise" || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
			t.Errorf("system cache = %#v", system)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"message_start","message":{"id":"msg_1","model":"claude-served","usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`)
		writeSSE(writer, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
		writeSSE(writer, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`)
		writeSSE(writer, `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`)
		writeSSE(writer, `{"type":"content_block_stop","index":0}`)
		writeSSE(writer, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		writeSSE(writer, `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`)
		writeSSE(writer, `{"type":"content_block_stop","index":1}`)
		writeSSE(writer, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"output_tokens_details":{"thinking_tokens":1}}}`)
		writeSSE(writer, `{"type":"message_stop"}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "claude-test", API: "anthropic-messages", Provider: "anthropic", BaseURL: server.URL + "/v1",
	}, ai.Context{SystemPrompt: "Be concise", Messages: []ai.Message{ai.NewUserMessage("hello", testTime)}}, ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopComplete || result.ResponseID != "msg_1" || result.ResponseModel != "claude-served" {
		t.Fatalf("result = %#v", result)
	}
	thinking := result.Content[0].(*ai.ThinkingContent)
	text := result.Content[1].(*ai.TextContent)
	if thinking.Thinking != "reason" || thinking.ThinkingSignature != "sig" || text.Text != "Hello" {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Usage.Input != 10 || result.Usage.Output != 5 || result.Usage.CacheRead != 3 || result.Usage.CacheWrite != 2 || *result.Usage.Reasoning != 1 || result.Usage.TotalTokens != 20 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	want := []ai.EventType{
		ai.EventStart,
		ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd,
		ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd,
		ai.EventDone,
	}
	if fmt.Sprint(eventTypes(events)) != fmt.Sprint(want) {
		t.Fatalf("events = %#v", eventTypes(events))
	}
}

func TestMessagesLongCacheBreakpointsCoverSystemToolsAndConversation(t *testing.T) {
	payload, err := buildRequest(
		ai.Model{ID: "claude", API: "anthropic-messages"},
		ai.Context{
			SystemPrompt: "system",
			Messages:     []ai.Message{ai.NewUserMessage("hello", testTime)},
			Tools:        []ai.Tool{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		},
		ai.StreamOptions{CacheRetention: ai.CacheLong},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantCache := func(value any) {
		t.Helper()
		cache := value.(map[string]any)["cache_control"].(map[string]any)
		if cache["type"] != "ephemeral" || cache["ttl"] != "1h" {
			t.Fatalf("cache control = %#v", cache)
		}
	}
	wantCache(payload["system"].([]any)[0])
	wantCache(payload["tools"].([]any)[0])
	message := payload["messages"].([]any)[0].(map[string]any)
	wantCache(message["content"].([]any)[0])
}

func TestMessagesToolUse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{}}}`)
		writeSSE(writer, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"read","input":{}}}`)
		writeSSE(writer, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
		writeSSE(writer, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"README.md\"}"}}`)
		writeSSE(writer, `{"type":"content_block_stop","index":0}`)
		writeSSE(writer, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`)
		writeSSE(writer, `{"type":"message_stop"}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "claude", API: "anthropic-messages", Provider: "anthropic", BaseURL: server.URL,
	}, ai.Context{}, ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call := result.Content[0].(*ai.ToolCall)
	if result.StopReason != ai.StopToolUse || call.ID != "tool_1" || call.Name != "read" || call.Arguments["path"] != "README.md" {
		t.Fatalf("result = %#v call=%#v", result, call)
	}
}

func TestMessagesHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "secret"})
	_, err := provider.Stream(context.Background(), ai.Model{API: "anthropic-messages", BaseURL: server.URL}, ai.Context{}, ai.StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("error = %v", err)
	}
}

func writeSSE(writer http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func collect(t *testing.T, stream *ai.AssistantStream) []ai.AssistantEvent {
	t.Helper()
	var events []ai.AssistantEvent
	for {
		event, ok, err := stream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return events
		}
		events = append(events, event)
	}
}

func eventTypes(events []ai.AssistantEvent) []ai.EventType {
	result := make([]ai.EventType, len(events))
	for index, event := range events {
		result[index] = event.Type
	}
	return result
}

var testTime = time.Unix(1, 0)
