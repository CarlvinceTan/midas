// Package e2e contains integration tests that drive midas against a real
// headless Chromium. The whole package is test-only: nothing here ships in
// library builds.
//
// One browser process is launched per test binary (TestMain); each test gets
// its own fresh page via newPage. Tests are served fixture HTML from
// e2e/assets through a local httptest server with per-test route overrides.
//
// Tests skip automatically when no Chromium binary can be found. Point
// MIDAS_CHROME_PATH (or CHROMIUM_PATH) at a binary to override discovery,
// set MIDAS_E2E_HEADED=1 to watch the browser.
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
	"github.com/PolymuxOrg/midas/launch"
)

const defaultTimeout = 15 * time.Second

var (
	harness     *testHarness
	harnessErr  error
	harnessOnce sync.Once
)

type testHarness struct {
	bctx   *browser.Context
	chrome launch.ManagedBrowser
	server *fixtureServer
}

func TestMain(m *testing.M) {
	code := m.Run()
	if harness != nil {
		harness.close()
	}
	if cliBinDir != "" {
		_ = os.RemoveAll(cliBinDir)
	}
	os.Exit(code)
}

// resolveChromePath finds a Chromium/Chrome binary: env override first, then PATH.
func resolveChromePath() string {
	for _, env := range []string{"MIDAS_CHROME_PATH", "MIDAS_CHROMIUM_PATH", "CHROMIUM_PATH"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			return p
		}
	}
	for _, name := range []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func newHarness() (*testHarness, error) {
	chromePath := resolveChromePath()
	if chromePath == "" {
		return nil, nil // caller skips
	}

	headless := os.Getenv("MIDAS_E2E_HEADED") == ""
	enableCleanup := false
	handleSignals := false

	// LaunchLocalChrome uses exec.CommandContext, so the launch ctx bounds the
	// browser process lifetime — it must outlive the whole test binary.
	// ConnectTimeoutMs bounds the startup probe instead.
	result, err := launch.LaunchLocalChrome(context.Background(), launch.LaunchLocalOptions{
		ChromePath: chromePath,
		Headless:   &headless,
		ChromeFlags: []string{
			"--password-store=basic",
			"--no-sandbox",
		},
		ConnectTimeoutMs:   30000,
		EnableCrashCleanup: &enableCleanup,
		HandleSignals:      &handleSignals,
	})
	if err != nil {
		return nil, fmt.Errorf("launch chrome: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bctx, err := browser.Connect(ctx, result.WS, browser.ConnectOptions{
		EnsureFirstTopLevelPage:    true,
		FirstTopLevelPageTimeoutMs: 10000,
	})
	if err != nil {
		_ = result.Resource.Close()
		return nil, fmt.Errorf("connect CDP: %w", err)
	}

	return &testHarness{
		bctx:   bctx,
		chrome: result.Resource,
		server: newFixtureServer(),
	}, nil
}

func (h *testHarness) close() {
	if h == nil {
		return
	}
	if h.bctx != nil {
		_ = h.bctx.Close()
	}
	if h.chrome != nil {
		_ = h.chrome.Close()
	}
	if h.server != nil {
		h.server.Close()
	}
}

// knownBug marks a test as documenting a confirmed midas defect that is not
// yet fixed. It skips so the suite stays green as a regression baseline, while
// keeping the test body as the executable spec for the desired behavior.
//
// When the underlying bug is fixed, delete the knownBug(...) line and the test
// becomes an enforcing regression test. Grep "KNOWN-BUG" to list the backlog.
//
// Set MIDAS_E2E_RUN_KNOWN_BUGS=1 to run these anyway (e.g. to confirm a fix).
func knownBug(t *testing.T, ref, detail string) {
	t.Helper()
	if os.Getenv("MIDAS_E2E_RUN_KNOWN_BUGS") != "" {
		return
	}
	t.Skipf("KNOWN-BUG[%s]: %s", ref, detail)
}

// requireHarness lazily launches the shared browser, skipping the test when
// no Chromium binary is available.
func requireHarness(t *testing.T) *testHarness {
	t.Helper()
	harnessOnce.Do(func() {
		harness, harnessErr = newHarness()
	})
	if harnessErr != nil {
		t.Fatalf("browser harness failed to start: %v", harnessErr)
	}
	if harness == nil {
		t.Skip("no Chromium binary found (set MIDAS_CHROME_PATH); skipping e2e test")
	}
	return harness
}

// newPage opens a fresh tab for this test and closes it on cleanup.
func newPage(t *testing.T) *browser.Page {
	t.Helper()
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	page, err := h.bctx.NewPage(ctx, "about:blank")
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = page.Close(closeCtx)
	})
	return page
}

// testCtx returns a context bounded by the default per-test timeout.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	t.Cleanup(cancel)
	return ctx
}

// contextWithTimeout is context.WithTimeout from background, for tests that
// need a tighter or looser bound than testCtx.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// gotoPath navigates the page to a fixture path (e.g. "/button.html") on the
// shared server and fails the test on error or non-OK response.
func gotoPath(t *testing.T, page *browser.Page, path string) {
	t.Helper()
	h := requireHarness(t)
	resp, err := page.Goto(testCtx(t), h.server.URL+path)
	if err != nil {
		t.Fatalf("goto %s: %v", path, err)
	}
	if resp != nil && !resp.Ok() {
		t.Fatalf("goto %s: status %d", path, resp.Status())
	}
}

// fixtureServer serves static files from e2e/assets and supports per-test
// dynamic route overrides (cleared via t.Cleanup).
type fixtureServer struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	files  http.Handler
}

func newFixtureServer() *fixtureServer {
	_, thisFile, _, _ := runtime.Caller(0)
	assetsDir := filepath.Join(filepath.Dir(thisFile), "assets")
	fs := &fixtureServer{
		routes: make(map[string]http.HandlerFunc),
		files:  http.FileServer(http.Dir(assetsDir)),
	}
	fs.Server = httptest.NewServer(http.HandlerFunc(fs.serve))
	return fs
}

func (fs *fixtureServer) serve(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	handler, ok := fs.routes[r.URL.Path]
	fs.mu.Unlock()
	if ok {
		handler(w, r)
		return
	}
	fs.files.ServeHTTP(w, r)
}

// SetRoute installs a dynamic handler for path, removed when the test ends.
func (fs *fixtureServer) SetRoute(t *testing.T, path string, handler http.HandlerFunc) {
	t.Helper()
	fs.mu.Lock()
	fs.routes[path] = handler
	fs.mu.Unlock()
	t.Cleanup(func() {
		fs.mu.Lock()
		delete(fs.routes, path)
		fs.mu.Unlock()
	})
}

// SetContent serves the given HTML at path for the duration of the test.
func (fs *fixtureServer) SetContent(t *testing.T, path, html string) {
	t.Helper()
	fs.SetRoute(t, path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
}

// SetRedirect makes path 302-redirect to target for the duration of the test.
func (fs *fixtureServer) SetRedirect(t *testing.T, path, target string) {
	t.Helper()
	fs.SetRoute(t, path, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// WaitForRequest returns a channel that receives the next request hitting
// path (in addition to its normal handling).
func (fs *fixtureServer) WaitForRequest(t *testing.T, path string) <-chan *http.Request {
	t.Helper()
	ch := make(chan *http.Request, 1)
	fs.mu.Lock()
	prev, hadPrev := fs.routes[path]
	fs.mu.Unlock()
	fs.SetRoute(t, path, func(w http.ResponseWriter, r *http.Request) {
		select {
		case ch <- r:
		default:
		}
		if hadPrev {
			prev(w, r)
			return
		}
		fs.files.ServeHTTP(w, r)
	})
	return ch
}

// CrossOriginURL returns the same server reachable under a different origin
// (localhost vs 127.0.0.1), for cheap cross-origin navigation tests.
func (fs *fixtureServer) CrossOriginURL() string {
	return strings.Replace(fs.URL, "127.0.0.1", "localhost", 1)
}

// evalString evaluates a JS expression and returns the string result.
func evalString(t *testing.T, page *browser.Page, expression string) string {
	t.Helper()
	var out string
	if err := page.Evaluate(testCtx(t), expression, &out); err != nil {
		t.Fatalf("evaluate %q: %v", expression, err)
	}
	return out
}

// evalInt evaluates a JS expression and returns the numeric result.
func evalInt(t *testing.T, page *browser.Page, expression string) int {
	t.Helper()
	var out float64
	if err := page.Evaluate(testCtx(t), expression, &out); err != nil {
		t.Fatalf("evaluate %q: %v", expression, err)
	}
	return int(out)
}

// evalBool evaluates a JS expression and returns the boolean result.
func evalBool(t *testing.T, page *browser.Page, expression string) bool {
	t.Helper()
	var out bool
	if err := page.Evaluate(testCtx(t), expression, &out); err != nil {
		t.Fatalf("evaluate %q: %v", expression, err)
	}
	return out
}

// waitForCondition polls a JS expression until it evaluates to true or the
// timeout elapses.
func waitForCondition(t *testing.T, page *browser.Page, expression string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var out bool
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := page.Evaluate(ctx, expression, &out)
		cancel()
		if err == nil && out {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition %q not met within %s", expression, timeout)
}
