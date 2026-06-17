package e2e

import (
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/cache"
)

// Cache replay drives a recorded action sequence against a live page — the
// agent's record-once/replay-fast path. These exercise the Replayer end to end
// (not just the in-memory cache unit tests).

func newReplayer(t *testing.T) *cache.Replayer {
	t.Helper()
	store := cache.NewMemoryStorage()
	c := cache.New(store)
	return cache.NewReplayer(c)
}

func TestCacheReplayActionSequence(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)

	entry := cache.NewEntry("login-flow", []cache.Action{
		{Type: cache.ActionTypeGoto, Value: h.server.URL + "/form.html"},
		{Type: cache.ActionTypeType, Selector: "#username", Value: "replayed-user"},
		{Type: cache.ActionTypeType, Selector: "#email", Value: "replayed@example.com"},
		{Type: cache.ActionTypeClick, Selector: "#submit"},
	}, h.server.URL+"/form.html")

	res, err := newReplayer(t).Replay(testCtx(t), page, entry, cache.ReplayOptions{}, h.server.URL+"/form.html")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !res.Success {
		t.Fatalf("replay reported failure: %+v", res)
	}
	if res.Executed != 4 {
		t.Errorf("executed = %d, want 4 actions", res.Executed)
	}
	status := evalString(t, page, "document.getElementById('status').textContent")
	if status != "submitted:replayed-user:replayed@example.com:" {
		t.Errorf("form status = %q, want the replayed values submitted", status)
	}
}

func TestCacheReplayWithVariables(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)

	entry := cache.NewEntry("param-flow", []cache.Action{
		{Type: cache.ActionTypeGoto, Value: h.server.URL + "/form.html"},
		{Type: cache.ActionTypeType, Selector: "#username", Value: "%user%"},
	}, h.server.URL+"/form.html")
	entry.Variables = []string{"user"}

	res, err := newReplayer(t).Replay(testCtx(t), page, entry, cache.ReplayOptions{
		Variables: map[string]string{"user": "alice"},
	}, h.server.URL+"/form.html")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !res.Success {
		t.Fatalf("replay failed: %+v", res)
	}
	got := evalString(t, page, "document.getElementById('username').value")
	if got != "alice" {
		t.Errorf("username = %q, want 'alice' (variable interpolation)", got)
	}
}

func TestCacheReplaySelfHealsChangedSelector(t *testing.T) {
	page := newPage(t)
	h := requireHarness(t)
	// The recorded xpath nests the button under a wrapper (#gone) that no
	// longer exists, so the exact path fails to resolve. The button's `name`
	// attribute survives, so attr-match self-heal recovers it via //*[@name=…].
	h.server.SetContent(t, "/heal.html", `<!DOCTYPE html>
<title>Heal</title>
<button name="submitBtn" onclick="document.getElementById('out').textContent='healed click'">Sign up</button>
<div id="out">idle</div>`)

	entry := cache.NewEntry("heal-flow", []cache.Action{
		{Type: cache.ActionTypeGoto, Value: h.server.URL + "/heal.html"},
		{Type: cache.ActionTypeClick, Selector: `xpath=//section[@id='gone']/button[@name='submitBtn']`},
	}, h.server.URL+"/heal.html")

	res, err := newReplayer(t).Replay(testCtx(t), page, entry, cache.ReplayOptions{
		SelfHeal: true,
		Timeout:  2 * time.Second, // fail the broken selector fast, then heal
	}, h.server.URL+"/heal.html")
	if err != nil {
		t.Fatalf("replay with self-heal: %v (result=%+v)", err, res)
	}
	if out := evalString(t, page, "document.getElementById('out').textContent"); out != "healed click" {
		t.Errorf("self-heal did not recover the changed selector: out=%q", out)
	}
}
