package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
)

const (
	synchronizedOutputBegin = "\x1b[?2026h"
	synchronizedOutputEnd   = "\x1b[?2026l"
	maxRenderWriteUTF16     = 1024 * 1024
)

// TuiMainScreenOptions configures regular-screen rendering.
type TuiMainScreenOptions struct {
	ShowHardwareCursor bool
	Scheduler          Scheduler
	LogDirectory       string
	IsTermux           func() bool
}

// TuiMainScreenRenderState is the renderer state retained around temporary
// terminal ownership changes. Kitty image IDs are deliberately not retained.
type TuiMainScreenRenderState struct {
	PreviousLines       []string `json:"previousLines"`
	PreviousWidth       int      `json:"previousWidth"`
	PreviousHeight      int      `json:"previousHeight"`
	CursorRow           int      `json:"cursorRow"`
	HardwareCursorRow   int      `json:"hardwareCursorRow"`
	MaxLinesRendered    int      `json:"maxLinesRendered"`
	PreviousViewportTop int      `json:"previousViewportTop"`
}

// TuiMainScreen renders into the terminal's main screen and scrollback.
type TuiMainScreen struct {
	*TuiBase

	previousLines         []string
	previousKittyImageIDs []uint32
	previousWidth         int
	previousHeight        int
	cursorRow             int
	hardwareCursorRow     int
	maxLinesRendered      int
	previousViewportTop   int
	logDirectory          string
	isTermux              func() bool
}

// NewTuiMainScreen constructs a regular-screen renderer.
func NewTuiMainScreen(terminal Terminal, options TuiMainScreenOptions) *TuiMainScreen {
	tui := &TuiMainScreen{
		previousLines: []string{},
		logDirectory:  options.LogDirectory,
		isTermux:      options.IsTermux,
	}
	if tui.isTermux == nil {
		tui.isTermux = func() bool { return os.Getenv("TERMUX_VERSION") != "" }
	}
	tui.TuiBase = NewTuiBase(terminal, ModeRegular, BaseOptions{
		ShowHardwareCursor: options.ShowHardwareCursor,
		Scheduler:          options.Scheduler,
		Hooks: TuiHooks{
			Render:             tui.renderMainScreen,
			ResetRenderState:   tui.resetRenderState,
			BeforeTerminalStop: tui.beforeTerminalStop,
		},
	})
	return tui
}

// CaptureRenderState snapshots renderer state. The returned lines do not alias
// the live renderer.
func (t *TuiMainScreen) CaptureRenderState() TuiMainScreenRenderState {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	previousLines := make([]string, len(t.previousLines))
	copy(previousLines, t.previousLines)
	return TuiMainScreenRenderState{
		PreviousLines:       previousLines,
		PreviousWidth:       t.previousWidth,
		PreviousHeight:      t.previousHeight,
		CursorRow:           t.cursorRow,
		HardwareCursorRow:   t.hardwareCursorRow,
		MaxLinesRendered:    t.maxLinesRendered,
		PreviousViewportTop: t.previousViewportTop,
	}
}

// RestoreRenderState restores a snapshot while blanking image rows and
// forgetting image IDs, matching the reference renderer's placement handling.
func (t *TuiMainScreen) RestoreRenderState(state TuiMainScreenRenderState) {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	t.previousLines = make([]string, len(state.PreviousLines))
	for index, line := range state.PreviousLines {
		if !IsImageLine(line) {
			t.previousLines[index] = line
		}
	}
	t.previousKittyImageIDs = nil
	t.previousWidth = state.PreviousWidth
	t.previousHeight = state.PreviousHeight
	t.cursorRow = state.CursorRow
	t.hardwareCursorRow = state.HardwareCursorRow
	t.maxLinesRendered = state.MaxLinesRendered
	t.previousViewportTop = state.PreviousViewportTop
}

func (t *TuiMainScreen) resetRenderState() {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	t.previousLines = []string{}
	t.previousWidth = -1
	t.previousHeight = -1
	t.cursorRow = 0
	t.hardwareCursorRow = 0
	t.maxLinesRendered = 0
	t.previousViewportTop = 0
}

func (t *TuiMainScreen) beforeTerminalStop(options StopOptions) {
	t.frameMu.Lock()
	defer t.frameMu.Unlock()
	if options.PreserveScreen || len(t.previousLines) == 0 {
		return
	}
	t.Terminal.Write(" ")
	targetRow := len(t.previousLines)
	lineDiff := targetRow - t.hardwareCursorRow
	if lineDiff > 0 {
		t.Terminal.Write(fmt.Sprintf("\x1b[%dB", lineDiff))
	} else if lineDiff < 0 {
		t.Terminal.Write(fmt.Sprintf("\x1b[%dA", -lineDiff))
	}
	t.Terminal.Write("\r\n")
}

type boundedTerminalWriter struct {
	write        func(string)
	buffer       string
	bufferUnits  int
	writtenUnits int
}

func newBoundedTerminalWriter(write func(string)) *boundedTerminalWriter {
	return &boundedTerminalWriter{write: write}
}

func (w *boundedTerminalWriter) append(value string) {
	for len(value) > 0 {
		capacity := maxRenderWriteUTF16 - w.bufferUnits
		if capacity == 0 {
			w.flush()
			continue
		}
		end, units := utf16Prefix(value, capacity)
		if end == 0 {
			w.flush()
			continue
		}
		w.buffer += value[:end]
		w.bufferUnits += units
		value = value[end:]
		if w.bufferUnits == maxRenderWriteUTF16 {
			w.flush()
		}
	}
}

func utf16Prefix(value string, limit int) (end, units int) {
	for end < len(value) {
		r, size := utf8.DecodeRuneInString(value[end:])
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > limit {
			break
		}
		end += size
		units += runeUnits
	}
	return end, units
}

func (w *boundedTerminalWriter) flush() {
	if w.buffer == "" {
		return
	}
	w.write(w.buffer)
	w.writtenUnits += w.bufferUnits
	w.buffer = ""
	w.bufferUnits = 0
}

type kittyImageHeader struct {
	ids  []uint32
	rows uint64
}

func parseKittyImageHeader(line string) (kittyImageHeader, bool) {
	sequenceStart := strings.Index(line, kittyImagePrefix)
	if sequenceStart == -1 {
		return kittyImageHeader{}, false
	}
	paramsStart := sequenceStart + len(kittyImagePrefix)
	paramsEndRelative := strings.IndexByte(line[paramsStart:], ';')
	if paramsEndRelative == -1 {
		return kittyImageHeader{}, false
	}
	paramsEnd := paramsStart + paramsEndRelative
	header := kittyImageHeader{rows: 1}
	for _, param := range strings.Split(line[paramsStart:paramsEnd], ",") {
		separator := strings.IndexByte(param, '=')
		if separator == -1 {
			continue
		}
		key := param[:separator]
		value := param[separator+1:]
		if next := strings.IndexByte(value, '='); next != -1 {
			value = value[:next]
		}
		number, ok := parseJavaScriptPositiveUint32(value)
		if !ok {
			continue
		}
		switch key {
		case "i":
			header.ids = append(header.ids, uint32(number))
		case "r":
			header.rows = number
		}
	}
	return header, true
}

func parseJavaScriptPositiveUint32(value string) (uint64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	var number float64
	var err error
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "0x"):
		var parsed uint64
		parsed, err = strconv.ParseUint(value[2:], 16, 64)
		number = float64(parsed)
	case strings.HasPrefix(lower, "0b"):
		var parsed uint64
		parsed, err = strconv.ParseUint(value[2:], 2, 64)
		number = float64(parsed)
	case strings.HasPrefix(lower, "0o"):
		var parsed uint64
		parsed, err = strconv.ParseUint(value[2:], 8, 64)
		number = float64(parsed)
	default:
		number, err = strconv.ParseFloat(value, 64)
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number > math.MaxUint32 || math.Trunc(number) != number {
		return 0, false
	}
	return uint64(number), true
}

func extractKittyImageIDs(line string) []uint32 {
	header, ok := parseKittyImageHeader(line)
	if !ok {
		return nil
	}
	return header.ids
}

func extractKittyImageRows(line string) uint64 {
	header, ok := parseKittyImageHeader(line)
	if !ok {
		return 1
	}
	return header.rows
}

func (t *TuiMainScreen) collectKittyImageIDs(lines []string) []uint32 {
	seen := make(map[uint32]struct{})
	ids := make([]uint32, 0)
	for _, line := range lines {
		for _, id := range extractKittyImageIDs(line) {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func deleteKittyImages(ids []uint32) string {
	var buffer strings.Builder
	for _, id := range ids {
		buffer.WriteString(DeleteKittyImage(id))
	}
	return buffer.String()
}

func getKittyImageReservedRows(lines []string, index int, maximumIndex ...int) int {
	maxIndex := len(lines) - 1
	if len(maximumIndex) > 0 {
		maxIndex = maximumIndex[0]
	}
	rows := extractKittyImageRows(lineAt(lines, index))
	if rows <= 1 {
		return 1
	}
	maxRows := minUint64(rows, uint64(max(0, maxIndex-index+1)), uint64(max(0, len(lines)-index)))
	reservedRows := 1
	for uint64(reservedRows) < maxRows {
		line := lineAt(lines, index+reservedRows)
		if IsImageLine(line) || tuitext.VisibleWidth(line) > 0 {
			break
		}
		reservedRows++
	}
	return reservedRows
}

func minUint64(values ...uint64) uint64 {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func lineAt(lines []string, index int) string {
	if index < 0 || index >= len(lines) {
		return ""
	}
	return lines[index]
}

func (t *TuiMainScreen) expandChangedRangeForKittyImages(firstChanged, lastChanged int, newLines []string) (int, int) {
	expandedFirst := firstChanged
	expandedLast := lastChanged
	for _, lines := range [][]string{t.previousLines, newLines} {
		for index, line := range lines {
			if len(extractKittyImageIDs(line)) == 0 {
				continue
			}
			blockEnd := index + getKittyImageReservedRows(lines, index) - 1
			if index >= firstChanged || (index <= lastChanged && blockEnd >= firstChanged) {
				expandedFirst = min(expandedFirst, index)
				expandedLast = max(expandedLast, blockEnd)
			}
		}
	}
	return expandedFirst, expandedLast
}

func (t *TuiMainScreen) deleteChangedKittyImages(firstChanged, lastChanged int) string {
	if firstChanged < 0 || lastChanged < firstChanged {
		return ""
	}
	seen := make(map[uint32]struct{})
	ids := make([]uint32, 0)
	maxLine := min(lastChanged, len(t.previousLines)-1)
	for index := firstChanged; index <= maxLine; index++ {
		for _, id := range extractKittyImageIDs(lineAt(t.previousLines, index)) {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return deleteKittyImages(ids)
}

type renderWidthError struct {
	message string
}

func (t *TuiMainScreen) renderMainScreen() {
	t.frameMu.Lock()
	renderError := t.renderMainScreenLocked()
	t.frameMu.Unlock()
	if renderError != nil {
		t.TuiBase.Stop(StopOptions{})
		panic(renderError.message)
	}
}

func (t *TuiMainScreen) renderMainScreenLocked() *renderWidthError {
	if t.isStopped() {
		return nil
	}
	width := t.Terminal.Columns()
	height := t.Terminal.Rows()
	widthChanged := t.previousWidth != 0 && t.previousWidth != width
	heightChanged := t.previousHeight != 0 && t.previousHeight != height
	previousBufferLength := height
	if t.previousHeight > 0 {
		previousBufferLength = t.previousViewportTop + t.previousHeight
	}
	prevViewportTop := t.previousViewportTop
	if heightChanged {
		prevViewportTop = max(0, previousBufferLength-height)
	}
	viewportTop := prevViewportTop
	hardwareCursorRow := t.hardwareCursorRow
	computeLineDiff := func(targetRow int) int {
		currentScreenRow := hardwareCursorRow - prevViewportTop
		targetScreenRow := targetRow - viewportTop
		return targetScreenRow - currentScreenRow
	}

	newLines := t.TuiBase.Container.Render(width)
	if t.HasOverlayEntries() {
		newLines = t.CompositeOverlays(newLines, width, height)
	}
	cursorPos, hasCursor := t.ExtractCursorPosition(newLines, height)
	newLines = t.ApplyLineResets(newLines)

	fullRender := func(clear bool) {
		t.IncrementFullRedraws()
		output := newBoundedTerminalWriter(t.Terminal.Write)
		output.append(synchronizedOutputBegin)
		if clear {
			output.append(deleteKittyImages(t.previousKittyImageIDs))
			output.append("\x1b[2J\x1b[H\x1b[3J")
		}
		for index := 0; index < len(newLines); index++ {
			if index > 0 {
				output.append("\r\n")
			}
			line := newLines[index]
			isImage := IsImageLine(line)
			imageReservedRows := 1
			if isImage {
				imageReservedRows = getKittyImageReservedRows(newLines, index)
			}
			if imageReservedRows > 1 && imageReservedRows <= height {
				for row := 1; row < imageReservedRows; row++ {
					output.append("\r\n")
				}
				output.append(fmt.Sprintf("\x1b[%dA", imageReservedRows-1))
				output.append(line)
				output.append(fmt.Sprintf("\x1b[%dB", imageReservedRows-1))
				index += imageReservedRows - 1
				continue
			}
			output.append(line)
		}
		output.append(synchronizedOutputEnd)
		output.flush()
		t.cursorRow = max(0, len(newLines)-1)
		t.hardwareCursorRow = t.cursorRow
		if clear {
			t.maxLinesRendered = len(newLines)
		} else {
			t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
		}
		bufferLength := max(height, len(newLines))
		t.previousViewportTop = max(0, bufferLength-height)
		t.positionHardwareCursor(cursorPos, hasCursor, len(newLines))
		t.previousLines = newLines
		t.previousKittyImageIDs = t.collectKittyImageIDs(newLines)
		t.previousWidth = width
		t.previousHeight = height
	}

	logRedraw := func(reason string) {
		if os.Getenv("MIDAS_TUI_DEBUG_REDRAW") != "1" || t.logDirectory == "" {
			return
		}
		logPath := filepath.Join(t.logDirectory, "midas-tui-debug.log")
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		message := fmt.Sprintf("[%s] fullRender: %s (prev=%d, new=%d, height=%d)\n",
			time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), reason, len(t.previousLines), len(newLines), height)
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = file.WriteString(message)
			_ = file.Close()
		}
	}

	if len(t.previousLines) == 0 && !widthChanged && !heightChanged {
		logRedraw("first render")
		fullRender(false)
		return nil
	}
	if widthChanged {
		logRedraw(fmt.Sprintf("terminal width changed (%d -> %d)", t.previousWidth, width))
		fullRender(true)
		return nil
	}
	if heightChanged && !t.isTermux() {
		logRedraw(fmt.Sprintf("terminal height changed (%d -> %d)", t.previousHeight, height))
		fullRender(true)
		return nil
	}
	if t.ClearOnShrink() && len(newLines) < t.maxLinesRendered && !t.HasOverlayEntries() {
		logRedraw(fmt.Sprintf("clearOnShrink (maxLinesRendered=%d)", t.maxLinesRendered))
		fullRender(true)
		return nil
	}

	firstChanged := -1
	lastChanged := -1
	maxLines := max(len(newLines), len(t.previousLines))
	for index := 0; index < maxLines; index++ {
		if lineAt(t.previousLines, index) != lineAt(newLines, index) {
			if firstChanged == -1 {
				firstChanged = index
			}
			lastChanged = index
		}
	}
	appendedLines := len(newLines) > len(t.previousLines)
	if appendedLines {
		if firstChanged == -1 {
			firstChanged = len(t.previousLines)
		}
		lastChanged = len(newLines) - 1
	}
	if firstChanged != -1 {
		firstChanged, lastChanged = t.expandChangedRangeForKittyImages(firstChanged, lastChanged, newLines)
	}
	appendStart := appendedLines && firstChanged == len(t.previousLines) && firstChanged > 0

	if firstChanged == -1 {
		t.positionHardwareCursor(cursorPos, hasCursor, len(newLines))
		t.previousViewportTop = prevViewportTop
		t.previousHeight = height
		return nil
	}

	if firstChanged >= len(newLines) {
		if len(t.previousLines) > len(newLines) {
			output := newBoundedTerminalWriter(t.Terminal.Write)
			output.append(synchronizedOutputBegin)
			output.append(t.deleteChangedKittyImages(firstChanged, lastChanged))
			targetRow := max(0, len(newLines)-1)
			if targetRow < prevViewportTop {
				logRedraw(fmt.Sprintf("deleted lines moved viewport up (%d < %d)", targetRow, prevViewportTop))
				fullRender(true)
				return nil
			}
			lineDiff := computeLineDiff(targetRow)
			if lineDiff > 0 {
				output.append(fmt.Sprintf("\x1b[%dB", lineDiff))
			} else if lineDiff < 0 {
				output.append(fmt.Sprintf("\x1b[%dA", -lineDiff))
			}
			output.append("\r")
			extraLines := len(t.previousLines) - len(newLines)
			if extraLines > height {
				logRedraw(fmt.Sprintf("extraLines > height (%d > %d)", extraLines, height))
				fullRender(true)
				return nil
			}
			clearStartOffset := 0
			if len(newLines) != 0 {
				clearStartOffset = 1
			}
			if extraLines > 0 && clearStartOffset > 0 {
				output.append(fmt.Sprintf("\x1b[%dB", clearStartOffset))
			}
			for index := 0; index < extraLines; index++ {
				output.append("\r\x1b[2K")
				if index < extraLines-1 {
					output.append("\x1b[1B")
				}
			}
			moveBack := max(0, extraLines-1+clearStartOffset)
			if moveBack > 0 {
				output.append(fmt.Sprintf("\x1b[%dA", moveBack))
			}
			output.append(synchronizedOutputEnd)
			output.flush()
			t.cursorRow = targetRow
			t.hardwareCursorRow = targetRow
		}
		t.positionHardwareCursor(cursorPos, hasCursor, len(newLines))
		t.previousLines = newLines
		t.previousKittyImageIDs = t.collectKittyImageIDs(newLines)
		t.previousWidth = width
		t.previousHeight = height
		t.previousViewportTop = prevViewportTop
		return nil
	}

	if firstChanged < prevViewportTop {
		logRedraw(fmt.Sprintf("firstChanged < viewportTop (%d < %d)", firstChanged, prevViewportTop))
		fullRender(true)
		return nil
	}

	output := newBoundedTerminalWriter(t.Terminal.Write)
	output.append(synchronizedOutputBegin)
	output.append(t.deleteChangedKittyImages(firstChanged, lastChanged))
	prevViewportBottom := prevViewportTop + height - 1
	moveTargetRow := firstChanged
	if appendStart {
		moveTargetRow = firstChanged - 1
	}
	if moveTargetRow > prevViewportBottom {
		currentScreenRow := max(0, min(height-1, hardwareCursorRow-prevViewportTop))
		moveToBottom := height - 1 - currentScreenRow
		if moveToBottom > 0 {
			output.append(fmt.Sprintf("\x1b[%dB", moveToBottom))
		}
		scroll := moveTargetRow - prevViewportBottom
		output.append(strings.Repeat("\r\n", scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTargetRow
	}

	lineDiff := computeLineDiff(moveTargetRow)
	if lineDiff > 0 {
		output.append(fmt.Sprintf("\x1b[%dB", lineDiff))
	} else if lineDiff < 0 {
		output.append(fmt.Sprintf("\x1b[%dA", -lineDiff))
	}
	if appendStart {
		output.append("\r\n")
	} else {
		output.append("\r")
	}

	renderEnd := min(lastChanged, len(newLines)-1)
	for index := firstChanged; index <= renderEnd; index++ {
		if index > firstChanged {
			output.append("\r\n")
		}
		line := newLines[index]
		isImage := IsImageLine(line)
		imageReservedRows := 1
		if isImage {
			imageReservedRows = getKittyImageReservedRows(newLines, index, renderEnd)
		}
		if imageReservedRows > 1 {
			imageStartScreenRow := index - viewportTop
			if imageStartScreenRow < 0 || imageStartScreenRow+imageReservedRows > height {
				logRedraw(fmt.Sprintf("kitty image pre-clear would scroll (%d + %d > %d)", imageStartScreenRow, imageReservedRows, height))
				fullRender(true)
				return nil
			}
			output.append("\x1b[2K")
			for row := 1; row < imageReservedRows; row++ {
				output.append("\r\n\x1b[2K")
			}
			output.append(fmt.Sprintf("\x1b[%dA", imageReservedRows-1))
			output.append(line)
			output.append(fmt.Sprintf("\x1b[%dB", imageReservedRows-1))
			index += imageReservedRows - 1
			continue
		}

		output.append("\x1b[2K")
		lineWidth := tuitext.VisibleWidth(line)
		if !isImage && lineWidth > width {
			crashLogPath := filepath.Join(os.TempDir(), "midas-tui-crash.log")
			if t.logDirectory != "" {
				crashLogPath = filepath.Join(t.logDirectory, "midas-tui-crash.log")
			}
			var crashData strings.Builder
			fmt.Fprintf(&crashData, "Crash at %s\nTerminal width: %d\nLine %d visible width: %d\n\n=== All rendered lines ===\n",
				time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), width, index, lineWidth)
			for lineIndex, renderedLine := range newLines {
				fmt.Fprintf(&crashData, "[%d] (w=%d) %s\n", lineIndex, tuitext.VisibleWidth(renderedLine), renderedLine)
			}
			crashData.WriteByte('\n')
			_ = os.MkdirAll(filepath.Dir(crashLogPath), 0o755)
			_ = os.WriteFile(crashLogPath, []byte(crashData.String()), 0o644)
			return &renderWidthError{message: fmt.Sprintf(
				"Rendered line %d exceeds terminal width (%d > %d).\n\nThis is likely caused by a custom TUI component not truncating its output.\nUse visibleWidth() to measure and truncateToWidth() to truncate lines.\n\nDebug log written to: %s",
				index, lineWidth, width, crashLogPath)}
		}
		output.append(line)
	}

	finalCursorRow := renderEnd
	if len(t.previousLines) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			moveDown := len(newLines) - 1 - renderEnd
			output.append(fmt.Sprintf("\x1b[%dB", moveDown))
			finalCursorRow = len(newLines) - 1
		}
		extraLines := len(t.previousLines) - len(newLines)
		for index := len(newLines); index < len(t.previousLines); index++ {
			output.append("\r\n\x1b[2K")
		}
		output.append(fmt.Sprintf("\x1b[%dA", extraLines))
	}
	output.append(synchronizedOutputEnd)
	output.flush()

	t.cursorRow = max(0, len(newLines)-1)
	t.hardwareCursorRow = finalCursorRow
	t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
	t.previousViewportTop = max(prevViewportTop, finalCursorRow-height+1)
	t.positionHardwareCursor(cursorPos, hasCursor, len(newLines))
	t.previousLines = newLines
	t.previousKittyImageIDs = t.collectKittyImageIDs(newLines)
	t.previousWidth = width
	t.previousHeight = height
	return nil
}

func (t *TuiMainScreen) positionHardwareCursor(cursorPos CursorPosition, hasCursor bool, totalLines int) {
	if !hasCursor || totalLines <= 0 {
		t.Terminal.HideCursor()
		return
	}
	targetRow := max(0, min(cursorPos.Row, totalLines-1))
	targetCol := max(0, cursorPos.Col)
	rowDelta := targetRow - t.hardwareCursorRow
	var buffer strings.Builder
	if rowDelta > 0 {
		fmt.Fprintf(&buffer, "\x1b[%dB", rowDelta)
	} else if rowDelta < 0 {
		fmt.Fprintf(&buffer, "\x1b[%dA", -rowDelta)
	}
	fmt.Fprintf(&buffer, "\x1b[%dG", targetCol+1)
	t.Terminal.Write(buffer.String())
	t.hardwareCursorRow = targetRow
	if t.ShowHardwareCursor() {
		t.Terminal.ShowCursor()
	} else {
		t.Terminal.HideCursor()
	}
}
