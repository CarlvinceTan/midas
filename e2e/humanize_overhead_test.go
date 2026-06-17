package e2e

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/PolymuxOrg/midas/browser"
	"github.com/PolymuxOrg/midas/humanize"
)

// TestHumanizeOverhead measures, against a real headless Chromium, how much
// slower each input operation is with humanize off vs on (the single profile).
// It exists to answer "how much does humanize actually cost?" with numbers
// rather than estimates.
//
// The cost is almost entirely injected *sleeps* (typing cadence, mouse step
// pauses, aim/hold, field-switch deliberation) — the Bezier/step math itself is
// microseconds — so these timings are dominated by the humanize.Config delay
// ranges, not by CDP round-trips.
//
//	go test ./e2e -run TestHumanizeOverhead -v -count=1
func TestHumanizeOverhead(t *testing.T) {
	page := newPage(t)
	if err := page.SetViewportSize(testCtx(t), 1000, 700, 1); err != nil {
		t.Fatalf("set viewport: %v", err)
	}

	onCfg := humanize.DefaultConfig()
	modes := []struct {
		name string
		cfg  *humanize.Config
	}{
		{"off", nil},
		{"on", &onCfg},
	}

	const reps = 4
	const email = "casey.jordan@example.com" // 24 chars — a realistic field

	// op runs a single timed operation on a freshly-reset form page. resetForm
	// clears any prior typed values + cursor state so each rep is independent.
	resetForm := func() {
		gotoPath(t, page, "/form.html")
	}

	// run executes fn under a generous context and returns the wall time.
	run := func(t *testing.T, fn func(ctx context.Context) error) time.Duration {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		start := time.Now()
		if err := fn(ctx); err != nil {
			t.Fatalf("op failed: %v", err)
		}
		return time.Since(start)
	}

	ops := []struct {
		name string
		fn   func(ctx context.Context, page *browser.Page) error
	}{
		{
			// Single click on a button: Bezier move from the current cursor +
			// aim/hold cadence.
			name: "click_button",
			fn: func(ctx context.Context, page *browser.Page) error {
				return page.Locator("#submit").Click(ctx)
			},
		},
		{
			// Hover only: the Bezier move with no press/release.
			name: "hover_button",
			fn: func(ctx context.Context, page *browser.Page) error {
				return page.Locator("#submit").Hover(ctx)
			},
		},
		{
			// Type a 24-char value into one field. On the humanize path this is
			// field-switch deliberation + a click to focus + per-character pacing.
			name: "type_email_24ch",
			fn: func(ctx context.Context, page *browser.Page) error {
				return page.Locator("#email").Type(ctx, email, 0)
			},
		},
		{
			// A realistic composite: fill two fields and submit, like a login.
			name: "form_flow_login",
			fn: func(ctx context.Context, page *browser.Page) error {
				if err := page.Locator("#username").Type(ctx, "casey.jordan", 0); err != nil {
					return err
				}
				if err := page.Locator("#email").Type(ctx, email, 0); err != nil {
					return err
				}
				return page.Locator("#submit").Click(ctx)
			},
		},
	}

	// results[op][mode] = mean duration.
	results := map[string]map[string]time.Duration{}

	for _, op := range ops {
		results[op.name] = map[string]time.Duration{}
		for _, m := range modes {
			page.EnableHumanize(m.cfg)
			var total time.Duration
			for i := 0; i < reps; i++ {
				resetForm()
				total += run(t, func(ctx context.Context) error {
					return op.fn(ctx, page)
				})
			}
			mean := total / time.Duration(reps)
			results[op.name][m.name] = mean
			t.Logf("%-18s %-8s mean=%s (n=%d)", op.name, m.name, mean.Round(time.Millisecond), reps)
		}
	}

	// Comparison table: per-op multiplier vs the "off" baseline.
	t.Log("")
	t.Logf("%-18s %12s %12s %10s", "operation", "off", "on", "on×")
	opNames := make([]string, 0, len(results))
	for name := range results {
		opNames = append(opNames, name)
	}
	sort.Strings(opNames)
	for _, name := range opNames {
		off := results[name]["off"]
		on := results[name]["on"]
		t.Logf("%-18s %12s %12s %9.1fx",
			name,
			off.Round(time.Millisecond),
			on.Round(time.Millisecond),
			ratio(on, off),
		)
	}
}

func ratio(a, b time.Duration) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}
