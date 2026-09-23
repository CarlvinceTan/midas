package tui

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultInputSequenceTimeout = 50 * time.Millisecond
	defaultInputEscapeTimeout   = 10 * time.Millisecond
	bracketedPasteStart         = "\x1b[200~"
	bracketedPasteEnd           = "\x1b[201~"
)

type inputSequenceStatus uint8

const (
	inputNotEscape inputSequenceStatus = iota
	inputIncomplete
	inputComplete
)

type stdinBufferEvent struct {
	data       string
	paste      bool
	generation uint64
}

type stdinBuffer struct {
	mu                       sync.Mutex
	dispatchMu               sync.Mutex
	buffer                   string
	timer                    *time.Timer
	timerGeneration          uint64
	eventGeneration          uint64
	timeout                  time.Duration
	escapeTimeout            time.Duration
	pasteMode                bool
	pasteBuffer              string
	pendingKittyPrintable    rune
	hasPendingKittyPrintable bool
	onData                   func(string)
	onPaste                  func(string)
}

func newStdinBuffer(timeout, escapeTimeout time.Duration, onData, onPaste func(string)) *stdinBuffer {
	if timeout <= 0 {
		timeout = defaultInputSequenceTimeout
	}
	if escapeTimeout <= 0 {
		escapeTimeout = defaultInputEscapeTimeout
	}
	if onData == nil {
		onData = func(string) {}
	}
	if onPaste == nil {
		onPaste = func(string) {}
	}
	return &stdinBuffer{timeout: timeout, escapeTimeout: escapeTimeout, onData: onData, onPaste: onPaste}
}

func completeInputSequence(data string) inputSequenceStatus {
	if !strings.HasPrefix(data, "\x1b") {
		return inputNotEscape
	}
	if data == "\x1b" {
		return inputIncomplete
	}
	after := data[1:]
	if strings.HasPrefix(after, "[") {
		if strings.HasPrefix(after, "[M") {
			if len(data) >= 6 {
				return inputComplete
			}
			return inputIncomplete
		}
		if len(data) < 3 {
			return inputIncomplete
		}
		payload := data[2:]
		last := payload[len(payload)-1]
		if last < 0x40 || last > 0x7e {
			return inputIncomplete
		}
		if strings.HasPrefix(payload, "<") {
			if (last == 'M' || last == 'm') && sgrMousePattern.MatchString("\x1b["+payload) {
				return inputComplete
			}
			return inputIncomplete
		}
		return inputComplete
	}
	if strings.HasPrefix(after, "]") {
		if strings.HasSuffix(data, "\x1b\\") || strings.HasSuffix(data, "\x07") {
			return inputComplete
		}
		return inputIncomplete
	}
	if strings.HasPrefix(after, "P") || strings.HasPrefix(after, "_") {
		if strings.HasSuffix(data, "\x1b\\") {
			return inputComplete
		}
		return inputIncomplete
	}
	if strings.HasPrefix(after, "O") {
		_, size := utf8.DecodeRuneInString(after[1:])
		if size > 0 && len(after) >= 1+size {
			return inputComplete
		}
		return inputIncomplete
	}
	_, size := utf8.DecodeRuneInString(after)
	if size == 0 || size == 1 && after[0] >= utf8.RuneSelf && !utf8.FullRuneInString(after) {
		return inputIncomplete
	}
	return inputComplete
}

func runeBoundaries(value string, start int) []int {
	boundaries := make([]int, 0, len(value)-start)
	for index := start; index < len(value); {
		_, size := utf8.DecodeRuneInString(value[index:])
		if size <= 0 || (size == 1 && value[index] >= utf8.RuneSelf && !utf8.FullRuneInString(value[index:])) {
			break
		}
		index += size
		boundaries = append(boundaries, index)
	}
	return boundaries
}

func extractCompleteInputSequences(buffer string) ([]string, string) {
	sequences := make([]string, 0)
	for position := 0; position < len(buffer); {
		remaining := buffer[position:]
		if remaining[0] != '\x1b' {
			_, size := utf8.DecodeRuneInString(remaining)
			if size == 1 && remaining[0] >= utf8.RuneSelf && !utf8.FullRuneInString(remaining) {
				return sequences, remaining
			}
			sequences = append(sequences, remaining[:size])
			position += size
			continue
		}
		completed := false
		for _, end := range runeBoundaries(remaining, 0) {
			candidate := remaining[:end]
			switch completeInputSequence(candidate) {
			case inputComplete:
				if candidate == "\x1b\x1b" && end < len(remaining) && strings.ContainsRune("[]OP_", rune(remaining[end])) {
					sequences = append(sequences, "\x1b")
					position++
				} else {
					sequences = append(sequences, candidate)
					position += end
				}
				completed = true
			case inputIncomplete:
				continue
			default:
				sequences = append(sequences, candidate)
				position += end
				completed = true
			}
			if completed {
				break
			}
		}
		if !completed {
			return sequences, remaining
		}
	}
	return sequences, ""
}

var unmodifiedKittyPrintablePattern = regexp.MustCompile(`^\x1b\[(\d+)(?::\d*)?(?::\d+)?u$`)

func unmodifiedKittyPrintable(sequence string) (rune, bool) {
	match := unmodifiedKittyPrintablePattern.FindStringSubmatch(sequence)
	if len(match) != 2 {
		return 0, false
	}
	codepoint, err := strconv.Atoi(match[1])
	return rune(codepoint), err == nil && codepoint >= 32 && codepoint <= utf8.MaxRune
}

func (b *stdinBuffer) emitDataLocked(sequence string, events *[]stdinBufferEvent) {
	if b.hasPendingKittyPrintable {
		characters := []rune(sequence)
		if len(characters) == 1 && characters[0] == b.pendingKittyPrintable {
			b.hasPendingKittyPrintable = false
			return
		}
	}
	b.pendingKittyPrintable, b.hasPendingKittyPrintable = unmodifiedKittyPrintable(sequence)
	*events = append(*events, stdinBufferEvent{data: sequence, generation: b.eventGeneration})
}

func (b *stdinBuffer) stopTimerLocked() {
	b.timerGeneration++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

func (b *stdinBuffer) processLocked(data string, events *[]stdinBufferEvent) {
	b.stopTimerLocked()
	if data == "" && b.buffer == "" {
		b.emitDataLocked("", events)
		return
	}
	b.buffer += data
	for {
		if b.pasteMode {
			b.pasteBuffer += b.buffer
			b.buffer = ""
			end := strings.Index(b.pasteBuffer, bracketedPasteEnd)
			if end < 0 {
				return
			}
			content := b.pasteBuffer[:end]
			remaining := b.pasteBuffer[end+len(bracketedPasteEnd):]
			b.pasteMode, b.pasteBuffer, b.hasPendingKittyPrintable = false, "", false
			*events = append(*events, stdinBufferEvent{data: content, paste: true, generation: b.eventGeneration})
			if remaining == "" {
				return
			}
			b.buffer = remaining
			continue
		}
		if start := strings.Index(b.buffer, bracketedPasteStart); start >= 0 {
			if start > 0 {
				sequences, _ := extractCompleteInputSequences(b.buffer[:start])
				for _, sequence := range sequences {
					b.emitDataLocked(sequence, events)
				}
			}
			b.hasPendingKittyPrintable = false
			b.buffer = b.buffer[start+len(bracketedPasteStart):]
			b.pasteMode, b.pasteBuffer = true, b.buffer
			b.buffer = ""
			continue
		}
		sequences, remainder := extractCompleteInputSequences(b.buffer)
		b.buffer = remainder
		for _, sequence := range sequences {
			b.emitDataLocked(sequence, events)
		}
		if b.buffer != "" {
			// An incomplete sequence waits for the rest of it, and a lone escape
			// waits only for the escape timeout so the escape key stays responsive.
			// A longer configured escape timeout therefore also covers a terminal
			// that splits a key sequence across reads, which is what a slow link or a
			// multiplexer can do.
			delay := max(b.timeout, b.escapeTimeout)
			if b.buffer == "\x1b" {
				delay = b.escapeTimeout
			}
			generation := b.timerGeneration
			b.timer = time.AfterFunc(delay, func() { b.flushTimer(generation) })
		}
		return
	}
}

func (b *stdinBuffer) dispatch(events []stdinBufferEvent) {
	for _, event := range events {
		b.mu.Lock()
		current := event.generation == b.eventGeneration
		b.mu.Unlock()
		if !current {
			continue
		}
		if event.paste {
			b.onPaste(event.data)
		} else {
			b.onData(event.data)
		}
	}
}

func (b *stdinBuffer) Process(data []byte) {
	b.dispatchMu.Lock()
	defer b.dispatchMu.Unlock()
	events := make([]stdinBufferEvent, 0)
	b.mu.Lock()
	b.processLocked(string(data), &events)
	b.mu.Unlock()
	b.dispatch(events)
}

func (b *stdinBuffer) flushTimer(generation uint64) {
	b.dispatchMu.Lock()
	defer b.dispatchMu.Unlock()
	events := make([]stdinBufferEvent, 0, 1)
	b.mu.Lock()
	if generation != b.timerGeneration {
		b.mu.Unlock()
		return
	}
	b.timer = nil
	if b.buffer != "" {
		sequence := b.buffer
		b.buffer = ""
		b.hasPendingKittyPrintable = false
		events = append(events, stdinBufferEvent{data: sequence, generation: b.eventGeneration})
	}
	b.mu.Unlock()
	b.dispatch(events)
}

func (b *stdinBuffer) Clear() {
	b.mu.Lock()
	b.stopTimerLocked()
	b.buffer, b.pasteBuffer, b.pasteMode, b.hasPendingKittyPrintable = "", "", false, false
	b.mu.Unlock()
}

func (b *stdinBuffer) discardPendingEvents() {
	b.mu.Lock()
	b.stopTimerLocked()
	b.eventGeneration++
	b.buffer, b.pasteBuffer, b.pasteMode, b.hasPendingKittyPrintable = "", "", false, false
	b.mu.Unlock()
}

func (b *stdinBuffer) Buffer() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer }
func (b *stdinBuffer) Destroy()       { b.Clear() }
