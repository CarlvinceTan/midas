package debug

import (
	"log"
	"net/url"
	"os"
	"strings"
)

const envKey = "MIDAS_DEBUG_BROWSER"

var stderrLog = log.New(os.Stderr, "polymux browser: ", log.LstdFlags)

// Enabled reports whether MIDAS_DEBUG_BROWSER is truthy.
func Enabled() bool {
	v := strings.TrimSpace(os.Getenv(envKey))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Printf logs to stderr when debug is enabled.
func Printf(format string, args ...any) {
	if !Enabled() {
		return
	}
	stderrLog.Printf(format, args...)
}

// WSSummary returns scheme+host+path for logging, stripping query params.
func WSSummary(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "(invalid ws url)"
	}
	if u.Host == "" {
		return wsURL
	}
	s := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		s += u.Path
	}
	return s
}
