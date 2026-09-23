package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/internal/goal"
	"github.com/CarlvinceTan/midas/pkg/mcp"
	providerpkg "github.com/CarlvinceTan/midas/pkg/provider"
	midassettings "github.com/CarlvinceTan/midas/internal/settings"
	internalstats "github.com/CarlvinceTan/midas/internal/stats"
	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/internal/tui"
	"github.com/CarlvinceTan/midas/pkg/ai"
	providerauth "github.com/CarlvinceTan/midas/pkg/ai/auth"
)

type catalogProvider struct {
	id     string
	models []ai.Model
}

func (p catalogProvider) ID() string { return p.id }
func (p catalogProvider) Models(context.Context) ([]ai.Model, error) {
	return append([]ai.Model(nil), p.models...), nil
}
func (p catalogProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (*ai.AssistantStream, error) {
	return nil, errors.New("unused")
}

func TestVersionHelpAndHeadlessNoPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != buildVersion() {
		t.Fatalf("version = %q", stdout.String())
	}
	stdout.Reset()
	if err := run(context.Background(), nil, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("empty headless run = %v", err)
	}
	if err := run(context.Background(), []string{"--help"}, &stdout, &stderr, func(string) string { return "" }); err != nil || !strings.Contains(stdout.String(), "native Go coding agent") {
		t.Fatalf("help = %q, %v", stdout.String(), err)
	}
}

func TestHeadlessPromptStillRequiresModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--print", "hello"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("missing model = %v", err)
	}
}

func TestParseCLIParity(t *testing.T) {
	cli, err := parseCLI([]string{"-C", "/repo", "-m", "anthropic/claude-test", "-a", "advisor", "-s", "ses_1", "-p", "--mystery", "hello", "world"}, "/cwd")
	if err != nil {
		t.Fatal(err)
	}
	if cli.cwd != "/repo" || cli.model != "anthropic/claude-test" || cli.agent != "advisor" || cli.session != "ses_1" || !cli.print {
		t.Fatalf("cli = %#v", cli)
	}
	if cli.prompt != "--mystery hello world" {
		t.Fatalf("unknown option prompt = %q", cli.prompt)
	}
	provider, model := splitModel("openai", cli.model)
	if provider != "anthropic" || model != "claude-test" {
		t.Fatalf("model split = %q/%q", provider, model)
	}
}

func TestListAgentsAndModels(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--list-agents"}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main\tprimary", "advisor\tsubagent", "explore\tsubagent"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("agents missing %q:\n%s", name, stdout.String())
		}
	}
	stdout.Reset()
	env := func(name string) string {
		if name == "MIDAS_MODEL" {
			return "gpt-test"
		}
		return ""
	}
	if err := run(context.Background(), []string{"--list-models"}, &stdout, &stderr, env); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "openai/gpt-test\tgpt-test" {
		t.Fatalf("models = %q", got)
	}
	// A session cannot start as an internal helper or as a subagent: only primary
	// agents are entry points.
	for name, want := range map[string]string{"title": "utility agent", "summary": "utility agent", "explore": "subagent"} {
		err := run(context.Background(), []string{"--agent", name, "--list-agents"}, &stdout, &stderr, env)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--agent %s = %v, want %q", name, err, want)
		}
	}
}

func TestAuthorizeOAuthUsesLoopbackPKCE(t *testing.T) {
	var verifier string
	var emittedChallenge string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		verifier = request.Form.Get("code_verifier")
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	credential := providerauth.OAuthCredential{
		AuthorizationURL: "https://accounts.example/authorize", TokenURL: tokenServer.URL,
		ClientID: "client", Scopes: []string{"models.read"},
	}
	authorizeContext, cancelAuthorize := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAuthorize()
	authorized, err := providerpkg.AuthorizeOAuth(authorizeContext, credential, func(value string) error {
		authorizationURL, parseErr := url.Parse(value)
		if parseErr != nil {
			return parseErr
		}
		query := authorizationURL.Query()
		if query.Get("code_challenge_method") != "S256" || query.Get("redirect_uri") == "" || query.Get("state") == "" {
			t.Errorf("authorization query = %v", query)
		}
		emittedChallenge = query.Get("code_challenge")
		go func() {
			callback, _ := url.Parse(query.Get("redirect_uri"))
			callbackQuery := callback.Query()
			callbackQuery.Set("state", query.Get("state"))
			callbackQuery.Set("code", "authorization-code")
			callback.RawQuery = callbackQuery.Encode()
			response, callbackErr := http.Get(callback.String())
			if callbackErr == nil {
				response.Body.Close()
			}
		}()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.AccessToken != "access" || authorized.RefreshToken != "refresh" || verifier == "" {
		t.Fatalf("authorized = %#v, verifier = %q", authorized, verifier)
	}
	verifierHash := sha256.Sum256([]byte(verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	// The test opener already verified that a PKCE challenge was emitted; this
	// assertion also ensures the verifier accepted by the token endpoint is valid.
	if emittedChallenge != expectedChallenge {
		t.Fatalf("PKCE challenge = %q, want %q", emittedChallenge, expectedChallenge)
	}
}

func TestHeadlessCompatibleProviderEndToEnd(t *testing.T) {
	t.Setenv("MIDAS_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization = %q", got)
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"native ok\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"--print", "--provider", "local", "--base-url", server.URL, "--model", "tiny", "hello",
	}, &stdout, &stderr, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "native ok\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestGoalAndMCPCommands(t *testing.T) {
	ctx := context.Background()
	goals := goal.NewStore(filepath.Join(t.TempDir(), "goal.json"))
	result, err := runGoalCommand(ctx, goals, "")
	if err != nil || !strings.Contains(result, "No current goal") {
		t.Fatalf("empty goal = %q, %v", result, err)
	}
	result, err = runGoalCommand(ctx, goals, "create finish the Go port")
	if err != nil || !strings.Contains(result, "finish the Go port") || !strings.Contains(result, "[active]") {
		t.Fatalf("create goal = %q, %v", result, err)
	}
	result, err = runGoalCommand(ctx, goals, "pause")
	if err != nil || !strings.Contains(result, "[paused]") {
		t.Fatalf("pause goal = %q, %v", result, err)
	}
	result, err = runGoalCommand(ctx, goals, "resume")
	if err != nil || !strings.Contains(result, "[active]") {
		t.Fatalf("resume goal = %q, %v", result, err)
	}
	if _, err = runGoalCommand(ctx, goals, "complete"); err == nil {
		t.Fatal("completion without evidence succeeded")
	}
	result, err = runGoalCommand(ctx, goals, "complete tests are green")
	if err != nil || !strings.Contains(result, "tests are green") {
		t.Fatalf("complete goal = %q, %v", result, err)
	}

	manager, err := mcp.New(map[string]mcp.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	result, err = runMCPCommand(ctx, manager, "list")
	if err != nil || !strings.Contains(result, "No MCP servers configured") {
		t.Fatalf("mcp list = %q, %v", result, err)
	}
}

func TestNativeLocalReadCommandsStayWithinMidas(t *testing.T) {
	repo := t.TempDir()
	directory := t.TempDir()
	store := storage.New(directory)
	title := "Only Midas"
	store.UpsertSession(storage.SessionUpdate{ID: "ses_1", CWD: repo, Title: &title})
	assistant := ai.AssistantMessage{Role: ai.RoleAssistant, Usage: ai.Usage{Input: 12, Output: 4, Cost: ai.UsageCost{Total: 0.25}}}
	if err := store.WriteTranscript("ses_1", []ai.Message{assistant}); err != nil {
		t.Fatal(err)
	}
	manager, err := mcp.New(map[string]mcp.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	command := localCommand(goal.NewStore(filepath.Join(directory, "goal.json")), manager, store, repo, ai.Model{Provider: "openai", ID: "gpt-test", API: "openai-responses"})

	stats, err := command(context.Background(), "stats", "")
	if err != nil || !strings.Contains(stats, "Sessions: 1") || !strings.Contains(stats, "Model calls: 1") || !strings.Contains(stats, "$0.2500") {
		t.Fatalf("stats = %q, %v", stats, err)
	}
	listed, err := command(context.Background(), "sessions", "")
	if err != nil || !strings.Contains(listed, "ses_1  Only Midas") {
		t.Fatalf("sessions = %q, %v", listed, err)
	}
	model, err := command(context.Background(), "model", "")
	if err != nil || !strings.Contains(model, "openai/gpt-test") {
		t.Fatalf("model = %q, %v", model, err)
	}
	agents, err := command(context.Background(), "agents", "")
	if err != nil || !strings.Contains(agents, "advisor") || !strings.Contains(agents, "explore") {
		t.Fatalf("agents = %q, %v", agents, err)
	}
}

func TestInteractiveModelDiscoveryKeepsSelectedAndSortsCatalog(t *testing.T) {
	// Discovery also sweeps every provider with a saved credential, so point it
	// at an empty config directory to keep the catalog to the test double.
	t.Setenv("MIDAS_CONFIG_DIR", t.TempDir())
	current := catalogProvider{id: "fake", models: []ai.Model{
		{Provider: "fake", ID: "z"},
		{Provider: "fake", ID: "a"},
		{Provider: "fake", ID: "a"},
	}}
	selected := ai.Model{Provider: "fake", ID: "selected", API: "fake"}
	models := providerpkg.DiscoverModels(context.Background(), current, selected, func(string) string { return "" })
	ids := make([]string, len(models))
	for index, model := range models {
		ids[index] = model.ID
	}
	if got := strings.Join(ids, ","); got != "a,selected,z" {
		t.Fatalf("catalog = %q", got)
	}
}

func TestDisplayModelDetailsUsesDiscoveredNameAndLimits(t *testing.T) {
	selected := ai.Model{Provider: "openrouter", ID: "deepseek/deepseek-v4.1-flash", API: "openai-completions", BaseURL: "https://example.test/v1"}
	discovered := []ai.Model{{
		Provider: "openrouter", ID: selected.ID, Name: "DeepSeek V4.1 Flash",
		ContextWindow: 1_000_000, Reasoning: true,
	}}
	got := providerpkg.DisplayModelDetails(selected, discovered)
	if got.Name != "DeepSeek V4.1 Flash" || got.ContextWindow != 1_000_000 || got.API != selected.API || got.BaseURL != selected.BaseURL {
		t.Fatalf("display model = %#v", got)
	}
}

func TestVisibleAgentGroupsListRemainingProfiles(t *testing.T) {
	groups := visibleAgentGroups()
	if len(groups) != 3 ||
		strings.Join(groups[0], ",") != "main" ||
		strings.Join(groups[1], ",") != "advisor,explore" ||
		strings.Join(groups[2], ",") != "summary,title" {
		t.Fatalf("agent groups = %#v", groups)
	}
}
func TestApplyCatalogCapabilitiesMarksMixedProtocolReasoning(t *testing.T) {
	models := []ai.Model{
		{Provider: "command-code", ID: "claude-sonnet-5", API: "anthropic-messages"},
		{Provider: "command-code", ID: "deepseek/deepseek-v4-flash", API: "openai-completions"},
		{Provider: "command-code", ID: "llama-3", API: "openai-completions"},
		{Provider: "openai", ID: "claude-sonnet-5", API: "anthropic-messages"},
	}
	providerpkg.ApplyCatalogCapabilities(models)
	if !models[0].Reasoning || !models[1].Reasoning {
		t.Fatalf("mixed-protocol models should reason: %#v", models[:2])
	}
	if models[2].Reasoning {
		t.Fatalf("llama-3 should not reason: %#v", models[2])
	}
	if models[3].Reasoning {
		t.Fatalf("single-protocol providers are left alone: %#v", models[3])
	}
}

func TestWriteExitSummaryPrintsAlignedBoldResumeLines(t *testing.T) {
	var output bytes.Buffer
	writeExitSummary(&output, "ses_123", "Fix queue editing", 0, []ai.Message{ai.NewUserMessage("hi", time.Now())})
	want := "\n" +
		"  \x1b[2mSession \x1b[0m  \x1b[1mFix queue editing\x1b[0m\n" +
		"  \x1b[2mContinue\x1b[0m  \x1b[1mmidas -s ses_123\x1b[0m\n" +
		"\n"
	if output.String() != want {
		t.Fatalf("summary = %q, want %q", output.String(), want)
	}
	// Both labels share a column, so the values start on the same one.
	lines := strings.Split(strings.Trim(output.String(), "\n"), "\n")
	if len(lines) != 2 || strings.Index(lines[0], "Fix") != strings.Index(lines[1], "midas") {
		t.Fatalf("values are not aligned:\n%s", output.String())
	}
}

func TestWriteExitSummarySkipsEmptySessionsAndNamesUntitledOnes(t *testing.T) {
	var output bytes.Buffer
	writeExitSummary(&output, "ses_123", "Fix queue editing", 0, []ai.Message{ai.AssistantMessage{Role: ai.RoleAssistant, Timestamp: ai.UnixMillis(time.Now())}})
	if output.Len() != 0 {
		t.Fatalf("summary for a session without a prompt = %q", output.String())
	}
	output.Reset()
	writeExitSummary(&output, "", "Fix queue editing", 0, []ai.Message{ai.NewUserMessage("hi", time.Now())})
	if output.Len() != 0 {
		t.Fatalf("summary without a session ID = %q", output.String())
	}
	output.Reset()
	created := time.Date(2026, time.September, 22, 5, 16, 12, 478_000_000, time.UTC)
	writeExitSummary(&output, "ses_123", "   ", created.UnixMilli(), []ai.Message{ai.NewUserMessage("hi", time.Now())})
	if !strings.Contains(output.String(), "New session - 2026-09-22T05:16:12.478Z") {
		t.Fatalf("summary = %q", output.String())
	}
}

func TestHasUserPromptAcceptsPointerMessages(t *testing.T) {
	message := ai.NewUserMessage("hi", time.Now())
	if !hasUserPrompt([]ai.Message{&message}) {
		t.Fatal("pointer user message was not detected")
	}
	if hasUserPrompt(nil) {
		t.Fatal("empty transcript reported a prompt")
	}
}

func TestQueuedTextDropsEmptyPrompts(t *testing.T) {
	got := queuedText([]storage.QueuedPrompt{{Text: "one"}, {Text: "  "}, {Text: "two"}})
	if strings.Join(got, ",") != "one,two" {
		t.Fatalf("queue = %#v", got)
	}
}

func TestRotationCandidatesPreferSameProviderAndRespectBoundaries(t *testing.T) {
	failed := ai.Model{Provider: "alpha", ID: "large", API: "openai-responses", Reasoning: true, ContextWindow: 100}
	available := []ai.Model{
		{Provider: "beta", ID: "other", API: "openai-responses", Reasoning: true, Input: []ai.Modality{ai.ModalityText}},
		{Provider: "alpha", ID: "small", API: "openai-completions", Input: []ai.Modality{ai.ModalityText}},
		{Provider: "alpha", ID: "match", API: "openai-responses", Reasoning: true, ContextWindow: 200, Input: []ai.Modality{ai.ModalityText}},
		{Provider: "gamma", ID: "audio", Input: []ai.Modality{ai.Modality("audio")}},
	}
	values := midassettings.Values{AutoModelRotation: true, AutoProviderRotation: true}
	got := rotationCandidates(failed, []ai.Model{failed}, available, values, false)
	if len(got) != 3 || got[0].ID != "match" || got[1].ID != "small" || got[2].Provider != "beta" {
		t.Fatalf("rotation order = %#v", got)
	}
	auth := rotationCandidates(failed, []ai.Model{failed}, available, values, true)
	if len(auth) != 1 || auth[0].Provider != "beta" {
		t.Fatalf("auth rotation = %#v", auth)
	}
	providerOff := rotationCandidates(failed, []ai.Model{failed}, available, midassettings.Values{AutoModelRotation: true}, false)
	if len(providerOff) != 2 || providerOff[0].Provider != "alpha" || providerOff[1].Provider != "alpha" {
		t.Fatalf("provider-off rotation = %#v", providerOff)
	}
}

func TestAvailabilityFailureClassification(t *testing.T) {
	for _, err := range []error{
		&ai.HTTPError{Status: 404},
		&ai.HTTPError{Status: 429},
		&ai.HTTPError{Status: 503},
		context.DeadlineExceeded,
		errors.New("provider is overloaded"),
	} {
		if !isAvailabilityFailure(err) {
			t.Fatalf("not classified as availability failure: %v", err)
		}
	}
	for _, err := range []error{context.Canceled, &ai.HTTPError{Status: 400}, errors.New("invalid tool schema")} {
		if isAvailabilityFailure(err) {
			t.Fatalf("incorrect availability failure: %v", err)
		}
	}
	if !isAuthenticationFailure(&ai.HTTPError{Status: 401}) || isAuthenticationFailure(&ai.HTTPError{Status: 429}) {
		t.Fatal("authentication failure classification is wrong")
	}
}

func TestCanonicalProviderNameUsesCatalogBranding(t *testing.T) {
	if got := canonicalProviderName("command-code", "Command Code"); got != "CommandCode" {
		t.Fatalf("CommandCode display name = %q", got)
	}
	if got := canonicalProviderName("custom", "My Provider"); got != "My Provider" {
		t.Fatalf("custom provider display name = %q", got)
	}
}

func TestSessionTitleAndOSC52(t *testing.T) {
	if got := normalizeSessionTitle("  A   useful\nname  "); got != "A useful name" {
		t.Fatalf("normalized title = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := []rune(normalizeSessionTitle(long)); len(got) != 80 || got[79] != '…' {
		t.Fatalf("bounded title = %q (%d runes)", string(got), len(got))
	}
	if got := osc52("hello"); got != "\x1b]52;c;aGVsbG8=\x07" {
		t.Fatalf("OSC 52 = %q", got)
	}
}

func TestFormatStatsReportsCacheHitRatePerSessionAndOverall(t *testing.T) {
	summary := internalstats.Summary{
		Sessions: 2, Calls: 5,
		Tokens: internalstats.Tokens{Input: 2_000, Output: 500, CacheRead: 98_000, CacheWrite: 0},
		SessionCache: []internalstats.SessionCache{
			{ID: "ses_a", Title: "Fix queue editing", Calls: 4, Tokens: internalstats.Tokens{Input: 1_000, CacheRead: 19_000}},
			{ID: "ses_b", Title: "Probe", Calls: 1, Tokens: internalstats.Tokens{Input: 1_000, CacheRead: 79_000}},
		},
	}
	text := formatStats(summary)
	if !strings.Contains(text, "Cache: 98.0% of 100000 prompt tokens") {
		t.Fatalf("overall cache line missing:\n%s", text)
	}
	if !strings.Contains(text, "ses_a  95.0%  4 calls  Fix queue editing") {
		t.Fatalf("session cache line missing:\n%s", text)
	}
	// Sessions are ranked by call volume, so the busiest corpus leads.
	if strings.Index(text, "ses_a") > strings.Index(text, "ses_b") {
		t.Fatalf("session ranking wrong:\n%s", text)
	}
}

func TestExplicitProviderKeepsNamespacedModelID(t *testing.T) {
	t.Setenv("MIDAS_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer router-key" {
			t.Error("compatible-provider authorization was not preserved")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek/deepseek-chat" {
			t.Errorf("model = %#v", body["model"])
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"live ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	environment := func(name string) string {
		if name == "OPENROUTER_API_KEY" {
			return "router-key"
		}
		return ""
	}
	err := run(context.Background(), []string{
		"--print", "--provider", "openrouter", "--base-url", server.URL,
		"--model", "deepseek/deepseek-chat", "hello",
	}, &stdout, &stderr, environment)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "live ok" {
		t.Fatalf("output = %q", got)
	}
}

// TestStartupModelFollowsTheAgentConfiguration pins the rule the user expects: a
// pinned (or configured, or last-used) model applies to a new session and to a
// resumed one, while an explicit --model wins for that run.
func TestStartupModelFollowsTheAgentConfiguration(t *testing.T) {
	store := midassettings.New(t.TempDir())
	config := tui.LoadAgentModelConfig(store)

	// Nothing configured: the provider default stands.
	if provider, model := startupModel(false, "openai", "gpt-5.6-sol", config, "main"); provider != "openai" || model != "gpt-5.6-sol" {
		t.Fatalf("unconfigured startup = %s/%s", provider, model)
	}
	// An explicit choice outranks the agent's own model.
	if err := config.SetModel("main", "anthropic/claude-opus"); err != nil {
		t.Fatal(err)
	}
	if provider, model := startupModel(true, "openai", "gpt-5.6-sol", config, "main"); provider != "openai" || model != "gpt-5.6-sol" {
		t.Fatalf("explicit startup = %s/%s", provider, model)
	}
	// The pin applies when the command line names nothing.
	if provider, model := startupModel(false, "openai", "gpt-5.6-sol", config, "main"); provider != "anthropic" || model != "claude-opus" {
		t.Fatalf("pinned startup = %s/%s", provider, model)
	}
	// Clearing the pin returns the agent to the model it last used, which lives on
	// across sessions.
	if err := config.SetModel("main", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.SetLastUsed("main", "commandcode/deepseek-v4.1-flash"); err != nil {
		t.Fatal(err)
	}
	if provider, model := startupModel(false, "", "", config, "main"); provider != "commandcode" || model != "deepseek-v4.1-flash" {
		t.Fatalf("last-used startup = %s/%s", provider, model)
	}
	// A subagent with no model of its own inherits, which the caller supplies; with
	// nothing to inherit the provider default stands.
	if provider, model := startupModel(false, "openai", "gpt-5.6-sol", config, "advisor"); provider != "openai" || model != "gpt-5.6-sol" {
		t.Fatalf("subagent startup = %s/%s", provider, model)
	}
}

// TestStartupReasoningFollowsTheAgentConfiguration: the reasoning level recorded
// for the agent (or its model) is restored, and an explicit off stays off.
func TestStartupReasoningFollowsTheAgentConfiguration(t *testing.T) {
	store := midassettings.New(t.TempDir())
	config := tui.LoadAgentModelConfig(store)
	model := ai.Model{ID: "gpt-5.6-sol", Provider: "openai", Reasoning: true}
	ref := "openai/gpt-5.6-sol"
	if got := config.Level("main", ref, ai.ThinkingMedium); got != ai.ThinkingMedium {
		t.Fatalf("default reasoning = %q", got)
	}
	if err := config.SetLevel("main", ai.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if got := config.Level("main", ref, ai.ThinkingMedium); got != ai.ThinkingHigh {
		t.Fatalf("agent reasoning = %q", got)
	}
	if err := config.SetLevel("main", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.SetModelLevel(ref, ai.ThinkingLow); err != nil {
		t.Fatal(err)
	}
	if got := config.Level("main", ref, ai.ThinkingMedium); got != ai.ThinkingLow {
		t.Fatalf("model reasoning = %q", got)
	}
	if err := config.SetLevel("main", ai.ThinkingOff); err != nil {
		t.Fatal(err)
	}
	if got := config.Level("main", ref, ai.ThinkingMedium); got != ai.ThinkingOff {
		t.Fatalf("explicit off was not kept: %q", got)
	}
	_ = model
}

// TestProviderUsableDecidesWhetherAnAgentsModelCanRun keeps a pinned model from
// starting a session on a provider that cannot authenticate here, while leaving
// keyless local endpoints alone.
func TestProviderUsableDecidesWhetherAnAgentsModelCanRun(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("MIDAS_CONFIG_DIR", configDir)
	env := map[string]string{}
	getenv := func(name string) string { return env[name] }

	if providerUsable("openai", getenv) {
		t.Fatal("openai with no key was treated as usable")
	}
	env["OPENAI_API_KEY"] = "sk-test"
	if !providerUsable("openai", getenv) {
		t.Fatal("openai with an environment key was treated as unusable")
	}
	delete(env, "OPENAI_API_KEY")

	authStore := providerauth.New(configDir)
	if err := authStore.Set("openai", providerauth.Credential{Kind: providerauth.KindAPIKey, Name: "openai", APIKey: "sk-stored"}); err != nil {
		t.Fatal(err)
	}
	if !providerUsable("openai", getenv) {
		t.Fatal("openai with a stored key was treated as unusable")
	}
	// A provider the catalog does not know may be a local endpoint that needs no
	// key, so it is left to the caller.
	if !providerUsable("my-local-endpoint", getenv) {
		t.Fatal("an unknown provider was treated as unusable")
	}
	if providerUsable("", getenv) {
		t.Fatal("an empty provider was treated as usable")
	}
	// MIDAS_API_KEY covers every provider.
	env["MIDAS_API_KEY"] = "sk-shared"
	if !providerUsable("anthropic", getenv) {
		t.Fatal("MIDAS_API_KEY was ignored")
	}
}

// TestSplitProviderFallsBackToTheLastProvider: a bare model ID belongs to the
// provider last used, exactly as it does on the command line.
func TestSplitProviderFallsBackToTheLastProvider(t *testing.T) {
	if got := splitProvider("openai/gpt-5.6-sol", "command-code"); got != "openai" {
		t.Fatalf("provider = %q", got)
	}
	if got := splitProvider("gpt-5.6-sol", "command-code"); got != "command-code" {
		t.Fatalf("bare model provider = %q", got)
	}
}
