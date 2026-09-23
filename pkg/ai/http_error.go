package ai

import "fmt"

// HTTPError keeps a provider response status machine-readable while preserving
// the concise error strings shown by the CLI and TUI.
type HTTPError struct {
	Provider string
	Status   int
	Message  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}
