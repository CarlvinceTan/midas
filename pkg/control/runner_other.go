//go:build !darwin

package control

import (
	"context"
	"fmt"
	"os"
)

// RunScript is only implemented on macOS: Linux and Windows need their own
// transport for the same capability rather than a shared guess.
func RunScript(ctx context.Context, script string) (string, error) {
	return "", fmt.Errorf("control: reading desktop state is not implemented on this platform yet")
}

func osHostname() (string, error) { return os.Hostname() }
