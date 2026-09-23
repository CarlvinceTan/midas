package tui

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func capabilityBooleanOverride(value string) (bool, bool) {
	if value == "1" {
		return true, true
	}
	if value == "0" {
		return false, true
	}
	return false, false
}

func probeTmuxHyperlinks() bool {
	contextValue, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(contextValue, "tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil {
		return false
	}
	for _, feature := range strings.Split(string(output), ",") {
		if strings.TrimSpace(feature) == "hyperlinks" {
			return true
		}
	}
	return false
}

func detectTerminalCapabilitiesWith(tmuxHyperlinks func() bool) TerminalCapabilities {
	hyperlinkOverride, hasHyperlinkOverride := capabilityBooleanOverride(os.Getenv("MIDAS_HYPERLINKS"))
	if hasHyperlinkOverride {
		tmuxHyperlinks = func() bool { return hyperlinkOverride }
	}
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	terminalEmulator := strings.ToLower(os.Getenv("TERMINAL_EMULATOR"))
	term := strings.ToLower(os.Getenv("TERM"))
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
	trueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"
	capabilities := TerminalCapabilities{TrueColor: trueColorHint}
	if os.Getenv("TMUX") != "" || strings.HasPrefix(term, "tmux") {
		capabilities.Hyperlinks = tmuxHyperlinks()
	} else if strings.HasPrefix(term, "screen") {
		// Conservative defaults are already correct.
	} else if os.Getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		capabilities = TerminalCapabilities{Images: ImageKitty, TrueColor: true, Hyperlinks: true}
	} else if termProgram == "ghostty" || strings.Contains(term, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		capabilities = TerminalCapabilities{Images: ImageKitty, TrueColor: true, Hyperlinks: true}
	} else if os.Getenv("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		capabilities = TerminalCapabilities{Images: ImageKitty, TrueColor: true, Hyperlinks: true}
	} else if termProgram == "warpterminal" || os.Getenv("WARP_SESSION_ID") != "" || os.Getenv("WARP_TERMINAL_SESSION_UUID") != "" {
		capabilities = TerminalCapabilities{Images: ImageKitty, TrueColor: true, Hyperlinks: true}
	} else if os.Getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		capabilities = TerminalCapabilities{Images: ImageITerm2, TrueColor: true, Hyperlinks: true}
	} else if os.Getenv("WT_SESSION") != "" {
		capabilities = TerminalCapabilities{TrueColor: true, Hyperlinks: true}
	} else if termProgram == "alacritty" || termProgram == "vscode" || termProgram == "zed" {
		capabilities = TerminalCapabilities{TrueColor: true, Hyperlinks: true}
	} else if terminalEmulator == "jetbrains-jediterm" {
		capabilities = TerminalCapabilities{TrueColor: true}
	} else if runtime.GOOS == "windows" {
		capabilities = TerminalCapabilities{TrueColor: true}
	}
	if value := strings.ToLower(os.Getenv("MIDAS_IMAGE_PROTOCOL")); value == "kitty" {
		capabilities.Images = ImageKitty
	} else if value == "iterm2" {
		capabilities.Images = ImageITerm2
	} else if value == "none" || value == "0" {
		capabilities.Images = ImageNone
	}
	if value, ok := capabilityBooleanOverride(os.Getenv("MIDAS_TRUE_COLOR")); ok {
		capabilities.TrueColor = value
	}
	if hasHyperlinkOverride {
		capabilities.Hyperlinks = hyperlinkOverride
	}
	return capabilities
}

// DetectTerminalCapabilities detects graphics, true-color, and hyperlink support.
func DetectTerminalCapabilities() TerminalCapabilities {
	return detectTerminalCapabilitiesWith(probeTmuxHyperlinks)
}
