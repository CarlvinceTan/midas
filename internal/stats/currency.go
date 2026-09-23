package stats

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	midassettings "github.com/CarlvinceTan/midas/internal/settings"
)

// Cost display currency, ported from the pre-port Midas `formatMoney`: costs are
// computed in USD, converted with a cached Frankfurter rate, and shown with the
// configured currency's symbol.
const (
	currencyCacheTTL  = 24 * time.Hour
	currencyRateURL   = "https://api.frankfurter.app/latest?from=USD&to="
	currencyMinRate   = 0.0001
	currencyMaxRate   = 1_000_000
	currencyCacheName = "exchange-rate.json"
	// currencyRetryDelay is how long a failed rate fetch waits before retrying.
	currencyRetryDelay = time.Minute
)

var currencySymbols = map[string]string{
	"USD": "$", "AUD": "A$", "SGD": "S$", "CAD": "C$", "NZD": "NZ$",
	"HKD": "HK$", "CNY": "CN¥", "INR": "₹", "EUR": "€", "GBP": "£",
	"JPY": "¥", "KRW": "₩", "CHF": "CHF", "MYR": "RM", "IDR": "Rp",
	"PHP": "₱", "THB": "฿", "AED": "AED", "BRL": "R$", "ZAR": "ZAR",
}

// timezoneCurrencies maps the machine timezone to a display currency, used only
// when the setting is "location".
var timezoneCurrencies = []struct {
	prefix string
	code   string
}{
	{"Australia/", "AUD"}, {"Asia/Singapore", "SGD"}, {"Asia/Kuala_Lumpur", "MYR"},
	{"Asia/Jakarta", "IDR"}, {"Asia/Manila", "PHP"}, {"Asia/Bangkok", "THB"},
	{"Asia/Ho_Chi_Minh", "VND"}, {"Asia/Kolkata", "INR"}, {"Asia/Calcutta", "INR"},
	{"Asia/Tokyo", "JPY"}, {"Asia/Seoul", "KRW"}, {"Asia/Shanghai", "CNY"},
	{"Asia/Hong_Kong", "HKD"}, {"Asia/Taipei", "TWD"}, {"Asia/Dubai", "AED"},
	{"Pacific/Auckland", "NZD"}, {"Europe/London", "GBP"}, {"Europe/Zurich", "CHF"},
	{"Europe/Stockholm", "SEK"}, {"Europe/Oslo", "NOK"}, {"Europe/Copenhagen", "DKK"},
	{"Europe/Warsaw", "PLN"}, {"Europe/", "EUR"}, {"America/Toronto", "CAD"},
	{"America/Vancouver", "CAD"}, {"America/Sao_Paulo", "BRL"}, {"America/Mexico_City", "MXN"},
	{"America/", "USD"}, {"Africa/Johannesburg", "ZAR"},
}

type exchangeRateCache struct {
	Timestamp int64   `json:"timestamp"`
	Code      string  `json:"code"`
	Rate      float64 `json:"rate"`
}

type costFormatter struct {
	mu       sync.Mutex
	path     string
	values   func() midassettings.Values
	timezone func() string
	fetch    func(code string) (float64, bool)
	request  func()

	code     string
	rate     float64
	loaded   bool
	fetching bool
	// retryAt is when a failed refresh may be tried again, so a network outage
	// does not turn every render into a request.
	retryAt time.Time
}

// NewCostFormatter builds the footer's money formatter. Currency conversion is
// best effort: a missing or stale rate falls back to USD until the next
// successful refresh, so a network failure never blocks or breaks the UI.
func NewCostFormatter(configDir string, values func() midassettings.Values, request func()) func(float64) string {
	formatter := &costFormatter{
		path:     filepath.Join(configDir, currencyCacheName),
		values:   values,
		timezone: func() string { return time.Local.String() },
		fetch:    fetchExchangeRate,
		request:  request,
	}
	return formatter.format
}

func (c *costFormatter) format(usd float64) string {
	code := currencyCodeFor(c.values().Currency, c.timezone())
	c.mu.Lock()
	if code != c.code {
		c.code = code
		c.loaded = false
		c.rate = 1
		c.fetching = false
	}
	if !c.loaded {
		switch {
		case code == "USD":
			c.rate, c.loaded = 1, true
		default:
			if cached, ok := readRateCache(c.path, code); ok {
				c.rate, c.loaded = cached, true
			} else if !time.Now().Before(c.retryAt) {
				// Render with a placeholder rate and refresh in the background.
				// A failed fetch backs off rather than leaving the placeholder
				// rate in place forever.
				c.rate = 1
				c.startRefreshLocked(code)
			}
		}
	}
	rate := c.rate
	c.mu.Unlock()
	value := usd * rate
	if value < 0 || usd < 0 {
		value = 0
	}
	return fmt.Sprintf("%s%.2f", currencySymbol(code), value)
}

func (c *costFormatter) startRefreshLocked(code string) {
	if c.fetching || c.fetch == nil {
		return
	}
	c.fetching = true
	go func() {
		rate, ok := c.fetch(code)
		c.mu.Lock()
		if ok && c.code == code {
			c.rate = rate
			c.loaded = true
			c.retryAt = time.Time{}
			writeRateCache(c.path, code, rate)
		} else if !ok {
			c.retryAt = time.Now().Add(currencyRetryDelay)
		}
		c.fetching = false
		refresh := c.request
		c.mu.Unlock()
		if ok && refresh != nil {
			refresh()
		}
	}()
}

// currencyCodeFor resolves the settings key into an ISO code.
func currencyCodeFor(setting, timezone string) string {
	switch setting {
	case "", "default":
		return "USD"
	case "location":
		for _, entry := range timezoneCurrencies {
			if strings.HasPrefix(timezone, entry.prefix) {
				return entry.code
			}
		}
		return "USD"
	default:
		return strings.ToUpper(setting)
	}
}

func currencySymbol(code string) string {
	if symbol, ok := currencySymbols[code]; ok {
		return symbol
	}
	if code == "" {
		return "$"
	}
	return code
}

func readRateCache(path, code string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var cached exchangeRateCache
	if json.Unmarshal(raw, &cached) != nil {
		return 0, false
	}
	if cached.Code != code || !validRate(cached.Rate) {
		return 0, false
	}
	if time.Since(time.UnixMilli(cached.Timestamp)) > currencyCacheTTL {
		return 0, false
	}
	return cached.Rate, true
}

func writeRateCache(path, code string, rate float64) {
	raw, err := json.Marshal(exchangeRateCache{Timestamp: time.Now().UnixMilli(), Code: code, Rate: rate})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}

func validRate(rate float64) bool {
	return rate >= currencyMinRate && rate <= currencyMaxRate
}

func fetchExchangeRate(code string) (float64, bool) {
	if code == "USD" {
		return 1, true
	}
	client := &http.Client{Timeout: 6 * time.Second}
	response, err := client.Get(currencyRateURL + code)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, false
	}
	var payload struct {
		Rates map[string]float64 `json:"rates"`
	}
	if json.NewDecoder(response.Body).Decode(&payload) != nil {
		return 0, false
	}
	rate, ok := payload.Rates[code]
	if !ok || !validRate(rate) {
		return 0, false
	}
	return rate, true
}
