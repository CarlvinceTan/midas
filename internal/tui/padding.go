package tui

import "sync"

// maxPadding keeps Midas usable on a narrow or short terminal: the padding
// never takes more than a few cells from each border.
const maxPadding = 4

// padding is the space Midas keeps between its content and the terminal
// borders. Chat rows are inset by it on the left and right, and the viewport
// reserves the same number of blank rows above the header and below the footer.
var (
	paddingMu  sync.Mutex
	rowPadding = 1
)

// Padding returns the configured border padding. The default of one cell keeps
// the gutter Midas rows have always used.
func Padding() int {
	paddingMu.Lock()
	defer paddingMu.Unlock()
	return rowPadding
}

// SetPadding clamps and stores the border padding, reporting whether it
// changed so callers can repaint only when the layout actually moves.
func SetPadding(value int) bool {
	value = max(0, min(maxPadding, value))
	paddingMu.Lock()
	defer paddingMu.Unlock()
	if rowPadding == value {
		return false
	}
	rowPadding = value
	return true
}
