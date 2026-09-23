package tui

import "math"

const maxSafeInteger = 9007199254740991

// LayoutViewport is the root terminal viewport used by visibility predicates.
type LayoutViewport struct {
	Width  int
	Height int
}

// StackEntryOptions control one child's main-axis allocation. Nil numeric
// fields mean the same thing as an omitted reference option; a nil Basis is
// equivalent to basis "auto".
type StackEntryOptions struct {
	Basis   *int
	Grow    *int
	Shrink  *int
	MinSize *int
	MaxSize *int
	Visible func(LayoutViewport) bool
}

// StackLayoutEntry is the normalized form consumed by the layout allocator.
type StackLayoutEntry struct {
	Component Component
	Basis     *int
	Grow      *int
	Shrink    *int
	MinSize   *int
	MaxSize   *int
	Visible   func(LayoutViewport) bool
}

// StackOptions configure spacing and cross-axis alignment.
type StackOptions struct {
	Gap   int
	Align StackAlignment
}

// StackAlignment controls HStack cross-axis placement.
type StackAlignment string

const (
	AlignStretch StackAlignment = "stretch"
	AlignStart   StackAlignment = "start"
	AlignCenter  StackAlignment = "center"
	AlignEnd     StackAlignment = "end"
)

// StackChild pairs a component with its allocation options.
type StackChild struct {
	Component Component
	Options   StackEntryOptions
}

// NewStackChild constructs a configured stack child.
func NewStackChild(component Component, options StackEntryOptions) StackChild {
	return StackChild{Component: component, Options: options}
}

// Stack contains the shared child bookkeeping for VStack and HStack.
type Stack struct {
	Container
	entries []StackLayoutEntry
	gap     int
	align   StackAlignment
}

func newStack(options StackOptions, children []StackChild) Stack {
	align := options.Align
	if align == "" {
		align = AlignStretch
	}
	stack := Stack{
		Container: Container{Children: []Component{}},
		entries:   []StackLayoutEntry{},
		gap:       max(0, options.Gap),
		align:     align,
	}
	for _, child := range children {
		stack.AddChild(child.Component, child.Options)
	}
	return stack
}

// AddChild appends a child and optional allocation settings.
func (s *Stack) AddChild(component Component, options ...StackEntryOptions) {
	var opts StackEntryOptions
	if len(options) > 0 {
		opts = options[0]
	}
	s.Container.AddChild(component)
	s.entries = append(s.entries, normalizeStackEntry(component, opts))
}

// RemoveChild removes the first identity match from both child lists.
func (s *Stack) RemoveChild(component Component) {
	s.Container.RemoveChild(component)
	for index, entry := range s.entries {
		if sameComponent(entry.Component, component) {
			s.entries = append(s.entries[:index], s.entries[index+1:]...)
			return
		}
	}
}

// Clear removes all stack children and layout entries.
func (s *Stack) Clear() {
	s.Container.Clear()
	s.entries = []StackLayoutEntry{}
}

// Entries returns a copy of the normalized layout entries.
func (s *Stack) Entries() []StackLayoutEntry {
	return append([]StackLayoutEntry(nil), s.entries...)
}

// Gap returns the normalized gap size.
func (s *Stack) Gap() int { return s.gap }

// Alignment returns the configured cross-axis alignment.
func (s *Stack) Alignment() StackAlignment { return s.align }

func normalizeStackEntry(component Component, options StackEntryOptions) StackLayoutEntry {
	return StackLayoutEntry{
		Component: component,
		Basis:     copyNormalized(options.Basis, 0),
		Grow:      copyNormalized(options.Grow, 0),
		Shrink:    copyNormalized(options.Shrink, 1),
		MinSize:   copyNormalized(options.MinSize, 0),
		MaxSize:   copyNormalized(options.MaxSize, maxSafeInteger),
		Visible:   options.Visible,
	}
}

func copyNormalized(value *int, fallback int) *int {
	if value == nil {
		return nil
	}
	normalized := *value
	if normalized < 0 {
		normalized = 0
	}
	// fallback documents the reference renderer's non-finite fallback. Go ints are always
	// finite, so it is not otherwise needed.
	_ = fallback
	return &normalized
}

// VisibleStackEntries filters entries using the root viewport.
func VisibleStackEntries(entries []StackLayoutEntry, viewport LayoutViewport) []StackLayoutEntry {
	visible := make([]StackLayoutEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Visible == nil || entry.Visible(viewport) {
			visible = append(visible, entry)
		}
	}
	return visible
}

func stackValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func clampStackSize(size int, entry StackLayoutEntry) int {
	minimum := max(0, stackValue(entry.MinSize, 0))
	maximum := max(minimum, stackValue(entry.MaxSize, maxSafeInteger))
	return max(minimum, min(maximum, max(0, size)))
}

// AllocateStackSizes implements the reference's ordered weighted distribution.
// Its repeated use of the decreasing remainder intentionally biases ties toward
// earlier entries.
func AllocateStackSizes(entries []StackLayoutEntry, intrinsicSizes []int, availableSize *int, gap int) []int {
	sizes := make([]int, len(entries))
	for index, entry := range entries {
		basis := 0
		if index < len(intrinsicSizes) {
			basis = intrinsicSizes[index]
		}
		if entry.Basis != nil {
			basis = *entry.Basis
		}
		sizes[index] = clampStackSize(basis, entry)
	}
	if availableSize == nil {
		return sizes
	}

	contentSize := max(0, *availableSize-max(0, len(entries)-1)*gap)
	total := 0
	for _, size := range sizes {
		total += size
	}
	if total < contentSize {
		distributeStackSizes(sizes, entries, contentSize-total, true)
	} else if total > contentSize {
		distributeStackSizes(sizes, entries, total-contentSize, false)
	}
	return sizes
}

func distributeStackSizes(sizes []int, entries []StackLayoutEntry, amount int, grow bool) {
	remaining := amount
	for remaining > 0 {
		candidates := make([]int, 0, len(entries))
		totalWeight := 0.0
		for index, entry := range entries {
			if grow {
				if stackValue(entry.Grow, 0) <= 0 || sizes[index] >= stackValue(entry.MaxSize, maxSafeInteger) {
					continue
				}
				candidates = append(candidates, index)
				totalWeight += float64(stackValue(entry.Grow, 0))
				continue
			}
			if stackValue(entry.Shrink, 1) <= 0 || sizes[index] <= stackValue(entry.MinSize, 0) {
				continue
			}
			candidates = append(candidates, index)
			totalWeight += float64(stackValue(entry.Shrink, 1) * max(1, sizes[index]))
		}
		if len(candidates) == 0 {
			return
		}

		distributed := 0
		for _, index := range candidates {
			if remaining <= 0 {
				break
			}
			entry := entries[index]
			weight := float64(stackValue(entry.Grow, 0))
			capacity := stackValue(entry.MaxSize, maxSafeInteger) - sizes[index]
			if !grow {
				weight = float64(stackValue(entry.Shrink, 1) * max(1, sizes[index]))
				capacity = sizes[index] - stackValue(entry.MinSize, 0)
			}
			proposed := max(1, int(math.Floor(float64(remaining)*weight/totalWeight)))
			delta := min(remaining, proposed, capacity)
			if delta <= 0 {
				continue
			}
			if grow {
				sizes[index] += delta
			} else {
				sizes[index] -= delta
			}
			remaining -= delta
			distributed += delta
		}
		if distributed == 0 {
			return
		}
	}
}
