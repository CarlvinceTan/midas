//go:build darwin

package control

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunScript executes AppleScript and returns its output. Errors carry the script
// error verbatim, because the wording is what tells a caller whether consent was
// declined ("not authorized") or something else went wrong.
func RunScript(ctx context.Context, script string) (string, error) {
	command := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := command.CombinedOutput()
	text := string(output)
	if err != nil {
		message := strings.TrimSpace(text)
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s", message)
	}
	return text, nil
}

func osHostname() (string, error) { return os.Hostname() }
