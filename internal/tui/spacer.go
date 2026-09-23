package tui

// Spacer renders a configurable number of empty rows.
type Spacer struct {
	lines int
}

// NewSpacer constructs a Spacer.
func NewSpacer(lines int) *Spacer { return &Spacer{lines: lines} }

// SetLines changes the number of rows.
func (s *Spacer) SetLines(lines int) { s.lines = lines }

// Invalidate is present to satisfy Component; Spacer has no cached state.
func (s *Spacer) Invalidate() {}

// Render returns empty rows. Zero and negative counts return an empty slice.
func (s *Spacer) Render(_ int) []string {
	result := make([]string, 0, max(0, s.lines))
	for index := 0; index < s.lines; index++ {
		result = append(result, "")
	}
	return result
}
