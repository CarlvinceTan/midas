package tui

import (
	"strings"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

type ToastLevel string

const (
	ToastSuccess ToastLevel = "success"
	ToastWarning ToastLevel = "warning"
	ToastError   ToastLevel = "error"
)

func toastWidth(value string, maximum int) int {
	return min(tuitext.VisibleWidth(value)+2, max(1, maximum))
}

func renderToast(value string, level ToastLevel, width int) string {
	style := "\x1b[43m\x1b[30m"
	if level == ToastSuccess {
		style = "\x1b[42m\x1b[30m"
	} else if level == ToastError {
		style = "\x1b[41m\x1b[97m"
	}
	inner := tuitext.TruncateToWidth(value, max(0, width-2), "…", false)
	fill := strings.Repeat(" ", max(0, width-2-tuitext.VisibleWidth(inner)))
	return style + " " + inner + fill + " \x1b[0m"
}
