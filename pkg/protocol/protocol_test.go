package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// repoFile reads a file from the repository root, so the contract tests can
// compare this package with the documents that describe it.
func repoFile(t *testing.T, relative string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", relative))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

func TestRecordsRoundTripThroughJSON(t *testing.T) {
	usage := Usage{Input: 100, Output: 20, CacheRead: 900, CacheWrite: 0, Total: 1020}
	records := []Record{
		RunStarted("run_1", "ses_1", "openai/gpt-6"),
		NewRecord(EventText, func(r *Record) { r.Delta = "hello" }),
		NewRecord(EventToolStart, func(r *Record) { r.Tool = "read"; r.CallID = "call_1" }),
		NewRecord(EventToolEnd, func(r *Record) { r.Tool = "read"; r.CallID = "call_1"; r.IsError = true }),
		NewRecord(EventUsage, func(r *Record) { r.Usage = &usage }),
		NewRecord(EventNotice, func(r *Record) { r.Message = "compacted the context" }),
	}
	var summary Summary
	summary.Add(usage)
	records = append(records, RunFinished("run_1", RunDone, summary, ""))

	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal %s: %v", record.Type, err)
		}
		var decoded Record
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", record.Type, err)
		}
		if decoded.Version != Version || decoded.Type != record.Type {
			t.Fatalf("round trip changed the header: %s", encoded)
		}
		if decoded.Usage != nil && *decoded.Usage != usage {
			t.Fatalf("round trip changed the usage: %s", encoded)
		}
	}
}

func TestUsageHitRateCountsEveryPromptToken(t *testing.T) {
	usage := Usage{Input: 1000, CacheRead: 9000, CacheWrite: 0}
	if got := usage.PromptTokens(); got != 10000 {
		t.Fatalf("prompt tokens = %d", got)
	}
	if got := usage.CacheHitRate(); got != 0.9 {
		t.Fatalf("hit rate = %v", got)
	}
	if empty := (Usage{}); empty.CacheHitRate() != 0 || empty.PromptTokens() != 0 {
		t.Fatal("an empty usage reported a cache hit rate")
	}
}

func TestSummaryAccumulatesTurnsAndRefreshesTheHitRate(t *testing.T) {
	var summary Summary
	summary.Add(Usage{Input: 1000, Output: 10, CacheRead: 0, Total: 1010})
	if summary.Calls != 1 || summary.CacheHitRate != 0 {
		t.Fatalf("first turn = %#v", summary)
	}
	summary.Add(Usage{Input: 100, Output: 20, CacheRead: 900, Total: 1020})
	if summary.Calls != 2 || summary.Input != 1100 || summary.CacheRead != 900 || summary.Output != 30 {
		t.Fatalf("totals = %#v", summary)
	}
	if got := summary.CacheHitRate; got != 900.0/2000.0 {
		t.Fatalf("hit rate = %v", got)
	}
}

func TestFromAgentEventMapsTheContractAndSkipsInternalEvents(t *testing.T) {
	delta := ai.AssistantEvent{Type: ai.EventTextDelta, Delta: "hi"}
	record, ok := FromAgentEvent(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &delta})
	if !ok || record.Type != EventText || record.Delta != "hi" {
		t.Fatalf("text record = %#v ok=%v", record, ok)
	}

	assistant := ai.AssistantMessage{Usage: ai.Usage{Input: 5, Output: 6, CacheRead: 7, TotalTokens: 18}}
	record, ok = FromAgentEvent(agent.Event{Type: agent.EventMessageEnd, Message: assistant})
	if !ok || record.Usage == nil || record.Usage.CacheRead != 7 {
		t.Fatalf("usage record = %#v ok=%v", record, ok)
	}

	if _, ok := FromAgentEvent(agent.Event{Type: agent.EventMessageStart}); ok {
		t.Fatal("an internal event leaked into the contract")
	}
	if _, ok := FromAgentEvent(agent.Event{Type: agent.EventMessageUpdate, AssistantEvent: &ai.AssistantEvent{Type: ai.EventThinkingDelta}}); ok {
		t.Fatal("a thinking delta leaked into the contract")
	}
}

// TestEventSchemaMatchesTheContract keeps api/events.schema.json and this package
// from drifting: every record type the package emits must be described there,
// and the document may not describe a type the package does not emit.
func TestEventSchemaMatchesTheContract(t *testing.T) {
	var schema struct {
		OneOf []struct {
			Ref string `json:"$ref"`
		} `json:"oneOf"`
		Defs map[string]struct {
			Properties struct {
				Type struct {
					Const string `json:"const"`
				} `json:"type"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, "api/events.schema.json")), &schema); err != nil {
		t.Fatalf("parse the schema: %v", err)
	}
	documented := map[string]bool{}
	for _, entry := range schema.OneOf {
		name := entry.Ref[strings.LastIndex(entry.Ref, "/")+1:]
		definition, exists := schema.Defs[name]
		if !exists {
			t.Fatalf("%s is referenced but not defined", name)
		}
		if definition.Properties.Type.Const != "" {
			documented[definition.Properties.Type.Const] = true
		}
	}
	for _, kind := range EventTypes() {
		if !documented[kind] {
			t.Errorf("the schema does not describe record type %q", kind)
		}
		delete(documented, kind)
	}
	for kind := range documented {
		t.Errorf("the schema describes unknown record type %q", kind)
	}
}

// TestOpenAPIDocumentMatchesTheRoutes keeps api/openapi.yaml and ControlRoutes in
// step: a route that is not documented, or documented but not served, fails here.
func TestOpenAPIDocumentMatchesTheRoutes(t *testing.T) {
	document := repoFile(t, "api/openapi.yaml")
	pathLine := regexp.MustCompile(`(?m)^  (/v1[^:]*):\s*$`)

	// Each path's block runs until the next path line, so a method is only
	// credited to the path that declares it.
	lines := strings.Split(document, "\n")
	blocks := map[string]string{}
	current := ""
	for _, line := range lines {
		if match := pathLine.FindStringSubmatch(line); match != nil {
			current = match[1]
		}
		if current != "" {
			blocks[current] += line + "\n"
		}
	}
	documented := map[string]bool{}
	for path := range blocks {
		documented[path] = true
	}
	for _, route := range ControlRoutes() {
		block, exists := blocks[route.Path]
		if !exists {
			t.Errorf("route %s %s is not in api/openapi.yaml", route.Method, route.Path)
			continue
		}
		if !strings.Contains(block, "\n    "+strings.ToLower(route.Method)+":") {
			t.Errorf("method %s for %s is not documented", route.Method, route.Path)
		}
		if !strings.Contains(block, "operationId: "+route.OperationID) {
			t.Errorf("operation %s is not documented", route.OperationID)
		}
	}
	served := map[string]bool{}
	for _, route := range ControlRoutes() {
		served[route.Path] = true
	}
	for path := range documented {
		if !served[path] {
			t.Errorf("api/openapi.yaml documents %s, which ControlRoutes does not serve", path)
		}
	}
}

func TestRequestsValidateWhatTheyRequire(t *testing.T) {
	if err := (RunRequest{Prompt: "do the thing"}).Validate(); err != nil {
		t.Fatalf("valid run request rejected: %v", err)
	}
	if err := (RunRequest{Prompt: "   "}).Validate(); err == nil {
		t.Fatal("a blank prompt was accepted")
	}
	if err := (SteerRequest{Text: "hint"}).Validate(); err != nil {
		t.Fatalf("valid steer request rejected: %v", err)
	}
	if err := (SteerRequest{}).Validate(); err == nil {
		t.Fatal("a blank steer was accepted")
	}
	if _, ok := any(Error{Code: "not_found", Message: "no such run"}).(error); !ok {
		t.Fatal("protocol.Error does not implement error")
	}
	if info := (RunInfo{ID: "run_1", Status: RunRunning, StartedAt: time.Now()}); info.Status != RunRunning {
		t.Fatal("run info lost its status")
	}
}
