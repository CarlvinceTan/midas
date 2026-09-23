package tui

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

const (
	DefaultDrainMax            = time.Second
	DefaultDrainIdle           = 50 * time.Millisecond
	keyboardNegotiationTimeout = 150 * time.Millisecond
	progressKeepalive          = time.Second
	progressActiveSequence     = "\x1b]9;4;3\x07"
	progressClearSequence      = "\x1b]9;4;0\x07"
	kittyKeyboardProtocolQuery = "\x1b[>7u\x1b[?u\x1b[c"
	modifyOtherKeysEnable      = "\x1b[>4;2m"
	modifyOtherKeysDisable     = "\x1b[>4;0m"
	kittyKeyboardProtocolPop   = "\x1b[<u"
)

var (
	kittyFlagsResponsePattern = regexp.MustCompile(`^\x1b\[\?(\d+)u$`)
	deviceAttributesPattern   = regexp.MustCompile(`^\x1b\[\?[\d;]*c$`)
	negotiationPrefixPattern  = regexp.MustCompile(`^\x1b\[\?[\d;]*$`)
)

type keyboardNegotiationKind uint8

const (
	negotiationNone keyboardNegotiationKind = iota
	negotiationKitty
	negotiationDeviceAttributes
)

type terminalInputChunk struct {
	generation      uint64
	drainGeneration uint64
	data            []byte
}

type terminalInputSession struct {
	generation uint64
	eventMu    sync.Mutex
	active     atomic.Bool
}

// ProcessTerminal is the production terminal backed by the process stdin/stdout.
type ProcessTerminal struct {
	stdin  *os.File
	stdout *os.File

	lifecycleMu            sync.Mutex
	drainMu                sync.Mutex
	eventMu                sync.Mutex
	stateMu                sync.Mutex
	writeMu                sync.Mutex
	generation             uint64
	session                *terminalInputSession
	started                bool
	onInput                func(string)
	onResize               func()
	oldState               *term.State
	stdinBuffer            *stdinBuffer
	inputData              chan terminalInputChunk
	stopInput              chan struct{}
	drainSignal            chan struct{}
	draining               bool
	drainGeneration        uint64
	stopReader             func()
	stopResize             func()
	restorePlatform        func() error
	lastError              error
	kittyProtocolActive    bool
	modifyOtherKeysActive  bool
	keyboardProtocolPushed bool
	negotiationBuffer      string
	negotiationTimer       *time.Timer
	negotiationGeneration  uint64
	activity               chan struct{}

	progressStop chan struct{}
	progressDone chan struct{}
	writeLogPath string
}

// NewProcessTerminal constructs a process-backed terminal and installs detected capabilities.
func NewProcessTerminal() *ProcessTerminal {
	terminal := newProcessTerminal(os.Stdin, os.Stdout)
	SetCapabilities(DetectTerminalCapabilities())
	return terminal
}

func newProcessTerminal(stdin, stdout *os.File) *ProcessTerminal {
	return &ProcessTerminal{
		stdin: stdin, stdout: stdout, activity: make(chan struct{}, 1),
		writeLogPath: resolveTerminalWriteLogPath(os.Getenv("MIDAS_TUI_WRITE_LOG")),
	}
}

func resolveTerminalWriteLogPath(value string) string {
	if value == "" {
		return ""
	}
	if info, err := os.Stat(value); err == nil && info.IsDir() {
		return filepath.Join(value, fmt.Sprintf("tui-%s-%d.log", time.Now().Format("2006-01-02_15-04-05"), os.Getpid()))
	}
	return value
}

func resolveEscapeTimeout() time.Duration {
	if configured, err := strconv.ParseFloat(os.Getenv("MIDAS_TUI_ESC_TIMEOUT"), 64); err == nil && configured > 0 && !math.IsInf(configured, 0) {
		return time.Duration(configured * float64(time.Millisecond))
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return 100 * time.Millisecond
	}
	return defaultInputEscapeTimeout
}

func (t *ProcessTerminal) Start(onInput func(string), onResize func()) {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	t.stateMu.Lock()
	if t.started {
		t.stateMu.Unlock()
		return
	}
	if t.oldState != nil {
		if err := term.Restore(int(t.stdin.Fd()), t.oldState); err != nil {
			t.lastError = err
			t.stateMu.Unlock()
			panic(err)
		}
		t.oldState = nil
	}
	t.generation++
	generation := t.generation
	session := &terminalInputSession{generation: generation}
	session.active.Store(true)
	t.session = session
	t.started = true
	t.lastError = nil
	t.onInput, t.onResize = onInput, onResize
	if term.IsTerminal(int(t.stdin.Fd())) {
		state, err := term.MakeRaw(int(t.stdin.Fd()))
		if err != nil {
			t.started = false
			session.active.Store(false)
			t.session = nil
			t.onInput, t.onResize = nil, nil
			t.lastError = err
			t.stateMu.Unlock()
			panic(err)
		}
		t.oldState = state
	}
	t.stdinBuffer = newStdinBuffer(defaultInputSequenceTimeout, resolveEscapeTimeout(), func(sequence string) {
		t.processInputSequenceForSession(session, sequence)
	}, func(content string) {
		t.forwardInputForSession(session, bracketedPasteStart+content+bracketedPasteEnd)
	})
	t.inputData = make(chan terminalInputChunk, 64)
	t.stopInput = make(chan struct{})
	t.drainSignal = make(chan struct{})
	t.draining = false
	inputData, stopInput := t.inputData, t.stopInput
	go t.runInputWorker(session, inputData, stopInput)
	t.stateMu.Unlock()

	restorePlatform := enableTerminalVT(t.stdin, t.stdout)
	t.stateMu.Lock()
	t.restorePlatform = restorePlatform
	t.stateMu.Unlock()
	t.directWrite("\x1b[?2004h")
	t.stateMu.Lock()
	t.stopResize = startTerminalResizeWatcher(t.stdout, func() { t.handleResize(session) })
	t.stateMu.Unlock()
	refreshTerminalDimensions()
	stopReader, err := startTerminalInputReader(t.stdin, func(data []byte) { t.handleRawInput(session, data) })
	if err != nil {
		t.stopLocked()
		panic(err)
	}
	t.stateMu.Lock()
	if !t.started || t.session != session {
		t.stateMu.Unlock()
		stopReader()
		return
	}
	t.stopReader = stopReader
	t.keyboardProtocolPushed = true
	t.clearNegotiationBufferLocked()
	t.directWrite(kittyKeyboardProtocolQuery)
	t.stateMu.Unlock()
}

func (t *ProcessTerminal) handleRawInput(session *terminalInputSession, data []byte) {
	t.stateMu.Lock()
	started := t.started && t.session == session
	inputData, stopInput, drainSignal, draining, drainGeneration := t.inputData, t.stopInput, t.drainSignal, t.draining, t.drainGeneration
	t.stateMu.Unlock()
	if !started || inputData == nil {
		return
	}
	select {
	case t.activity <- struct{}{}:
	default:
	}
	if draining {
		return
	}
	chunk := terminalInputChunk{generation: session.generation, drainGeneration: drainGeneration, data: append([]byte(nil), data...)}
	select {
	case inputData <- chunk:
	case <-stopInput:
	case <-drainSignal:
	}
}

func (t *ProcessTerminal) runInputWorker(session *terminalInputSession, inputData <-chan terminalInputChunk, stopInput <-chan struct{}) {
	for {
		select {
		case chunk := <-inputData:
			t.stateMu.Lock()
			buffer := t.stdinBuffer
			active := t.started && t.session == session && chunk.generation == session.generation && chunk.drainGeneration == t.drainGeneration && !t.draining
			t.stateMu.Unlock()
			if active && buffer != nil {
				buffer.Process(chunk.data)
			}
		case <-stopInput:
			return
		}
	}
}

func (t *ProcessTerminal) handleResize(session *terminalInputSession) {
	t.stateMu.Lock()
	handler, started := t.onResize, t.started && t.session == session
	t.stateMu.Unlock()
	if started && session.active.Load() && handler != nil {
		handler()
	}
}

func parseKeyboardNegotiation(sequence string) (keyboardNegotiationKind, int) {
	if match := kittyFlagsResponsePattern.FindStringSubmatch(sequence); len(match) == 2 {
		flags, _ := strconv.Atoi(match[1])
		return negotiationKitty, flags
	}
	if deviceAttributesPattern.MatchString(sequence) {
		return negotiationDeviceAttributes, 0
	}
	return negotiationNone, 0
}

func isKeyboardNegotiationPrefix(sequence string) bool {
	return sequence == "\x1b[" || negotiationPrefixPattern.MatchString(sequence)
}

func (t *ProcessTerminal) processInputSequence(sequence string) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	t.processInputSequenceLocked(sequence, nil, false)
}

func (t *ProcessTerminal) processInputSequenceForSession(session *terminalInputSession, sequence string) {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	t.processInputSequenceLocked(sequence, session, true)
}

func (t *ProcessTerminal) processInputSequenceLocked(sequence string, session *terminalInputSession, requireActive bool) {
	t.stateMu.Lock()
	if requireActive && (!t.started || t.session != session) {
		t.stateMu.Unlock()
		return
	}
	buffered := t.negotiationBuffer
	if buffered != "" {
		combined := buffered + sequence
		if kind, flags := parseKeyboardNegotiation(combined); kind != negotiationNone {
			t.clearNegotiationBufferLocked()
			t.handleNegotiationLocked(kind, flags)
			t.stateMu.Unlock()
			return
		}
		if isKeyboardNegotiationPrefix(combined) {
			t.setNegotiationBufferLocked(combined)
			t.scheduleNegotiationFlushLocked(session, requireActive)
			t.stateMu.Unlock()
			return
		}
		t.clearNegotiationBufferLocked()
		handler := t.onInput
		t.stateMu.Unlock()
		if (!requireActive || session.active.Load()) && handler != nil {
			t.forwardInputTo(handler, buffered)
		}
		t.stateMu.Lock()
		if requireActive && (!t.started || t.session != session) {
			t.stateMu.Unlock()
			return
		}
	}
	if kind, flags := parseKeyboardNegotiation(sequence); kind != negotiationNone {
		t.clearNegotiationBufferLocked()
		t.handleNegotiationLocked(kind, flags)
		t.stateMu.Unlock()
		return
	}
	if isKeyboardNegotiationPrefix(sequence) {
		t.setNegotiationBufferLocked(sequence)
		t.scheduleNegotiationFlushLocked(session, requireActive)
		t.stateMu.Unlock()
		return
	}
	handler := t.onInput
	t.stateMu.Unlock()
	if (!requireActive || session.active.Load()) && handler != nil {
		t.forwardInputTo(handler, sequence)
	}
}

func (t *ProcessTerminal) handleNegotiationLocked(kind keyboardNegotiationKind, flags int) {
	if kind == negotiationKitty {
		if flags != 0 {
			t.disableModifyOtherKeysLocked()
			if !t.kittyProtocolActive {
				t.kittyProtocolActive = true
				SetKittyProtocolActive(true)
			}
		} else {
			t.enableModifyOtherKeysLocked()
		}
		return
	}
	if !t.kittyProtocolActive {
		t.enableModifyOtherKeysLocked()
	}
}

func (t *ProcessTerminal) setNegotiationBufferLocked(sequence string) {
	t.clearNegotiationTimerLocked()
	t.negotiationBuffer = sequence
}

func (t *ProcessTerminal) clearNegotiationTimerLocked() {
	t.negotiationGeneration++
	if t.negotiationTimer != nil {
		t.negotiationTimer.Stop()
		t.negotiationTimer = nil
	}
}

func (t *ProcessTerminal) clearNegotiationBufferLocked() {
	t.clearNegotiationTimerLocked()
	t.negotiationBuffer = ""
}

func (t *ProcessTerminal) scheduleNegotiationFlushLocked(session *terminalInputSession, requireActive bool) {
	if t.negotiationBuffer == "" || t.negotiationTimer != nil {
		return
	}
	timerGeneration := t.negotiationGeneration
	t.negotiationTimer = time.AfterFunc(keyboardNegotiationTimeout, func() {
		if session != nil {
			session.eventMu.Lock()
			defer session.eventMu.Unlock()
		} else {
			t.eventMu.Lock()
			defer t.eventMu.Unlock()
		}
		t.stateMu.Lock()
		if timerGeneration != t.negotiationGeneration || requireActive && (!t.started || t.session != session) {
			t.stateMu.Unlock()
			return
		}
		sequence, handler := t.negotiationBuffer, t.onInput
		t.negotiationBuffer, t.negotiationTimer = "", nil
		t.negotiationGeneration++
		t.stateMu.Unlock()
		if sequence != "" && (!requireActive || session.active.Load()) && handler != nil {
			t.forwardInputTo(handler, sequence)
		}
	})
}

func (t *ProcessTerminal) forwardInputForSession(session *terminalInputSession, sequence string) {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	t.stateMu.Lock()
	if !t.started || t.session != session {
		t.stateMu.Unlock()
		return
	}
	handler := t.onInput
	t.stateMu.Unlock()
	if session.active.Load() && handler != nil {
		t.forwardInputTo(handler, sequence)
	}
}

func (t *ProcessTerminal) forwardInputTo(handler func(string), sequence string) {
	shouldDetectShift := sequence == "\r" && (runtime.GOOS == "windows" || runtime.GOOS == "darwin" && os.Getenv("TERM_PROGRAM") == "Apple_Terminal")
	sequence = normalizeNativeShiftEnterInput(sequence, shouldDetectShift, shouldDetectShift && nativeShiftPressed())
	handler(sequence)
}

func normalizeNativeShiftEnterInput(sequence string, shouldDetect, shiftPressed bool) string {
	if sequence == "\r" && shouldDetect && shiftPressed {
		return "\x1b[13;2u"
	}
	return sequence
}

func (t *ProcessTerminal) enableModifyOtherKeysLocked() {
	if t.kittyProtocolActive || t.modifyOtherKeysActive {
		return
	}
	t.directWrite(modifyOtherKeysEnable)
	t.modifyOtherKeysActive = true
}

func (t *ProcessTerminal) disableModifyOtherKeysLocked() {
	if !t.modifyOtherKeysActive {
		return
	}
	t.directWrite(modifyOtherKeysDisable)
	t.modifyOtherKeysActive = false
}

func (t *ProcessTerminal) DrainInput(maxDuration, idleDuration time.Duration) error {
	t.drainMu.Lock()
	defer t.drainMu.Unlock()
	if maxDuration <= 0 {
		maxDuration = DefaultDrainMax
	}
	if idleDuration <= 0 {
		idleDuration = DefaultDrainIdle
	}
	t.stateMu.Lock()
	generation := t.generation
	t.draining = true
	t.drainGeneration++
	if t.drainSignal != nil {
		close(t.drainSignal)
	}
	t.drainSignal = make(chan struct{})
	t.clearNegotiationBufferLocked()
	if t.keyboardProtocolPushed || t.kittyProtocolActive {
		t.directWrite(kittyKeyboardProtocolPop)
		t.keyboardProtocolPushed, t.kittyProtocolActive = false, false
		SetKittyProtocolActive(false)
	}
	t.disableModifyOtherKeysLocked()
	previousHandler := t.onInput
	t.onInput = nil
	if t.stdinBuffer != nil {
		t.stdinBuffer.discardPendingEvents()
	}
	t.stateMu.Unlock()

	maximum := time.NewTimer(maxDuration)
	idle := time.NewTimer(idleDuration)
	defer maximum.Stop()
	defer idle.Stop()
	for {
		select {
		case <-maximum.C:
			goto done
		case <-idle.C:
			goto done
		case <-t.activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleDuration)
		}
	}
done:
	t.stateMu.Lock()
	if t.started && t.generation == generation {
		t.draining = false
		if t.onInput == nil {
			t.onInput = previousHandler
		}
	}
	t.stateMu.Unlock()
	return nil
}

func (t *ProcessTerminal) Stop() {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	t.stopLocked()
}

func (t *ProcessTerminal) stopLocked() {
	t.stateMu.Lock()
	t.generation++
	t.started = false
	stoppedSession := t.session
	if stoppedSession != nil {
		stoppedSession.active.Store(false)
	}
	t.session = nil
	shouldPop := t.keyboardProtocolPushed || t.kittyProtocolActive
	shouldDisableModifyOtherKeys := t.modifyOtherKeysActive
	t.clearNegotiationBufferLocked()
	t.keyboardProtocolPushed, t.kittyProtocolActive, t.modifyOtherKeysActive = false, false, false
	buffer, stopInput, stopReader, stopResize, restorePlatform, oldState := t.stdinBuffer, t.stopInput, t.stopReader, t.stopResize, t.restorePlatform, t.oldState
	t.stdinBuffer, t.inputData, t.stopInput, t.drainSignal = nil, nil, nil, nil
	t.stopReader, t.stopResize, t.restorePlatform = nil, nil, nil
	t.draining = false
	t.drainGeneration++
	t.onInput, t.onResize = nil, nil
	progressStop, progressDone := t.progressStop, t.progressDone
	if progressStop != nil {
		t.progressStop, t.progressDone = nil, nil
		close(progressStop)
	}
	t.stateMu.Unlock()
	if shouldPop {
		SetKittyProtocolActive(false)
	}
	if stopInput != nil {
		close(stopInput)
	}
	if buffer != nil {
		buffer.Destroy()
	}
	if stopReader != nil {
		stopReader()
	}
	if stopResize != nil {
		stopResize()
	}
	if oldState != nil {
		err := term.Restore(int(t.stdin.Fd()), oldState)
		if err != nil {
			err = term.Restore(int(t.stdin.Fd()), oldState)
		}
		if err == nil {
			t.stateMu.Lock()
			if t.oldState == oldState {
				t.oldState = nil
			}
			t.stateMu.Unlock()
		} else {
			t.recordError(err)
		}
	}
	if progressDone != nil {
		<-progressDone
		t.directWrite(progressClearSequence)
	}
	t.directWrite("\x1b[?2004l")
	if shouldPop {
		t.directWrite(kittyKeyboardProtocolPop)
	}
	if shouldDisableModifyOtherKeys {
		t.directWrite(modifyOtherKeysDisable)
	}
	if restorePlatform != nil {
		t.recordError(restorePlatform())
	}
}

func (t *ProcessTerminal) recordError(err error) {
	if err == nil {
		return
	}
	t.stateMu.Lock()
	t.lastError = errors.Join(t.lastError, err)
	t.stateMu.Unlock()
}

// LastError returns the most recent terminal setup or restoration error.
func (t *ProcessTerminal) LastError() error {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.lastError
}

func (t *ProcessTerminal) directWrite(data string) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeDirectLocked(data)
}

func (t *ProcessTerminal) writeDirectLocked(data string) {
	for len(data) > 0 {
		written, err := io.WriteString(t.stdout, data)
		if err != nil || written <= 0 {
			return
		}
		data = data[written:]
	}
}

func (t *ProcessTerminal) Write(data string) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeDirectLocked(data)
	if t.writeLogPath != "" {
		if file, err := os.OpenFile(t.writeLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			_, _ = io.WriteString(file, data)
			_ = file.Close()
		}
	}
}

func terminalDimension(file *os.File, environment string, fallback int, width bool) int {
	if term.IsTerminal(int(file.Fd())) {
		columns, rows, err := term.GetSize(int(file.Fd()))
		if err == nil {
			if width && columns != 0 {
				return columns
			}
			if !width && rows != 0 {
				return rows
			}
		}
	}
	if value, err := strconv.Atoi(os.Getenv(environment)); err == nil && value != 0 {
		return value
	}
	return fallback
}

func (t *ProcessTerminal) Columns() int { return terminalDimension(t.stdout, "COLUMNS", 80, true) }
func (t *ProcessTerminal) Rows() int    { return terminalDimension(t.stdout, "LINES", 24, false) }
func (t *ProcessTerminal) KittyProtocolActive() bool {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.kittyProtocolActive
}

func (t *ProcessTerminal) MoveBy(lines int) {
	if lines > 0 {
		t.directWrite("\x1b[" + strconv.Itoa(lines) + "B")
	} else if lines < 0 {
		t.directWrite("\x1b[" + strconv.Itoa(-lines) + "A")
	}
}
func (t *ProcessTerminal) HideCursor()           { t.directWrite("\x1b[?25l") }
func (t *ProcessTerminal) ShowCursor()           { t.directWrite("\x1b[?25h") }
func (t *ProcessTerminal) ClearLine()            { t.directWrite("\x1b[K") }
func (t *ProcessTerminal) ClearFromCursor()      { t.directWrite("\x1b[J") }
func (t *ProcessTerminal) ClearScreen()          { t.directWrite("\x1b[2J\x1b[H") }
func (t *ProcessTerminal) SetTitle(title string) { t.directWrite("\x1b]0;" + title + "\x07") }

func (t *ProcessTerminal) SetProgress(active bool) {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	if active {
		t.directWrite(progressActiveSequence)
		t.stateMu.Lock()
		if t.progressStop == nil {
			stop, done := make(chan struct{}), make(chan struct{})
			t.progressStop, t.progressDone = stop, done
			go func() {
				ticker := time.NewTicker(progressKeepalive)
				defer ticker.Stop()
				defer close(done)
				for {
					select {
					case <-ticker.C:
						t.directWrite(progressActiveSequence)
					case <-stop:
						return
					}
				}
			}()
		}
		t.stateMu.Unlock()
		return
	}
	t.stopProgress(false)
	t.directWrite(progressClearSequence)
}

func (t *ProcessTerminal) stopProgress(writeClear bool) {
	t.stateMu.Lock()
	stop, done := t.progressStop, t.progressDone
	if stop != nil {
		t.progressStop, t.progressDone = nil, nil
		close(stop)
	}
	t.stateMu.Unlock()
	if done != nil {
		<-done
		if writeClear {
			t.directWrite(progressClearSequence)
		}
	}
}

var _ Terminal = (*ProcessTerminal)(nil)
