package stats

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	midassettings "github.com/CarlvinceTan/midas/internal/settings"
)

func TestCurrencyCodeForResolvesSettingsKeys(t *testing.T) {
	cases := []struct {
		setting  string
		timezone string
		want     string
	}{
		{"", "Asia/Singapore", "USD"},
		{"default", "Australia/Sydney", "USD"},
		{"location", "Australia/Sydney", "AUD"},
		{"location", "Asia/Singapore", "SGD"},
		{"location", "Nowhere/Unknown", "USD"},
		{"aud", "UTC", "AUD"},
		{"SGD", "UTC", "SGD"},
	}
	for _, testCase := range cases {
		if got := currencyCodeFor(testCase.setting, testCase.timezone); got != testCase.want {
			t.Fatalf("currencyCodeFor(%q, %q) = %q, want %q", testCase.setting, testCase.timezone, got, testCase.want)
		}
	}
}

func TestCurrencySymbolsMatchThePrePortFooter(t *testing.T) {
	cases := map[string]string{"USD": "$", "AUD": "A$", "SGD": "S$", "CNY": "CN¥", "INR": "₹", "XYZ": "XYZ"}
	for code, want := range cases {
		if got := currencySymbol(code); got != want {
			t.Fatalf("currencySymbol(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestCostFormatterConvertsWithCachedRate(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, currencyCacheName)
	writeRateCache(path, "AUD", 1.5)
	if rate, ok := readRateCache(path, "AUD"); !ok || rate != 1.5 {
		t.Fatalf("cached rate = %v, %v", rate, ok)
	}
	values := func() midassettings.Values { return midassettings.Values{Currency: "AUD"} }
	format := NewCostFormatter(directory, values, nil)
	if got := format(0.045); got != "A$0.07" {
		t.Fatalf("format(0.045) = %q, want A$0.07", got)
	}
	if got := format(0); got != "A$0.00" {
		t.Fatalf("format(0) = %q", got)
	}
}

func TestCostFormatterFallsBackToUSDWithoutARate(t *testing.T) {
	values := func() midassettings.Values { return midassettings.Values{Currency: "AUD"} }
	var fetched atomic.Bool
	formatter := &costFormatter{
		path:     filepath.Join(t.TempDir(), currencyCacheName),
		values:   values,
		timezone: func() string { return "UTC" },
		fetch:    func(string) (float64, bool) { fetched.Store(true); return 0, false },
	}
	format := formatter.format
	if got := format(1); got != "A$1.00" {
		t.Fatalf("first render should fall back to USD value: %q", got)
	}
	deadline := time.Now().Add(time.Second)
	for !fetched.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !fetched.Load() {
		t.Fatal("a missing rate should trigger one background refresh")
	}
}

func TestCostFormatterRefreshesAndRepaintsOncePerCurrency(t *testing.T) {
	var calls atomic.Int32
	var repaints atomic.Int32
	formatter := &costFormatter{
		path:     filepath.Join(t.TempDir(), currencyCacheName),
		values:   func() midassettings.Values { return midassettings.Values{Currency: "AUD"} },
		timezone: func() string { return "UTC" },
		fetch:    func(string) (float64, bool) { calls.Add(1); return 2, true },
		request:  func() { repaints.Add(1) },
	}
	format := formatter.format
	format(1)
	deadline := time.Now().Add(time.Second)
	for repaints.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	repaintCount, fetchCount := repaints.Load(), calls.Load()
	if repaintCount != 1 || fetchCount != 1 {
		t.Fatalf("repaints = %d, fetches = %d, want 1 and 1", repaintCount, fetchCount)
	}
	if got := format(1); got != "A$2.00" {
		t.Fatalf("after refresh = %q, want A$2.00", got)
	}
	// The refreshed rate is cached, so no second fetch is started.
	if fetches := calls.Load(); fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}
	if _, ok := readRateCache(formatter.path, "AUD"); !ok {
		t.Fatal("the refreshed rate was not cached")
	}
}

// TestCostFormatterRetriesAfterAFailedFetch: a failed refresh used to leave the
// formatter permanently on the placeholder rate, so the wrong currency was shown
// for the rest of the session.
func TestCostFormatterRetriesAfterAFailedFetch(t *testing.T) {
	var calls atomic.Int32
	formatter := &costFormatter{
		path:     filepath.Join(t.TempDir(), currencyCacheName),
		values:   func() midassettings.Values { return midassettings.Values{Currency: "AUD"} },
		timezone: func() string { return "UTC" },
		fetch: func(string) (float64, bool) {
			// Fail the first attempt, then answer, so a retry is visible.
			if calls.Add(1) == 1 {
				return 0, false
			}
			return 2, true
		},
	}
	format := formatter.format
	format(1)
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// The failure backs off rather than hammering the endpoint...
	format(1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetches before the backoff elapsed = %d, want 1", got)
	}
	// ...and the rate is still retried once the backoff is over.
	formatter.mu.Lock()
	formatter.retryAt = time.Now().Add(-time.Second)
	formatter.mu.Unlock()
	format(1)
	deadline = time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 2 {
		t.Fatal("a failed fetch was never retried")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := format(1); got == "A$2.00" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the refreshed rate was not applied: %q", format(1))
}
