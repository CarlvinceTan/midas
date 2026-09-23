package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

var testTime = time.Unix(1, 0)

func TestResponsesTextStreamAndRequestConversion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "gpt-test" || body["stream"] != true || body["store"] != false {
			t.Errorf("body = %#v", body)
		}
		if body["max_output_tokens"] != float64(16) {
			t.Errorf("max_output_tokens = %#v", body["max_output_tokens"])
		}
		input := body["input"].([]any)
		if input[0].(map[string]any)["role"] != "system" || input[1].(map[string]any)["role"] != "user" {
			t.Errorf("input = %#v", input)
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.created","response":{"id":"resp_1"}}`)
		writeSSE(writer, `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`)
		writeSSE(writer, `{"type":"response.output_text.delta","output_index":0,"delta":"Hel"}`)
		writeSSE(writer, `{"type":"response.output_text.delta","output_index":0,"delta":"lo"}`)
		writeSSE(writer, `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`)
		writeSSE(writer, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`)
		writeSSE(writer, `[DONE]`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	model := ai.Model{
		ID: "gpt-test", API: "openai-responses", Provider: "openai", BaseURL: server.URL + "/v1",
		Input: []ai.Modality{ai.ModalityText}, Cost: ai.ModelCost{Input: 1, Output: 2, CacheRead: .1},
	}
	stream, err := provider.Stream(context.Background(), model, ai.Context{
		SystemPrompt: "You are concise.",
		Messages:     []ai.Message{ai.NewUserMessage("Hello", testTime)},
	}, ai.StreamOptions{MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	want := []ai.EventType{ai.EventStart, ai.EventTextStart, ai.EventTextDelta, ai.EventTextDelta, ai.EventTextEnd, ai.EventDone}
	if fmt.Sprint(eventTypes(events)) != fmt.Sprint(want) {
		t.Fatalf("events = %#v, want %#v", eventTypes(events), want)
	}
	if events[2].Partial.Content[0].(*ai.TextContent).Text != "Hel" {
		t.Fatalf("first delta snapshot = %#v", events[2].Partial)
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*ai.TextContent)
	if text.Text != "Hello" || result.ResponseID != "resp_1" || result.StopReason != ai.StopComplete {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage.Input != 7 || result.Usage.CacheRead != 3 || result.Usage.Output != 4 || *result.Usage.Reasoning != 2 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestResponsesPromptCacheAffinityAndCurrentLongTTL(t *testing.T) {
	longSession := strings.Repeat("界", 70)
	payload, err := buildRequest(
		ai.Model{ID: "gpt-5.6-sol", API: "openai-responses"},
		ai.Context{Messages: []ai.Message{ai.NewUserMessage("hello", testTime)}},
		ai.StreamOptions{SessionID: longSession, CacheRetention: ai.CacheLong},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(payload["prompt_cache_key"].(string))); got != 64 {
		t.Fatalf("prompt cache key length = %d", got)
	}
	options := payload["prompt_cache_options"].(map[string]any)
	if options["ttl"] != "30m" || payload["prompt_cache_retention"] != nil {
		t.Fatalf("long cache fields = %#v", payload)
	}
}

func TestResponsesToolCall(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}`)
		writeSSE(writer, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\""}`)
		writeSSE(writer, `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"README.md\"}"}`)
		writeSSE(writer, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"README.md\"}"}}`)
		writeSSE(writer, `{"type":"response.completed","response":{"status":"completed"}}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "gpt-test", API: "openai-responses", Provider: "openai", BaseURL: server.URL,
	}, ai.Context{}, ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopToolUse {
		t.Fatalf("stop reason = %s", result.StopReason)
	}
	call := result.Content[0].(*ai.ToolCall)
	if call.ID != "call_1|fc_1" || call.Arguments["path"] != "README.md" {
		t.Fatalf("call = %#v", call)
	}
	want := []ai.EventType{ai.EventStart, ai.EventToolCallStart, ai.EventToolCallDelta, ai.EventToolCallDelta, ai.EventToolCallEnd, ai.EventDone}
	if fmt.Sprint(eventTypes(events)) != fmt.Sprint(want) {
		t.Fatalf("events = %#v", eventTypes(events))
	}
}

func TestResponsesHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "wrong"})
	_, err := provider.Stream(context.Background(), ai.Model{API: "openai-responses", BaseURL: server.URL}, ai.Context{}, ai.StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error = %v", err)
	}
}

func TestResponsesRequiresCredentials(t *testing.T) {
	t.Parallel()

	provider := New(Options{})
	_, err := provider.Stream(context.Background(), ai.Model{API: "openai-responses", Provider: "openai"}, ai.Context{}, ai.StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompatibleProviderCanStreamWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization = %q", got)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"local\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	provider := New(Options{ID: "local", BaseURL: server.URL, DefaultAPI: "openai-completions", AllowNoAPIKey: true})
	model := ai.Model{ID: "local-model", Provider: "local", API: "openai-completions", BaseURL: server.URL}
	stream, err := provider.Stream(context.Background(), model, ai.Context{}, ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := stream.Result(context.Background())
	if err != nil || assistantText(message) != "local" {
		t.Fatalf("result = %#v, %v", message, err)
	}
}

func assistantText(message ai.AssistantMessage) string {
	var result strings.Builder
	for _, part := range message.Content {
		switch text := part.(type) {
		case ai.TextContent:
			result.WriteString(text.Text)
		case *ai.TextContent:
			if text != nil {
				result.WriteString(text.Text)
			}
		}
	}
	return result.String()
}

func TestCompletionsTextReasoningAndUsage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != true || body["max_completion_tokens"] != float64(32) {
			t.Errorf("body = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"id":"chat_1","model":"served-model","choices":[{"delta":{"reasoning_content":"think"},"finish_reason":null}]}`)
		writeSSE(writer, `{"id":"chat_1","model":"served-model","choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`)
		writeSSE(writer, `{"id":"chat_1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(writer, `{"id":"chat_1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`)
		writeSSE(writer, `[DONE]`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "requested", API: "openai-completions", Provider: "compatible", BaseURL: server.URL + "/v1",
	}, ai.Context{Messages: []ai.Message{ai.NewUserMessage("hello", testTime)}}, ai.StreamOptions{MaxTokens: 32})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopComplete || result.ResponseID != "chat_1" || result.ResponseModel != "served-model" {
		t.Fatalf("result = %#v", result)
	}
	if result.Content[0].(*ai.ThinkingContent).Thinking != "think" || result.Content[1].(*ai.TextContent).Text != "Hello" {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Usage.Input != 10 || result.Usage.CacheRead != 2 || result.Usage.Output != 6 || *result.Usage.Reasoning != 1 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	want := []ai.EventType{
		ai.EventStart,
		ai.EventThinkingStart, ai.EventThinkingDelta,
		ai.EventTextStart, ai.EventTextDelta,
		ai.EventThinkingEnd, ai.EventTextEnd, ai.EventDone,
	}
	if fmt.Sprint(eventTypes(events)) != fmt.Sprint(want) {
		t.Fatalf("events = %#v", eventTypes(events))
	}
}

func TestCompletionsToolCall(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"id":"chat_1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`)
		writeSSE(writer, `{"id":"chat_1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":null}]}`)
		writeSSE(writer, `{"id":"chat_1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "test", API: "openai-completions", Provider: "compatible", BaseURL: server.URL,
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
	if result.StopReason != ai.StopToolUse || call.ID != "call_1" || call.Name != "read" || call.Arguments["path"] != "README.md" {
		t.Fatalf("result = %#v call=%#v", result, call)
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
