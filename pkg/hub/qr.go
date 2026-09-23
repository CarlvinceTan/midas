package hub

import (
	"strings"

	"rsc.io/qr"
)

// RenderQR encodes a QR payload into rows a terminal can print verbatim, one
// character cell per two module rows, using the half-block characters. A client
// with its own QR library can ignore this and encode Payload itself; a TUI or a
// plain terminal prints these rows unchanged, which is the whole point of
// returning them.
//
// The quiet zone is part of the code as far as scanners are concerned, so the
// rows include it rather than leaving it to the caller.
func RenderQR(payload string) []string {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil
	}
	code, err := qr.Encode(trimmed, qr.M)
	if err != nil {
		return nil
	}
	const quiet = 4
	size := code.Size
	rows := make([]string, 0, (size+quiet*2+1)/2)
	var line strings.Builder
	for y := -quiet; y < size+quiet; y += 2 {
		line.Reset()
		for x := -quiet; x < size+quiet; x++ {
			top := inQuiet(x, y, size, quiet) || code.Black(x, y)
			bottom := inQuiet(x, y+1, size, quiet) || code.Black(x, y+1)
			switch {
			case top && bottom:
				line.WriteRune('█')
			case top:
				line.WriteRune('▀')
			case bottom:
				line.WriteRune('▄')
			default:
				line.WriteRune(' ')
			}
		}
		rows = append(rows, strings.TrimRight(line.String(), " "))
	}
	return rows
}

// inQuiet reports whether a coordinate falls in the code's quiet zone, where
// nothing is drawn.
func inQuiet(x, y, size, quiet int) bool {
	return x < 0 || y < 0 || x >= size || y >= size
}
