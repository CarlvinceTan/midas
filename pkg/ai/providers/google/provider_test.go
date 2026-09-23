package google

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

func TestGeminiTextThinkingUsageAndRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || request.URL.Query().Get("alt") != "sse" {
			t.Errorf("URL = %s", request.URL)
		}
		if request.Header.Get("X-Goog-Api-Key") != "secret" {
			t.Errorf("key = %q", request.Header.Get("X-Goog-Api-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["systemInstruction"] == nil || body["contents"] == nil {
			t.Errorf("body = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"reason","thoughtSignature":"c2ln"}]}}]}`)
		writeSSE(writer, `{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":2,"cachedContentTokenCount":3,"totalTokenCount":16}}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "gemini-test", API: "google-generative-ai", Provider: "google", BaseURL: server.URL + "/v1beta",
	}, ai.Context{SystemPrompt: "Be concise", Messages: []ai.Message{ai.NewUserMessage("hello", time.Unix(1, 0))}}, ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopComplete || result.ResponseID != "resp_1" {
		t.Fatalf("result = %#v", result)
	}
	thinking := result.Content[0].(*ai.ThinkingContent)
	text := result.Content[1].(*ai.TextContent)
	if thinking.Thinking != "reason" || thinking.ThinkingSignature != "c2ln" || text.Text != "Hello" {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Usage.Input != 7 || result.Usage.Output != 6 || result.Usage.CacheRead != 3 || *result.Usage.Reasoning != 2 {
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

func TestGeminiToolCall(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writeSSE(writer, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"read","args":{"path":"README.md"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()

	provider := New(Options{APIKey: "secret"})
	stream, err := provider.Stream(context.Background(), ai.Model{
		ID: "gemini-3-pro", API: "google-generative-ai", Provider: "google", BaseURL: server.URL,
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
	if result.StopReason != ai.StopToolUse || call.ID != "call_1" || call.Name != "read" || call.Arguments["path"] != "README.md" || call.ThoughtSignature != "c2ln" {
		t.Fatalf("result = %#v call=%#v", result, call)
	}
}

func TestGeminiHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	provider := New(Options{APIKey: "wrong"})
	_, err := provider.Stream(context.Background(), ai.Model{
		ID: "gemini-test", API: "google-generative-ai", BaseURL: server.URL,
	}, ai.Context{}, ai.StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error = %v", err)
	}
}

func TestGeminiReplaysOnlyValidSameModelSignature(t *testing.T) {
	t.Parallel()

	message := ai.AssistantMessage{
		Role: ai.RoleAssistant, Provider: "google", Model: "gemini-test",
		Content: []ai.Content{ai.TextContent{Type: "text", Text: "answer", TextSignature: "c2ln"}},
	}
	contents, err := convertMessages(ai.Model{Provider: "google", ID: "gemini-test"}, []ai.Message{message})
	if err != nil {
		t.Fatal(err)
	}
	part := contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if part["thoughtSignature"] != "c2ln" {
		t.Fatalf("part = %#v", part)
	}
	contents, err = convertMessages(ai.Model{Provider: "google", ID: "other"}, []ai.Message{message})
	if err != nil {
		t.Fatal(err)
	}
	part = contents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if part["thoughtSignature"] != nil {
		t.Fatalf("cross-model signature retained: %#v", part)
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
