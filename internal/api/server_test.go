package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
	"github.com/CarlvinceTan/midas/pkg/protocol"
)

// scriptedProvider streams a fixed reply, so the API can be tested without a
// network round trip.
type scriptedProvider struct {
	partial string
}

func (scriptedProvider) ID() string                                   { return "scripted" }
func (scriptedProvider) Models(_ context.Context) ([]ai.Model, error) { return nil, nil }

func (p scriptedProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	stream := ai.NewAssistantStream()
	partial := ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	accumulated := ""
	for _, delta := range strings.SplitAfter(p.partial, " ") {
		if delta == "" {
			continue
		}
		accumulated += delta
		partial := ai.AssistantMessage{
			Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(accumulated)}, StopReason: ai.StopPending,
		}
		stream.Push(ai.AssistantEvent{Type: ai.EventTextDelta, Delta: delta, Partial: &partial})
	}
	message := ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(p.partial)}, StopReason: ai.StopComplete,
		Usage: ai.Usage{Input: 100, Output: 20, CacheRead: 900, TotalTokens: 1020},
	}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	return stream, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(Config{
		Root: t.TempDir(), Provider: scriptedProvider{partial: "one two"},
		Model: ai.Model{ID: "scripted-model", Provider: "scripted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// TestRoutesMatchTheContract keeps the mux and pkg/protocol in step: a route the
// protocol advertises must be served here, and nothing else may be registered.
func TestRoutesMatchTheContract(t *testing.T) {
	served := map[string]bool{}
	for _, route := range Routes() {
		served[route.Method+" "+route.Path] = true
	}
	declared := map[string]bool{}
	for _, route := range protocol.ControlRoutes() {
		declared[route.Method+" "+route.Path] = true
		if !served[route.Method+" "+route.Path] {
			t.Errorf("protocol advertises %s %s, which the server does not serve", route.Method, route.Path)
		}
	}
	for key := range served {
		if !declared[key] {
			t.Errorf("the server serves %s, which pkg/protocol does not advertise", key)
		}
	}
	if len(Routes()) != len(protocol.ControlRoutes()) {
		t.Fatalf("route counts differ: %d served, %d declared", len(Routes()), len(protocol.ControlRoutes()))
	}
}

func TestHealthReportsTheProtocolVersion(t *testing.T) {
	server := newTestServer(t)
	response := request(t, server, http.MethodGet, "/v1/health", "")
	if response.Code != http.StatusOK {
		t.Fatalf("health = %d", response.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["version"] != protocol.Version {
		t.Fatalf("health body = %#v", body)
	}
}

func TestRunLifecycleStreamsProtocolRecords(t *testing.T) {
	server := newTestServer(t)
	response := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"say hi","sessionID":"ses_api"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start run = %d: %s", response.Code, response.Body)
	}
	var started protocol.RunInfo
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.Status != protocol.RunRunning || started.SessionID != "ses_api" {
		t.Fatalf("run info = %#v", started)
	}

	records := streamRecords(t, server, started.ID)
	if len(records) == 0 || records[0].Type != protocol.EventRunStarted {
		t.Fatalf("stream did not start with run_started: %#v", records)
	}
	last := records[len(records)-1]
	if last.Type != protocol.EventRunFinished || last.Status != protocol.RunDone {
		t.Fatalf("stream did not finish cleanly: %#v", last)
	}
	if last.Summary == nil || last.Summary.Calls != 1 || last.Summary.CacheRead != 900 {
		t.Fatalf("summary = %#v", last.Summary)
	}
	if got := last.Summary.CacheHitRate; got != 0.9 {
		t.Fatalf("cache hit rate = %v", got)
	}

	text := ""
	for _, record := range records {
		if record.Type == protocol.EventText {
			text += record.Delta
		}
	}
	if text != "one two" {
		t.Fatalf("streamed text = %q", text)
	}

	// The run stays addressable after it finishes.
	described := request(t, server, http.MethodGet, "/v1/runs/"+started.ID, "")
	if described.Code != http.StatusOK {
		t.Fatalf("get run = %d", described.Code)
	}
	var info protocol.RunInfo
	if err := json.Unmarshal(described.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Status != protocol.RunDone || info.FinishedAt.IsZero() {
		t.Fatalf("finished run = %#v", info)
	}
}

func TestStreamServesEventStreamWhenAsked(t *testing.T) {
	server := newTestServer(t)
	response := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"say hi"}`)
	var started protocol.RunInfo
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	stream := requestWithHeaders(t, server, http.MethodGet, "/v1/runs/"+started.ID+"/events", "", map[string]string{"Accept": "text/event-stream"})
	if stream.Code != http.StatusOK || stream.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("sse response = %d %s", stream.Code, stream.Header().Get("Content-Type"))
	}
	if !strings.Contains(stream.Body.String(), "data: ") {
		t.Fatalf("sse body = %q", stream.Body.String())
	}
	if !strings.Contains(stream.Body.String(), `"type":"run_finished"`) {
		t.Fatalf("sse stream did not finish: %q", stream.Body.String())
	}
}

func TestUnknownRunsAndBadBodiesAreRejected(t *testing.T) {
	server := newTestServer(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{name: "unknown run", method: http.MethodGet, path: "/v1/runs/run_missing", status: http.StatusNotFound},
		{name: "unknown run steer", method: http.MethodPost, path: "/v1/runs/run_missing/steer", body: `{"text":"x"}`, status: http.StatusNotFound},
		{name: "unknown run abort", method: http.MethodPost, path: "/v1/runs/run_missing/abort", status: http.StatusNotFound},
		{name: "unknown run events", method: http.MethodGet, path: "/v1/runs/run_missing/events", status: http.StatusNotFound},
		{name: "blank prompt", method: http.MethodPost, path: "/v1/runs", body: `{"prompt":"  "}`, status: http.StatusBadRequest},
		{name: "malformed body", method: http.MethodPost, path: "/v1/runs", body: `{`, status: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, path: "/v1/runs", body: `{"prompt":"x","nope":1}`, status: http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, server, test.method, test.path, test.body)
			if response.Code != test.status {
				t.Fatalf("%s %s = %d, want %d (%s)", test.method, test.path, response.Code, test.status, response.Body)
			}
			if test.status == http.StatusNotFound && !strings.Contains(response.Body.String(), `"code":"unknown_run"`) {
				t.Fatalf("error body = %s", response.Body)
			}
		})
	}
}

// request performs one API call against the server.
func request(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithHeaders(t, server, method, path, body, nil)
}

func requestWithHeaders(t *testing.T, server *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// streamRecords reads a run's NDJSON stream to completion.
func streamRecords(t *testing.T, server *Server, runID string) []protocol.Record {
	t.Helper()
	response := request(t, server, http.MethodGet, "/v1/runs/"+runID+"/events", "")
	if response.Code != http.StatusOK {
		t.Fatalf("events = %d", response.Code)
	}
	records := []protocol.Record{}
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record protocol.Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		t.Fatal("the stream had no records")
	}
	if records[len(records)-1].Type != protocol.EventRunFinished {
		t.Fatalf("stream did not end with run_finished: %#v", records[len(records)-1])
	}
	return records
}

// blockingProvider holds each run open until it is released, so steer, abort, and
// concurrency can be tested while a run is genuinely in flight.
type blockingProvider struct {
	startedOnce sync.Once
	started     chan struct{}

	mu       sync.Mutex
	releases []chan struct{}
}

func (*blockingProvider) ID() string                                   { return "blocking" }
func (*blockingProvider) Models(_ context.Context) ([]ai.Model, error) { return nil, nil }

func (p *blockingProvider) Stream(ctx context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	release := make(chan struct{})
	p.mu.Lock()
	p.releases = append(p.releases, release)
	p.mu.Unlock()

	stream := ai.NewAssistantStream()
	partial := ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	go func() {
		p.startedOnce.Do(func() { close(p.started) })
		message := ai.AssistantMessage{
			Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText("done")}, StopReason: ai.StopComplete,
			Usage: ai.Usage{Input: 10, Output: 5, TotalTokens: 15},
		}
		select {
		case <-release:
		case <-ctx.Done():
			// Real providers report cancellation instead of a clean stop.
			message.StopReason = ai.StopAborted
			message.ErrorMessage = "aborted by client"
		}
		stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	}()
	return stream, nil
}

// releaseAll lets every blocked run finish.
func (p *blockingProvider) releaseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, release := range p.releases {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	p.releases = nil
}

func newBlockingServer(t *testing.T) (*Server, *blockingProvider) {
	t.Helper()
	provider := &blockingProvider{started: make(chan struct{})}
	server, err := NewServer(Config{
		Root: t.TempDir(), Provider: provider,
		Model: ai.Model{ID: "blocking-model", Provider: "blocking"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, provider
}

func TestSteerAbortAndConcurrencyWhileRunning(t *testing.T) {
	server, provider := newBlockingServer(t)

	first := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"start","sessionID":"ses_live"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("start = %d: %s", first.Code, first.Body)
	}
	var started protocol.RunInfo
	if err := json.Unmarshal(first.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	<-provider.started

	// A second run on the same session is refused while the first is in flight.
	conflict := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"another","sessionID":"ses_live"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("concurrent run = %d: %s", conflict.Code, conflict.Body)
	}

	// Steering is accepted while the run is going, and the text reaches the agent.
	steer := request(t, server, http.MethodPost, "/v1/runs/"+started.ID+"/steer", `{"text":"try the other file"}`)
	if steer.Code != http.StatusAccepted {
		t.Fatalf("steer = %d: %s", steer.Code, steer.Body)
	}
	blank := request(t, server, http.MethodPost, "/v1/runs/"+started.ID+"/steer", `{"text":"   "}`)
	if blank.Code != http.StatusBadRequest {
		t.Fatalf("blank steer = %d", blank.Code)
	}

	// Abort answers immediately and the stream ends as aborted.
	abort := request(t, server, http.MethodPost, "/v1/runs/"+started.ID+"/abort", `{"reason":"changed my mind"}`)
	if abort.Code != http.StatusAccepted {
		t.Fatalf("abort = %d", abort.Code)
	}
	records := streamRecords(t, server, started.ID)
	final := records[len(records)-1]
	if final.Type != protocol.EventRunFinished {
		t.Fatalf("stream did not finish: %#v", final)
	}
	if final.Status != protocol.RunAborted || final.Failure == "" {
		t.Fatalf("status after abort = %q (%q)", final.Status, final.Failure)
	}

	// Once it has finished, the same session accepts a new run again.
	provider.releaseAll()
	waitFor(t, func() bool {
		info := request(t, server, http.MethodGet, "/v1/runs/"+started.ID, "")
		var described protocol.RunInfo
		if err := json.Unmarshal(info.Body.Bytes(), &described); err != nil {
			return false
		}
		return described.Status != protocol.RunRunning
	})
	again := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"later","sessionID":"ses_live"}`)
	if again.Code != http.StatusAccepted {
		t.Fatalf("run after completion = %d: %s", again.Code, again.Body)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// TestConcurrentStartIsRejectedBeforeTheProvider: a duplicate start for a session
// that already has a run in flight must be refused without another paid request.
func TestConcurrentStartIsRejectedBeforeTheProvider(t *testing.T) {
	server, provider := newBlockingServer(t)
	first := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"start","sessionID":"ses_one"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first start = %d: %s", first.Code, first.Body)
	}
	<-provider.started

	conflict := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"second","sessionID":"ses_one"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("second start = %d: %s", conflict.Code, conflict.Body)
	}
	provider.mu.Lock()
	streams := len(provider.releases)
	provider.mu.Unlock()
	if streams != 1 {
		t.Fatalf("the provider was asked %d times, want 1", streams)
	}

	// A different session is not blocked by the first one.
	other := request(t, server, http.MethodPost, "/v1/runs", `{"prompt":"other","sessionID":"ses_two"}`)
	if other.Code != http.StatusAccepted {
		t.Fatalf("other session start = %d: %s", other.Code, other.Body)
	}
	provider.releaseAll()
}

// TestRunIDsAreUniqueWithinTheSameInstant: an injected clock returns the same
// time for every call, and two runs must still get different IDs.
func TestRunIDsAreUniqueWithinTheSameInstant(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{})}
	server, err := NewServer(Config{
		Root: t.TempDir(), Provider: provider,
		Model: ai.Model{ID: "blocking-model", Provider: "blocking"},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return frozen }
	seen := map[string]bool{}
	for index := 0; index < 5; index++ {
		body := `{"prompt":"run","sessionID":"ses_` + strconv.Itoa(index) + `"}`
		response := request(t, server, http.MethodPost, "/v1/runs", body)
		if response.Code != http.StatusAccepted {
			t.Fatalf("start %d = %d: %s", index, response.Code, response.Body)
		}
		var info protocol.RunInfo
		if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if seen[info.ID] {
			t.Fatalf("duplicate run ID %q", info.ID)
		}
		seen[info.ID] = true
	}
	provider.releaseAll()
}
