package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tuitext "github.com/CarlvinceTan/midas/internal/tui/text"
	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// ChatRun is the event stream required by ChatBackend. Both agent.Stream and
// internal/chat.Run satisfy it without coupling this package to internal/chat.
type ChatRun interface {
	Next(context.Context) (agent.Event, bool, error)
	Cancel(error)
}

// ChatBackend is the small runtime boundary used by the interactive TUI.
type ChatBackend interface {
	Start(context.Context, string) (ChatRun, error)
	Abort(error) bool
}

type ChatSteerer interface {
	Steer(context.Context, string) error
}

// ChatCompactor is the optional backend capability behind /compact. It replaces
// the older part of the conversation with a checkpoint and returns the rewritten
// history.
type ChatCompactor interface {
	CompactContext(context.Context) ([]ai.Message, bool, error)
}

// ChatReverter is the optional backend capability behind /undo. It removes the
// last prompt and everything after it, returning the removed prompt text and the
// conversation that remains.
type ChatReverter interface {
	RevertLastPrompt() (string, []ai.Message, bool, error)
}

type LocalCommand func(context.Context, string, string) (string, error)
type OverlayCommand func(name, args string) bool
type StatusGenerator func(context.Context, string, int) (string, error)

// ShellEntry is one local !/!! command rendered independently of agent chat.
type ShellEntry struct {
	Command  string
	Output   string
	Exclude  bool
	Status   string
	ExitCode *int
	At       int64
}

// NativeSlashCommands is the command catalog shown by the Go TUI. It mirrors
// the pre-port Midas wording while containing no OpenCode-provided commands.
// Commands that accept arguments declare their usage hint; every other command
// takes none, so a slash line that passes arguments to it is sent as an ordinary
// prompt rather than answered with a command error.
var NativeSlashCommands = []SlashCommand{
	{Name: "model", Description: "Select the active model"},
	{Name: "connect", Description: "Connect or disconnect providers"},
	{Name: "agents", Description: "Set the active agent profile", Args: "[name]"},
	{Name: "thinking", Description: "Set the thinking level"},
	{Name: "settings", Description: "Configure Midas behavior"},
	{Name: "reload", Description: "Re-read settings, agents, MCP servers, instructions, and skills"},
	{Name: "voice", Description: "Dictate into the input with the microphone (on/off)", Args: "[on|off]"},
	{Name: "sessions", Description: "Resume or manage sessions"},
	{Name: "new", Description: "Start a new session"},
	{Name: "title", Description: "Rename the current session", Args: "<name>"},
	{Name: "copy", Description: "Copy the last assistant response"},
	{Name: "undo", Description: "Stop the run and put the last prompt back in the input"},
	{Name: "compact", Description: "Summarize older context into a checkpoint"},
	{Name: "stats", Description: "Token usage and spend for Midas"},
	{Name: "remote", Description: "Toggle password-gated browser access", Args: "[on|off]"},
	{Name: "goal", Description: "Resume, pause or edit the goal", Args: "[resume|pause|edit <objective>]"},
	{Name: "mcps", Description: "Manage MCP servers", Args: "[status|connect|disconnect <name>]"},
	{Name: "skills", Description: "Show available skills"},
	{Name: "exit", Description: "Quit Midas"},
}

// slashCommandAliases maps accepted spellings that the menu does not list onto
// their catalog command.
var slashCommandAliases = map[string]string{"mcp": "mcps"}

type ChatOptions struct {
	Backend ChatBackend
	// ModelReasoning marks a model whose reasoning tokens are hidden, so live
	// characters may not calibrate the chars/token ratio.
	ModelReasoning  bool
	Context         context.Context
	Provider        string
	Model           string
	ModelName       string
	ModelContext    int
	Thinking        ai.ThinkingLevel
	Agent           string
	SessionID       string
	SessionTitle    string
	CWD             string
	Branch          string
	ContextPaths    []string
	AgentGroups     [][]string
	SkillGroups     [][]string
	MCPNames        []string
	InitialMessages []ai.Message
	InitialShell    []ShellEntry
	InitialDraft    string
	InitialQueue    []string
	QueueHeld       bool
	CompactHeader   bool
	StatusMaxWords  int
	StatusGenerator StatusGenerator
	// TitleGenerator names the session from the request that starts it. The
	// placeholder title is heuristic until the helper answers.
	TitleGenerator TitleGenerator
	TitleMaxWords  int
	// OnTitle receives a generated title after the Chat applied it, so the host
	// can persist it for the session picker and the terminal window title.
	OnTitle         func(string)
	CostFormatter   func(float64) string
	Rows            func() int
	RequestRender   func()
	Command         LocalCommand
	OverlayCommand  OverlayCommand
	OnDraftChange   func(string)
	OnQueueChange   func([]string, bool)
	OnShellComplete func(ShellEntry)
	// OnDirectoryChange handles a bare `cd` typed in shell mode. Returning an
	// error leaves the working directory unchanged.
	OnDirectoryChange func(path string) error
	ShellRunner       ShellRunner
	OnVoiceStop       func()
	OnCycleThinking   func()
	OnQuit            func()
}

type chatEntryKind uint8

const (
	chatUser chatEntryKind = iota
	chatAssistant
	chatShell
	// chatNotice is a muted one-line row about the session itself, such as a
	// compaction checkpoint.
	chatNotice
)

type chatTool struct {
	id      string
	name    string
	args    map[string]any
	result  string
	status  string
	isError bool
}

type chatSegmentKind uint8

const (
	chatSegmentText chatSegmentKind = iota
	chatSegmentThinking
	chatSegmentTool
)

// chatSegment preserves the order of prose, reasoning and tool calls inside a
// turn. The old Midas transcript used that order to build collapsible activity
// chains; keeping only three aggregate fields cannot distinguish an
// intermediate reply from the final answer or split consecutive tool groups.
type chatSegment struct {
	kind      chatSegmentKind
	text      string
	toolID    string
	startedAt int64
	endedAt   int64
}

type chatEntry struct {
	kind chatEntryKind
	text string
	// display preserves the prompt as the user saw it (attachment chips stay
	// `[Image: …]` labels) while text carries what the agent receives.
	display   string
	thinking  string
	tools     []chatTool
	segments  []chatSegment
	shell     *ShellEntry
	startedAt int64
	endedAt   int64
	// borderStyle snapshots the mode used when the prompt was sent. Legacy
	// Midas kept existing prompt cards in their original agent/thinking colour.
	borderStyle  func(string) string
	steerID      uint64
	steerPending bool
	steerLanded  bool
}

// Chat is the native interactive conversation component. It keeps rendering
// state only; provider messages remain owned by the backend.
// pendingTarget marks one rendered queued-message row so a click can pull that
// message back into the editor.
type pendingTarget struct {
	index      int
	start, end int
}

type Chat struct {
	FocusState

	backend           ChatBackend
	ctx               context.Context
	cancel            context.CancelFunc
	provider          string
	model             string
	modelName         string
	modelContext      int
	thinking          ai.ThinkingLevel
	agent             string
	session           string
	title             string
	cwd               string
	branch            string
	contextPaths      []string
	agentGroups       [][]string
	skillGroups       [][]string
	mcpNames          []string
	request           func()
	command           LocalCommand
	overlay           OverlayCommand
	onQueue           func([]string, bool)
	onShell           func(ShellEntry)
	onDirectoryChange func(string) error
	shellRunner       ShellRunner
	onVoiceStop       func()
	onCycleThinking   func()
	onQuit            func()
	statusGenerator   StatusGenerator
	// contextTokens is the size of the context the last completed assistant
	// message left behind; the live estimate grows it while a turn streams.
	contextTokens   int
	contextKnown    bool
	liveContext     LiveContext
	contextDisplay  ContextDisplay
	titleGenerator  TitleGenerator
	titleGenerating bool
	titleGeneration uint64
	titleMaxWords   int
	onTitle         func(string)
	editor          *Editor
	voiceEditor     *Editor
	done            chan struct{}
	doneOnce        sync.Once

	mu                sync.Mutex
	entries           []chatEntry
	active            int
	expandedTools     bool
	runExpanded       map[int]bool
	chainExpanded     map[string]bool
	detailExpanded    map[string]bool
	transcriptTargets []transcriptTarget
	nextSteerID       uint64
	queue             []string
	queueHeld         bool
	reverting         bool
	// queueEdit is the slot a queued message was pulled out of for editing, or
	// -1 when the editor holds an ordinary prompt.
	queueEdit        int
	pendingTargets   []pendingTarget
	queueDispatching bool
	busy             bool
	closing          bool
	voiceActive      bool
	voiceStopping    bool
	voiceReady       bool
	voiceBase        string
	voiceCommitted   string
	voicePartial     string
	toastText        string
	toastLevel       ToastLevel
	toastGeneration  uint64
	compactHeader    bool
	statusText       string
	statusFallback   string
	// writingLabel is set while the model is composing a tool call, so the live
	// row can say what is being written before anything executes.
	writingLabel      string
	statusMaxWords    int
	statusGenerating  bool
	statusGeneration  uint64
	lastStatusAt      time.Time
	usage             ai.Usage
	generation        GenerationRate
	rateDisplay       RateDisplay
	modelReasoning    bool
	spinnerFrame      int
	lastSpinnerAt     time.Time
	liveSince         time.Time
	liveSinceID       string
	liveExpanded      bool
	costFormatter     func(float64) string
	runStarted        time.Time
	editorBorderStyle func(string) string
	dockComponent     Component
	shellCancel       context.CancelFunc
	remoteURL         string
	onCopyRemote      func()

	lastEditorHeight int
}

func NewChat(options ChatOptions) (*Chat, error) {
	if options.Backend == nil {
		return nil, errors.New("tui: chat backend is required")
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	model := strings.TrimSpace(options.Model)
	provider := strings.TrimSpace(options.Provider)
	if provider == "" && model != "" {
		provider = "configured"
	}
	chat := &Chat{
		backend: options.Backend, ctx: ctx, cancel: cancel,
		provider: provider, model: model, modelName: options.ModelName, modelContext: options.ModelContext,
		thinking: options.Thinking, agent: options.Agent, session: options.SessionID, title: options.SessionTitle, cwd: options.CWD,
		branch: options.Branch, contextPaths: append([]string(nil), options.ContextPaths...),
		agentGroups: cloneStringGroups(options.AgentGroups), skillGroups: cloneStringGroups(options.SkillGroups),
		mcpNames: append([]string(nil), options.MCPNames...),
		request:  options.RequestRender, command: options.Command, overlay: options.OverlayCommand, onQueue: options.OnQueueChange,
		onVoiceStop: options.OnVoiceStop, onCycleThinking: options.OnCycleThinking, onQuit: options.OnQuit,
		onShell: options.OnShellComplete, onDirectoryChange: options.OnDirectoryChange, shellRunner: options.ShellRunner,
		statusGenerator: options.StatusGenerator,
		titleGenerator:  options.TitleGenerator, titleMaxWords: options.TitleMaxWords, onTitle: options.OnTitle,
		done: make(chan struct{}), active: -1, queueEdit: -1, queue: append([]string(nil), options.InitialQueue...), queueHeld: options.QueueHeld,
		compactHeader: options.CompactHeader, statusMaxWords: options.StatusMaxWords,
		modelReasoning: options.ModelReasoning, costFormatter: options.CostFormatter,
		runExpanded: make(map[int]bool), chainExpanded: make(map[string]bool), detailExpanded: make(map[string]bool),
	}
	if chat.statusMaxWords <= 0 {
		chat.statusMaxWords = 6
	}
	if chat.titleMaxWords <= 0 {
		chat.titleMaxWords = defaultTitleMaxWords
	}
	chat.entries = transcriptEntries(options.InitialMessages)
	for _, entry := range options.InitialShell {
		copy := entry
		chat.entries = append(chat.entries, chatEntry{kind: chatShell, shell: &copy})
	}
	if chat.shellRunner == nil {
		chat.shellRunner = runLocalShell
	}
	chat.usage = transcriptUsage(options.InitialMessages)
	chat.contextTokens, chat.contextKnown = transcriptContext(options.InitialMessages)
	chat.editor = NewEditor(EditorOptions{
		PaddingX: 0, Rows: options.Rows, CWD: chat.cwd, RequestRender: options.RequestRender,
		SlashCommands: NativeSlashCommands,
		OnSubmit:      chat.submit, OnEscape: chat.escape, OnChange: func(value string) {
			chat.updateEditorBorder(value)
			if options.OnDraftChange != nil {
				options.OnDraftChange(value)
			}
		},
	})
	if options.InitialDraft != "" {
		chat.editor.SetText(options.InitialDraft)
	}
	return chat, nil
}

func (c *Chat) Done() <-chan struct{} { return c.done }
func (c *Chat) Editor() *Editor       { return c.editor }
func (c *Chat) Provider() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.provider
}
func (c *Chat) SetProvider(provider string) {
	c.mu.Lock()
	c.provider = strings.TrimSpace(provider)
	c.mu.Unlock()
	c.repaint()
}
func (c *Chat) SetModel(model string) { c.SetModelDetails(model, "", 0) }
func (c *Chat) SetModelDetails(model, name string, contextWindow int) {
	c.mu.Lock()
	changed := c.model != model
	c.model, c.modelName, c.modelContext = model, name, contextWindow
	if changed {
		// A new model has a different window and pricing context, so the live
		// reading, the eased rate and the generation all restart.
		c.liveContext.Clear()
		c.contextDisplay.Clear()
		c.rateDisplay.Reset()
		c.generation = GenerationRate{}
	}
	c.mu.Unlock()
	c.repaint()
}
func (c *Chat) SetThinking(level ai.ThinkingLevel) {
	c.mu.Lock()
	c.thinking = level
	c.mu.Unlock()
	c.repaint()
}

// SetRemote updates the footer's remote indicator. An empty URL removes it.
func (c *Chat) SetRemote(url string, onCopy func()) {
	c.mu.Lock()
	c.remoteURL = strings.TrimSpace(url)
	if c.remoteURL == "" {
		c.onCopyRemote = nil
	} else {
		c.onCopyRemote = onCopy
	}
	c.mu.Unlock()
	c.repaint()
}
func (c *Chat) SetAgent(name string)  { c.mu.Lock(); c.agent = name; c.mu.Unlock(); c.repaint() }
func (c *Chat) SetTitle(title string) { c.mu.Lock(); c.title = title; c.mu.Unlock(); c.repaint() }
func (c *Chat) SetCompactHeader(enabled bool) {
	c.mu.Lock()
	c.compactHeader = enabled
	generate := !enabled && c.busy
	if enabled {
		c.statusGeneration++
		c.statusGenerating = false
	} else if generate {
		c.lastStatusAt = time.Time{}
	}
	c.mu.Unlock()
	c.repaint()
	if generate {
		c.maybeGenerateStatus()
	}
}
func (c *Chat) SetStatusMaxWords(maximum int) {
	maximum = max(1, min(12, maximum))
	c.mu.Lock()
	c.statusMaxWords = maximum
	c.statusText = cleanStatusPhrase(c.statusText, maximum)
	c.mu.Unlock()
	c.repaint()
}
func (c *Chat) SetEditorBorderStyle(style func(string) string) {
	c.mu.Lock()
	c.editorBorderStyle = style
	c.mu.Unlock()
	c.updateEditorBorder(c.editor.Text())
}

func (c *Chat) editorStyleFor(value string) func(string) string {
	c.mu.Lock()
	style := c.editorBorderStyle
	c.mu.Unlock()
	if _, _, shell := parseShellCommand(value); shell {
		return func(text string) string { return CurrentTheme().FG("bashMode", text) }
	}
	return style
}

func (c *Chat) updateEditorBorder(value string) {
	c.editor.SetBorderStyle(c.editorStyleFor(value))
}

// SetDockComponent temporarily replaces the editor frame with a command view.
// The original Midas UI mounted pickers in the fixed editor dock at full width;
// it did not float them in a centred modal.
func (c *Chat) SetDockComponent(component Component) {
	c.mu.Lock()
	c.dockComponent = component
	c.mu.Unlock()
	c.repaint()
}

// ClearDockComponent restores the editor when component still owns the dock.
func (c *Chat) ClearDockComponent(component Component) bool {
	c.mu.Lock()
	if c.dockComponent != component {
		c.mu.Unlock()
		return false
	}
	c.dockComponent = nil
	c.mu.Unlock()
	c.repaint()
	return true
}

func (c *Chat) DockComponent() Component {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dockComponent
}
func (c *Chat) SetSession(id string, messages []ai.Message, shell []ShellEntry, draft string, queue []string, held bool) {
	c.mu.Lock()
	c.session = id
	c.entries = transcriptEntries(messages)
	for _, entry := range shell {
		copy := entry
		c.entries = append(c.entries, chatEntry{kind: chatShell, shell: &copy})
	}
	c.usage = transcriptUsage(messages)
	c.rateDisplay.Reset()
	c.generation = GenerationRate{}
	c.active = -1
	c.expandedTools = false
	c.runExpanded = make(map[int]bool)
	c.chainExpanded = make(map[string]bool)
	c.detailExpanded = make(map[string]bool)
	c.transcriptTargets = nil
	c.queue = append([]string(nil), queue...)
	c.queueHeld = held
	c.mu.Unlock()
	c.editor.SetText(draft)
	c.repaint()
}
func (c *Chat) LastAssistantText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range slices.Backward(c.entries) {
		if entry.kind == chatAssistant && strings.TrimSpace(entry.text) != "" {
			return entry.text
		}
	}
	return ""
}
func (c *Chat) Notice(value string) { c.setStatus(value) }

func (c *Chat) ShowToast(value string, level ToastLevel, duration time.Duration) {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return
	}
	c.mu.Lock()
	c.toastGeneration++
	generation := c.toastGeneration
	c.toastText = value
	c.toastLevel = level
	c.mu.Unlock()
	c.repaint()
	if duration <= 0 {
		return
	}
	time.AfterFunc(duration, func() {
		c.mu.Lock()
		if c.toastGeneration == generation {
			c.toastText = ""
		}
		c.mu.Unlock()
		c.repaint()
	})
}
func (c *Chat) Invalidate()           { c.editor.Invalidate() }
func (c *Chat) IsFocused() bool       { return c.FocusState.IsFocused() }
func (c *Chat) SetFocused(value bool) { c.FocusState.SetFocused(value); c.editor.SetFocused(value) }

func (c *Chat) Close() {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		c.cancel()
		c.backend.Abort(context.Canceled)
		if c.onQuit != nil {
			c.onQuit()
		}
		close(c.done)
	})
}

func (c *Chat) HandleInput(data string) {
	c.mu.Lock()
	voiceActive := c.voiceActive
	voiceStop := c.onVoiceStop
	c.mu.Unlock()
	if voiceActive {
		if MatchesKey(data, "escape") && voiceStop != nil {
			if c.BeginVoiceStop() {
				voiceStop()
			}
		}
		return
	}
	if MatchesKey(data, "ctrl+c") && c.editor.Text() == "" {
		c.mu.Lock()
		busy := c.busy
		c.mu.Unlock()
		if busy {
			c.escape()
		} else {
			c.Close()
		}
		return
	}
	if GetKeybindings().Matches(data, "tui.input.steer") {
		if !IsKeyRelease(data) && !IsKeyRepeat(data) {
			c.steerInput()
		}
		return
	}
	if GetKeybindings().Matches(data, "tui.thinking.cycle") {
		if c.onCycleThinking != nil {
			c.onCycleThinking()
		}
		return
	}
	if GetKeybindings().Matches(data, "tui.transcript.expandTools") {
		c.mu.Lock()
		c.expandedTools = !c.expandedTools
		c.mu.Unlock()
		c.repaint()
		return
	}
	c.editor.HandleInput(data)
}

// BeginVoiceStop hides the listening state immediately while the recognizer
// flushes its final segment. EndVoice commits the accumulated text afterward.
func (c *Chat) BeginVoiceStop() bool {
	c.mu.Lock()
	if !c.voiceActive || c.voiceStopping {
		c.mu.Unlock()
		return false
	}
	c.voiceStopping = true
	c.mu.Unlock()
	c.repaint()
	return true
}

func (c *Chat) VoiceActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.voiceActive
}

func (c *Chat) StartVoice() {
	c.mu.Lock()
	c.voiceActive = true
	c.voiceStopping = false
	c.voiceReady = false
	c.voiceBase = c.editor.Text()
	c.voiceCommitted = ""
	c.voicePartial = ""
	voiceEditor := NewEditor(EditorOptions{PaddingX: 0, Rows: c.editor.rows})
	voiceEditor.SetCursorShown(false)
	voiceEditor.SetSubmitDisabled(true)
	voiceEditor.SetText(c.voiceBase)
	c.voiceEditor = voiceEditor
	c.mu.Unlock()
	c.editor.SetSubmitDisabled(true)
	c.repaint()
}

func (c *Chat) SetVoiceReady(ready bool) {
	c.mu.Lock()
	if c.voiceActive {
		c.voiceReady = ready
	}
	c.mu.Unlock()
	c.repaint()
}

func (c *Chat) SetVoiceText(committed, partial string) {
	c.mu.Lock()
	if c.voiceActive {
		c.voiceCommitted = committed
		c.voicePartial = partial
		if c.voiceEditor != nil {
			c.voiceEditor.SetText(composeVoiceInput(c.voiceBase, committed, partial))
		}
	}
	c.mu.Unlock()
	c.repaint()
}

func (c *Chat) EndVoice(commit bool) {
	c.mu.Lock()
	if !c.voiceActive {
		c.mu.Unlock()
		return
	}
	value := c.voiceBase
	if commit {
		value = composeVoiceInput(c.voiceBase, c.voiceCommitted, c.voicePartial)
	}
	c.voiceActive = false
	c.voiceStopping = false
	c.voiceReady = false
	c.voiceBase = ""
	c.voiceCommitted = ""
	c.voicePartial = ""
	c.voiceEditor = nil
	c.mu.Unlock()
	c.editor.SetSubmitDisabled(false)
	c.editor.SetText(value)
	c.repaint()
}

func composeVoiceInput(base, committed, partial string) string {
	committed = strings.TrimSpace(committed)
	partial = strings.TrimSpace(partial)
	speech := committed
	if speech == "" {
		speech = partial
	} else if partial != "" {
		speech += " " + partial
	}
	if speech == "" {
		return base
	}
	if base == "" || strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\n") || strings.HasSuffix(base, "\t") {
		return base + speech
	}
	return base + " " + speech
}

func (c *Chat) HandleMouse(event MouseEvent) *MouseResult {
	if event.Y < event.Height-c.lastEditorHeight {
		return nil
	}
	event.Y -= event.Height - c.lastEditorHeight
	event.Height = c.lastEditorHeight
	return c.editor.HandleMouse(event)
}

// undo stops the active run and rolls the session back to just before the last
// prompt, putting that prompt back into the input box. Like Esc, it holds the
// queue so follow-ups the user still has queued are not delivered by the
// revert's idle.
func (c *Chat) undo() {
	reverter, canUndo := c.backend.(ChatReverter)
	if !canUndo {
		c.setStatus("Nothing to undo")
		return
	}
	c.mu.Lock()
	busy := c.busy
	if busy {
		c.queueHeld = true
	}
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	// An aborted run finalises its transcript entries in finishRun, so the
	// revert waits for that to happen instead of racing it.
	reverting := busy
	c.reverting = reverting
	c.mu.Unlock()
	if !reverting {
		c.applyUndo(reverter)
		return
	}
	if callback != nil {
		callback(queue, held)
	}
	if !c.backend.Abort(errors.New("aborted by user")) {
		c.mu.Lock()
		c.reverting = false
		c.mu.Unlock()
		c.applyUndo(reverter)
	}
}

// compact summarizes older context now, without waiting for the model window to
// fill up. The rewrite happens in the background so the UI keeps running.
func (c *Chat) compact() {
	compactor, canCompact := c.backend.(ChatCompactor)
	if !canCompact {
		c.setStatus("Nothing to compact")
		return
	}
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if busy {
		c.setStatus("Wait for the current run to finish before compacting.")
		return
	}
	c.setStatus("Compacting context…")
	go func() {
		messages, compacted, err := compactor.CompactContext(c.ctx)
		if err != nil {
			c.setStatus("Error: " + err.Error())
			return
		}
		if !compacted {
			c.setStatus("Nothing to compact")
			return
		}
		c.SetTranscript(messages)
		c.setStatus(fmt.Sprintf("Compacted the context to %d messages.", len(messages)))
	}()
}

// SetTranscript re-renders the conversation while keeping the queue, local shell
// entries, and whatever the editor already holds.
func (c *Chat) SetTranscript(messages []ai.Message) {
	c.mu.Lock()
	shell := make([]ShellEntry, 0, 2)
	for _, entry := range c.entries {
		if entry.kind == chatShell && entry.shell != nil {
			shell = append(shell, *entry.shell)
		}
	}
	queue, held := append([]string(nil), c.queue...), c.queueHeld
	draft := c.editor.Text()
	c.mu.Unlock()
	c.SetSession(c.session, messages, shell, draft, queue, held)
}

// applyUndo rolls the rendered transcript back to the reverted conversation and
// restores the undone prompt in the editor.
func (c *Chat) applyUndo(reverter ChatReverter) {
	prompt, messages, undone, err := reverter.RevertLastPrompt()
	if err != nil {
		c.setStatus("Error: " + err.Error())
		return
	}
	if !undone {
		c.ShowToast("Nothing to undo", ToastWarning, 2500*time.Millisecond)
		return
	}
	c.mu.Lock()
	held := c.queueHeld
	c.mu.Unlock()
	c.SetTranscript(messages)
	c.editor.SetText(prompt)
	if held {
		c.setStatus("Undid the last prompt. Queued prompts are held until you submit new work.")
		return
	}
	c.setStatus("Undid the last prompt.")
}

func (c *Chat) escape() {
	c.mu.Lock()
	busy := c.busy
	shellCancel := c.shellCancel
	if busy {
		c.queueHeld = true
	}
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	c.mu.Unlock()
	if busy && callback != nil {
		callback(queue, held)
	}
	if busy {
		if shellCancel != nil {
			shellCancel()
			c.setStatus("Shell command cancelled. Queued prompts are held until you submit new work.")
			return
		}
		if c.backend.Abort(errors.New("aborted by user")) {
			c.setStatus("Run aborted. Queued prompts are held until you submit new work.")
		}
		return
	}
	if c.editor.Text() != "" {
		c.editor.SetText("")
	}
}

func (c *Chat) submit(value string) {
	command, exclude, shell := parseShellCommand(value)
	trimmed := strings.TrimSpace(value)
	c.mu.Lock()
	editing := c.queueEdit
	c.mu.Unlock()
	if editing >= 0 {
		c.finishQueueEdit(value, trimmed)
		return
	}
	if trimmed == "" {
		return
	}
	if strings.EqualFold(trimmed, "exit") || trimmed == "/exit" || trimmed == "/quit" {
		c.Close()
		return
	}
	if strings.EqualFold(trimmed, "/undo") {
		c.undo()
		return
	}
	if strings.EqualFold(trimmed, "/compact") {
		c.compact()
		return
	}
	if strings.HasPrefix(trimmed, "/") && isSlashCommandCall(trimmed) {
		c.runCommand(trimmed)
		return
	}
	c.mu.Lock()
	if c.busy {
		c.queue = append(c.queue, value)
		c.queueHeld = false
		queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
		c.mu.Unlock()
		if callback != nil {
			callback(queue, held)
		}
		c.repaint()
		return
	}
	provider, model := c.provider, c.model
	if !shell && (strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "") {
		c.mu.Unlock()
		c.editor.SetText(value)
		if strings.TrimSpace(provider) == "" {
			c.ShowToast("No provider selected.", ToastWarning, 2500*time.Millisecond)
		} else {
			c.ShowToast("No model selected.", ToastWarning, 2500*time.Millisecond)
		}
		return
	}
	c.queueHeld = false
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	c.mu.Unlock()
	if callback != nil {
		callback(queue, held)
	}
	if shell {
		if command != "" {
			c.startShell(command, exclude)
		}
		return
	}
	c.start(ExpandAttachmentPaths(trimmed, c.takeSubmissionAttachments()), trimmed)
}

// takeSubmissionAttachments consumes the chips captured by the last editor
// submit, so a prompt keeps its visible labels while the agent receives paths.
func (c *Chat) takeSubmissionAttachments() map[string]string {
	if c.editor == nil {
		return nil
	}
	return c.editor.SubmissionAttachments()
}

func (c *Chat) steerInput() {
	if strings.TrimSpace(c.editor.ExpandedText()) != "" {
		c.steerTyped()
		return
	}
	c.steerQueued()
}

func (c *Chat) steerTyped() {
	value := strings.TrimSpace(c.editor.ExpandedText())
	if value == "" {
		return
	}
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if command, exclude, shell := parseShellCommand(value); busy && shell {
		if command == "" {
			return
		}
		if !c.startParallelShell(command, exclude) {
			c.ShowToast("A shell command is already running.", ToastWarning, 3*time.Second)
			return
		}
		_, _ = c.editor.takeSubmission()
		return
	}
	steerer, canSteer := c.backend.(ChatSteerer)
	if !busy || !canSteer || strings.HasPrefix(value, "/") {
		c.editor.submit()
		return
	}
	// Match the original Midas delivery order: the steer card enters the
	// transcript before the backend can echo the user message or begin the next
	// step. Otherwise a fast provider can win the race and attach its
	// continuation to the assistant block above the steer.
	c.mu.Lock()
	steerID := c.appendSteerLocked(value)
	c.mu.Unlock()
	c.repaint()
	if err := steerer.Steer(c.ctx, value); err != nil {
		c.mu.Lock()
		removed := c.removePendingSteerLocked(steerID)
		c.mu.Unlock()
		c.repaint()
		if removed {
			// The run may have ended between the keypress and admission. Route the
			// unchanged editor through normal submission so the message is not lost.
			c.editor.submit()
		} else {
			// An echoed user message proves the steer crossed the boundary even if
			// the admission call returned an error afterward.
			_, _ = c.editor.takeSubmission()
		}
		return
	}
	_, _ = c.editor.takeSubmission()
	c.repaint()
}

func (c *Chat) steerQueued() {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}
	value := c.queue[0]
	busy := c.busy
	if !busy {
		c.queue = c.queue[1:]
		c.queueHeld = false
		queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
		c.mu.Unlock()
		if callback != nil {
			callback(queue, held)
		}
		c.dispatch(value)
		return
	}
	if strings.HasPrefix(strings.TrimSpace(value), "/") {
		c.mu.Unlock()
		c.ShowToast("Command queued until the run finishes.", ToastWarning, 3*time.Second)
		return
	}
	if command, exclude, shell := parseShellCommand(value); shell {
		if command == "" {
			c.mu.Unlock()
			return
		}
		if c.shellCancel != nil {
			c.mu.Unlock()
			c.ShowToast("A shell command is already running.", ToastWarning, 3*time.Second)
			return
		}
		c.queueDispatching = true
		c.mu.Unlock()
		started := c.startParallelShell(command, exclude)
		c.mu.Lock()
		c.queueDispatching = false
		if !started {
			c.mu.Unlock()
			return
		}
		if len(c.queue) > 0 && c.queue[0] == value {
			c.queue = c.queue[1:]
		}
		queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
		c.mu.Unlock()
		if callback != nil {
			callback(queue, held)
		}
		c.repaint()
		return
	}
	steerer, canSteer := c.backend.(ChatSteerer)
	if !canSteer {
		c.mu.Unlock()
		return
	}
	c.queue = c.queue[1:]
	c.queueDispatching = true
	steerID := c.appendSteerLocked(strings.TrimSpace(value))
	c.mu.Unlock()
	c.repaint()

	err := steerer.Steer(c.ctx, strings.TrimSpace(value))
	c.mu.Lock()
	c.queueDispatching = false
	if err != nil {
		removed := c.removePendingSteerLocked(steerID)
		if removed && c.busy {
			c.queue = append([]string{value}, c.queue...)
			queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
			c.mu.Unlock()
			if callback != nil {
				callback(queue, held)
			}
			c.repaint()
			return
		}
		if removed {
			c.queueHeld = false
		}
		queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
		c.mu.Unlock()
		if callback != nil {
			callback(queue, held)
		}
		if removed {
			c.dispatch(value)
		} else {
			c.repaint()
		}
		return
	}
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	c.mu.Unlock()
	if callback != nil {
		callback(queue, held)
	}
	c.repaint()
}

func (c *Chat) appendSteerLocked(value string) uint64 {
	c.nextSteerID++
	id := c.nextSteerID
	style := c.promptBorderStyleLocked()
	c.entries = append(c.entries, chatEntry{
		kind: chatUser, text: value, borderStyle: style, steerID: id, steerPending: true,
	})
	return id
}

func (c *Chat) removePendingSteerLocked(id uint64) bool {
	for index := range c.entries {
		entry := c.entries[index]
		if entry.kind != chatUser || entry.steerID != id || !entry.steerPending {
			continue
		}
		c.entries = append(c.entries[:index], c.entries[index+1:]...)
		return true
	}
	return false
}

func (c *Chat) runCommand(value string) {
	name, args := splitSlashLine(value)
	if c.overlay != nil && c.overlay(name, args) {
		return
	}
	if c.command == nil {
		c.setStatus(fmt.Sprintf("Unknown command /%s", name))
		return
	}
	go func() {
		result, err := c.command(c.ctx, name, args)
		if err != nil {
			c.setStatus("Error: " + err.Error())
			return
		}
		if strings.TrimSpace(result) != "" {
			c.setStatus(result)
		}
	}()
}

// splitSlashLine splits a submitted slash line into its lowercased command name
// and its trimmed arguments, exactly as typed.
func splitSlashLine(value string) (string, string) {
	body := strings.TrimPrefix(value, "/")
	name := body
	args := ""
	if space := strings.IndexAny(body, " \t\n"); space >= 0 {
		name, args = body[:space], strings.TrimSpace(body[space:])
	}
	return strings.ToLower(name), args
}

// isSlashCommandCall reports whether a submitted slash line actually invokes a
// command: the name must be a known command and the arguments must fit it. Slash
// text that names nothing, or that carries arguments for a command taking none,
// keeps its fuzzy-menu behaviour (the menu resolves a partial name on Enter) and
// is otherwise delivered as an ordinary prompt instead of a command error.
func isSlashCommandCall(value string) bool {
	name, args := splitSlashLine(value)
	if alias, ok := slashCommandAliases[name]; ok {
		name = alias
	}
	for _, command := range NativeSlashCommands {
		if command.Name != name {
			continue
		}
		return args == "" || strings.TrimSpace(command.Args) != ""
	}
	return false
}

func (c *Chat) start(prompt, display string) {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	borderStyle := c.promptBorderStyleLocked()
	c.entries = append(c.entries, chatEntry{kind: chatUser, text: prompt, display: display, borderStyle: borderStyle})
	now := time.Now().UnixMilli()
	c.entries = append(c.entries, chatEntry{kind: chatAssistant, startedAt: now})
	c.active = len(c.entries) - 1
	c.clearExpansionForEntryLocked(c.active)
	c.busy = true
	c.runStarted = time.Now()
	c.generation.Start(c.provider+"\x00"+c.model, c.runStarted)
	c.liveSince = c.runStarted
	c.liveSinceID = "working"
	c.liveExpanded = false
	c.statusText = ""
	c.statusFallback = "Working"
	c.lastStatusAt = time.Time{}
	c.statusGeneration++
	c.statusGenerating = false
	// The live estimate starts from the context the previous turn left behind and
	// grows with this turn's streamed content.
	c.liveContext.Start(c.contextTokens, c.contextKnown)
	c.contextDisplay.Clear()
	c.mu.Unlock()
	// The session title names the request that opened or extended the session, so
	// it is generated alongside the run rather than after it.
	c.applyHeuristicTitle(prompt)
	c.maybeGenerateTitle(prompt)
	c.repaint()
	run, err := c.backend.Start(c.ctx, prompt)
	if err != nil {
		c.failStart(prompt, err)
		return
	}
	go c.consume(run)
}

func (c *Chat) promptBorderStyleLocked() func(string) string {
	if c.editorBorderStyle != nil {
		return c.editorBorderStyle
	}
	level := c.thinking
	return func(value string) string { return CurrentTheme().Thinking(level, value) }
}

// cdTarget reports the directory a bare `cd` asks for, and whether the command is
// one. Anything carrying shell syntax is left to the shell, so `cd x && make`
// behaves like a command rather than a directory change.
func cdTarget(cwd, command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed != "cd" && !strings.HasPrefix(trimmed, "cd ") && !strings.HasPrefix(trimmed, "cd\t") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "cd"))
	if strings.ContainsAny(rest, ";|&<>$`(){}*?[]") {
		return "", false
	}
	if rest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Clean(home), true
	}
	// A quoted target may contain spaces; an unquoted one must be a single word.
	target := rest
	switch {
	case len(target) > 1 && strings.HasPrefix(target, `"`) && strings.HasSuffix(target, `"`):
		target = target[1 : len(target)-1]
	case len(target) > 1 && strings.HasPrefix(target, "'") && strings.HasSuffix(target, "'"):
		target = target[1 : len(target)-1]
	case strings.ContainsAny(target, " \t"):
		return "", false
	}
	if target == "" {
		return "", false
	}
	if strings.HasPrefix(target, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		target = filepath.Join(home, strings.TrimPrefix(target, "~"))
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	return filepath.Clean(target), true
}

// reportDirectoryChange applies a `cd` and writes the outcome into the shell row
// that started it. The working directory only moves when the hook accepts it, so
// a rejected change leaves everything as it was.
func (c *Chat) reportDirectoryChange(index int, path string) {
	c.mu.Lock()
	current := c.cwd
	hook := c.onDirectoryChange
	var entry *ShellEntry
	// The transcript can be replaced between the command starting and its
	// completion, so the row must be re-checked rather than assumed.
	if index >= 0 && index < len(c.entries) {
		entry = c.entries[index].shell
	}
	c.mu.Unlock()
	if entry == nil {
		return
	}

	output, status := "", "complete"
	switch {
	case hook == nil:
		output, status = "changing directory is not supported here", "error"
	default:
		if err := hook(path); err != nil {
			output, status = err.Error(), "error"
			break
		}
		c.mu.Lock()
		c.cwd = path
		c.mu.Unlock()
		output = compactPath(path)
		if current != "" && current != path {
			output = compactPath(current) + " → " + compactPath(path)
		}
	}

	c.mu.Lock()
	entry.Output, entry.Status = output, status
	snapshot := *entry
	c.mu.Unlock()
	if c.onShell != nil {
		c.onShell(snapshot)
	}
	c.repaint()
}

// Environment is the working-directory context the header displays.
type Environment struct {
	CWD          string
	Branch       string
	ContextPaths []string
	SkillGroups  [][]string
	MCPNames     []string
}

// SetEnvironment refreshes the header after the working directory changed.
func (c *Chat) SetEnvironment(env Environment) {
	c.mu.Lock()
	if env.CWD != "" {
		c.cwd = env.CWD
	}
	c.branch = env.Branch
	c.contextPaths = append([]string(nil), env.ContextPaths...)
	c.skillGroups = cloneGroups(env.SkillGroups)
	c.mcpNames = append([]string(nil), env.MCPNames...)
	c.mu.Unlock()
	c.repaint()
}

// cloneGroups copies a group list so the header never aliases caller state.
func cloneGroups(groups [][]string) [][]string {
	if groups == nil {
		return nil
	}
	cloned := make([][]string, 0, len(groups))
	for _, group := range groups {
		cloned = append(cloned, append([]string(nil), group...))
	}
	return cloned
}

func (c *Chat) startShell(command string, exclude bool) {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	entry := ShellEntry{Command: command, Exclude: exclude, Status: "running", At: time.Now().UnixMilli()}
	c.entries = append(c.entries, chatEntry{kind: chatShell, shell: &entry})
	index := len(c.entries) - 1
	ctx, cancel := context.WithCancel(c.ctx)
	c.shellCancel = cancel
	c.active = -1
	c.busy = true
	c.statusText = ""
	c.statusFallback = "Running shell command"
	c.statusGeneration++
	c.statusGenerating = false
	c.mu.Unlock()
	c.repaint()

	if target, isCD := cdTarget(c.cwd, command); isCD {
		// A directory change is handled here rather than in a subshell, which is
		// what makes it persist. This row reports the result.
		c.finishShell(index, 0, false, nil)
		c.reportDirectoryChange(index, target)
		return
	}
	go func() {
		exitCode, cancelled, runErr := c.shellRunner(ctx, c.cwd, command, func(output string) {
			c.mu.Lock()
			if index < len(c.entries) && c.entries[index].shell != nil {
				c.entries[index].shell.Output += output
			}
			c.mu.Unlock()
			c.repaint()
		})
		c.finishShell(index, exitCode, cancelled, runErr)
	}()
}

// startParallelShell runs an explicitly steered shell command without
// replacing or aborting the active model run.
func (c *Chat) startParallelShell(command string, exclude bool) bool {
	c.mu.Lock()
	if c.closing || c.shellCancel != nil {
		c.mu.Unlock()
		return false
	}
	entry := ShellEntry{Command: command, Exclude: exclude, Status: "running", At: time.Now().UnixMilli()}
	c.entries = append(c.entries, chatEntry{kind: chatShell, shell: &entry})
	index := len(c.entries) - 1
	ctx, cancel := context.WithCancel(c.ctx)
	c.shellCancel = cancel
	parallel := c.busy
	if !parallel {
		c.active = -1
		c.busy = true
		c.statusText = ""
		c.statusFallback = "Running shell command"
		c.statusGeneration++
		c.statusGenerating = false
	}
	c.mu.Unlock()
	c.repaint()

	go func() {
		exitCode, cancelled, runErr := c.shellRunner(ctx, c.cwd, command, func(output string) {
			c.mu.Lock()
			if index < len(c.entries) && c.entries[index].shell != nil {
				c.entries[index].shell.Output += output
			}
			c.mu.Unlock()
			c.repaint()
		})
		if parallel {
			c.finishParallelShell(index, exitCode, cancelled, runErr)
		} else {
			c.finishShell(index, exitCode, cancelled, runErr)
		}
	}()
	return true
}

func (c *Chat) finishParallelShell(index, exitCode int, cancelled bool, runErr error) {
	c.mu.Lock()
	if index >= len(c.entries) || c.entries[index].shell == nil {
		c.shellCancel = nil
		c.mu.Unlock()
		return
	}
	entry := c.entries[index].shell
	entry.Status = "complete"
	if cancelled {
		entry.Status = "cancelled"
	} else if runErr != nil || exitCode != 0 {
		entry.Status = "error"
	}
	if exitCode >= 0 {
		code := exitCode
		entry.ExitCode = &code
	}
	if runErr != nil && exitCode < 0 && !cancelled && strings.TrimSpace(entry.Output) == "" {
		entry.Output = runErr.Error() + "\n"
	}
	record := *entry
	c.shellCancel = nil
	callback := c.onShell
	c.mu.Unlock()
	if callback != nil {
		callback(record)
	}
	c.repaint()
}

func (c *Chat) finishShell(index, exitCode int, cancelled bool, runErr error) {
	c.mu.Lock()
	if index >= len(c.entries) || c.entries[index].shell == nil {
		c.busy = false
		c.shellCancel = nil
		c.mu.Unlock()
		return
	}
	entry := c.entries[index].shell
	entry.Status = "complete"
	if cancelled {
		entry.Status = "cancelled"
	} else if runErr != nil || exitCode != 0 {
		entry.Status = "error"
	}
	if exitCode >= 0 {
		code := exitCode
		entry.ExitCode = &code
	}
	if runErr != nil && exitCode < 0 && !cancelled && strings.TrimSpace(entry.Output) == "" {
		entry.Output = runErr.Error() + "\n"
	}
	record := *entry
	c.active = -1
	c.busy = false
	c.shellCancel = nil
	c.statusText = ""
	c.statusFallback = ""
	c.statusGeneration++
	c.statusGenerating = false
	var next string
	if len(c.queue) > 0 && !c.queueHeld && !c.queueDispatching {
		next = c.queue[0]
		c.queue = c.queue[1:]
	}
	closing := c.closing
	queue, held, queueCallback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	shellCallback := c.onShell
	c.mu.Unlock()
	if shellCallback != nil {
		shellCallback(record)
	}
	if queueCallback != nil {
		queueCallback(queue, held)
	}
	c.repaint()
	if next != "" && !closing {
		time.AfterFunc(50*time.Millisecond, func() { c.dispatch(next) })
	}
}

func (c *Chat) dispatch(value string) {
	if command, exclude, shell := parseShellCommand(value); shell {
		if command != "" {
			c.startShell(command, exclude)
		}
		return
	}
	trimmed := strings.TrimSpace(value)
	c.mu.Lock()
	provider, model := c.provider, c.model
	c.mu.Unlock()
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		c.editor.SetText(value)
		if strings.TrimSpace(provider) == "" {
			c.ShowToast("No provider selected.", ToastWarning, 2500*time.Millisecond)
		} else {
			c.ShowToast("No model selected.", ToastWarning, 2500*time.Millisecond)
		}
		return
	}
	c.start(trimmed, trimmed)
}

func (c *Chat) failStart(prompt string, err error) {
	c.mu.Lock()
	failedIndex := c.active
	if c.busy && c.active == len(c.entries)-1 && len(c.entries) >= 2 {
		c.entries = c.entries[:len(c.entries)-2]
	}
	c.clearExpansionForEntryLocked(failedIndex)
	c.active = -1
	c.busy = false
	c.statusText = ""
	c.statusFallback = ""
	c.statusGeneration++
	c.statusGenerating = false
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	c.mu.Unlock()
	if callback != nil {
		callback(queue, held)
	}
	c.editor.SetText(prompt)
	c.setStatus("Error: " + err.Error())
}

func (c *Chat) consume(run ChatRun) {
	for {
		event, ok, err := run.Next(c.ctx)
		if err != nil {
			c.finishRun(err)
			return
		}
		if !ok {
			c.finishRun(nil)
			return
		}
		c.applyEvent(event)
		if event.Type == agent.EventAgentEnd {
			c.finishRun(nil)
			return
		}
	}
}

// writingToolLabel names what the model is composing: a shell command for the
// bash tool, nothing for anything else, so ordinary calls do not claim a label.
func writingToolLabel(partial *ai.AssistantMessage) string {
	if partial == nil {
		return ""
	}
	for index := len(partial.Content) - 1; index >= 0; index-- {
		call, ok := partial.Content[index].(ai.ToolCall)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(call.Name)) {
		case "bash", "shell":
			return "Writing Command…"
		}
		return ""
	}
	return ""
}

func (c *Chat) applyEvent(event agent.Event) {
	if event.Type == agent.EventNotice {
		if notice := strings.TrimSpace(event.Notice); notice != "" {
			c.setStatus(notice)
		}
		return
	}
	var toastError string
	generateStatus := false
	c.mu.Lock()
	if c.active < 0 || c.active >= len(c.entries) {
		c.mu.Unlock()
		return
	}
	active := &c.entries[c.active]
	switch event.Type {
	case agent.EventTurnStart:
		c.beginSteeredTurnLocked()
	case agent.EventMessageUpdate:
		if event.AssistantEvent != nil {
			switch event.AssistantEvent.Type {
			case ai.EventTextDelta:
				active.text += event.AssistantEvent.Delta
				active.appendDelta(chatSegmentText, event.AssistantEvent.Delta, time.Now().UnixMilli())
				chars := utf8.RuneCountInString(event.AssistantEvent.Delta)
				c.generation.Add(chars, time.Now())
				c.liveContext.AddChars(chars)
				c.statusFallback = "Writing response"
				generateStatus = true
			case ai.EventToolCallStart, ai.EventToolCallDelta:
				c.writingLabel = writingToolLabel(event.AssistantEvent.Partial)
			case ai.EventThinkingDelta:
				active.thinking += event.AssistantEvent.Delta
				active.appendDelta(chatSegmentThinking, event.AssistantEvent.Delta, time.Now().UnixMilli())
				c.generation.Add(utf8.RuneCountInString(event.AssistantEvent.Delta), time.Now())
				c.liveContext.AddChars(utf8.RuneCountInString(event.AssistantEvent.Delta))
				c.statusFallback = "Thinking"
				generateStatus = true
			}
		}
	case agent.EventMessageEnd:
		if message, ok := asUserMessage(event.Message); ok {
			c.markSteerLandedLocked(userMessageText(message))
			break
		}
		if active.text == "" {
			active.text = messageText(event.Message)
			if active.text != "" && !active.hasSegmentKind(chatSegmentText) {
				active.appendDelta(chatSegmentText, active.text, time.Now().UnixMilli())
			}
		}
		if message, ok := assistantMessage(event.Message); ok {
			toastError = message.ErrorMessage
			// Cost is cumulative across the session; the footer's ctx pair is the
			// context size this message leaves behind (cached input is context, not
			// generated output).
			addUsage(&c.usage, message.Usage)
			c.liveContext.UpdateUsage(message.Usage)
			if size := contextSize(message.Usage); size > 0 {
				c.contextTokens, c.contextKnown = size, true
			}
			// Match the pre-port Midas: streamed characters give the live reading
			// and provider usage settles it; only non-reasoning models calibrate
			// the chars/token ratio, because hidden reasoning would skew it.
			c.generation.Finish(message.Usage.Output, !c.modelReasoning, time.Now())
		}
	case agent.EventCompaction:
		// A compaction is reported like a tool: it did work, and the sizes it
		// replaced are the whole point of showing it. A failure keeps the same
		// row with the error glyph and the reason.
		toolID := fmt.Sprintf("compact:%d", time.Now().UnixNano())
		arguments := map[string]any{"before": float64(event.TokensBefore), "after": float64(event.TokensAfter)}
		failed := event.Err != nil
		if failed {
			arguments["error"] = event.Err.Error()
		}
		active.tools = append(active.tools, chatTool{
			id: toolID, name: "compact", status: "done", isError: failed, args: arguments,
		})
		active.startToolSegment(toolID, time.Now().UnixMilli())
		if failed {
			c.setStatus("Compaction failed: " + event.Err.Error())
		}
	case agent.EventToolExecutionStart:
		c.writingLabel = ""
		if !active.hasTool(event.ToolCallID) {
			active.tools = append(active.tools, chatTool{id: event.ToolCallID, name: event.ToolName, args: cloneArguments(event.Arguments), status: "running"})
			active.startToolSegment(event.ToolCallID, time.Now().UnixMilli())
		} else {
			for index := range active.tools {
				if active.tools[index].id == event.ToolCallID {
					active.tools[index].name = event.ToolName
					active.tools[index].args = cloneArguments(event.Arguments)
					active.tools[index].status = "running"
					break
				}
			}
		}
		c.statusFallback = cleanStatusPhrase("Running "+event.ToolName, c.statusMaxWords)
		generateStatus = true
	case agent.EventToolExecutionUpdate:
		for index := range active.tools {
			if active.tools[index].id == event.ToolCallID {
				active.tools[index].status = "running"
				active.tools[index].result = toolResultText(event.PartialResult)
			}
		}
		c.statusFallback = cleanStatusPhrase("Running "+event.ToolName, c.statusMaxWords)
	case agent.EventToolExecutionEnd:
		found := false
		for index := range active.tools {
			if active.tools[index].id == event.ToolCallID {
				active.tools[index].status = "done"
				active.tools[index].isError = event.IsError
				active.tools[index].result = toolResultText(event.Result)
				found = true
				active.finishToolSegment(event.ToolCallID, time.Now().UnixMilli())
			}
		}
		if !found {
			active.tools = append(active.tools, chatTool{id: event.ToolCallID, name: event.ToolName, args: cloneArguments(event.Arguments), result: toolResultText(event.Result), status: "done", isError: event.IsError})
			now := time.Now().UnixMilli()
			active.segments = append(active.segments, chatSegment{kind: chatSegmentTool, toolID: event.ToolCallID, startedAt: now, endedAt: now})
		}
		c.statusFallback = "Working"
		generateStatus = true
	case agent.EventAgentEnd:
		if active.text == "" {
			for _, eventMessage := range slices.Backward(event.Messages) {
				if message, ok := assistantMessage(eventMessage); ok && toastError == "" {
					toastError = message.ErrorMessage
				}
				if text := messageText(eventMessage); text != "" {
					active.text = text
					break
				}
			}
		}
	}
	c.mu.Unlock()
	if toastError != "" {
		c.ShowToast("Error: "+toastError, ToastError, 5*time.Second)
		return
	}
	c.repaint()
	if generateStatus {
		c.maybeGenerateStatus()
	}
}

func (c *Chat) markSteerLandedLocked(text string) {
	for index := range c.entries {
		entry := &c.entries[index]
		if entry.kind == chatUser && entry.steerPending && entry.text == text {
			entry.steerPending = false
			entry.steerLanded = true
			return
		}
	}
}

func (c *Chat) beginSteeredTurnLocked() {
	insertAt := -1
	for index := range c.entries {
		if c.entries[index].steerLanded {
			c.entries[index].steerLanded = false
			insertAt = index + 1
		}
	}
	if insertAt < 0 {
		return
	}
	now := time.Now().UnixMilli()
	// The continuation becomes the active block, so close the block it
	// supersedes. Without an end time its collapsed "Worked" header could never
	// report a duration.
	if c.active >= 0 && c.active < len(c.entries) && c.entries[c.active].endedAt == 0 {
		c.entries[c.active].endedAt = now
		c.entries[c.active].finishOpenSegments(now)
	}
	entry := chatEntry{kind: chatAssistant, startedAt: now}
	c.entries = append(c.entries, chatEntry{})
	copy(c.entries[insertAt+1:], c.entries[insertAt:])
	c.entries[insertAt] = entry
	c.active = insertAt
	c.clearExpansionForEntryLocked(insertAt)
	c.statusFallback = "Working"
}

func (c *Chat) finishRun(err error) {
	c.mu.Lock()
	if !c.busy {
		c.mu.Unlock()
		return
	}
	showError := err != nil && !errors.Is(err, context.Canceled)
	if c.active >= 0 && c.active < len(c.entries) {
		c.entries[c.active].endedAt = time.Now().UnixMilli()
		c.entries[c.active].finishOpenSegments(c.entries[c.active].endedAt)
	}
	c.active = -1
	c.busy = false
	c.statusText = ""
	c.statusFallback = ""
	c.statusGeneration++
	c.statusGenerating = false
	var next string
	if len(c.queue) > 0 && !c.queueHeld && !c.queueDispatching {
		next = c.queue[0]
		c.queue = c.queue[1:]
	}
	closing := c.closing
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	reverting := c.reverting
	c.reverting = false
	c.mu.Unlock()
	if callback != nil {
		callback(queue, held)
	}
	if reverting {
		if reverter, canUndo := c.backend.(ChatReverter); canUndo {
			c.applyUndo(reverter)
		}
		return
	}
	if showError {
		c.ShowToast("Error: "+err.Error(), ToastError, 5*time.Second)
	}
	c.repaint()
	if next != "" && !closing {
		time.AfterFunc(50*time.Millisecond, func() { c.dispatch(next) })
	}
}

func (c *Chat) setStatus(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	lower := strings.ToLower(value)
	level, duration := ToastSuccess, 3*time.Second
	if strings.HasPrefix(lower, "error:") || strings.Contains(lower, "failed") {
		level, duration = ToastError, 5*time.Second
	} else if strings.HasPrefix(lower, "usage:") || strings.Contains(lower, "unknown command") || strings.Contains(lower, "aborted") || strings.Contains(lower, "stopped") || strings.HasPrefix(lower, "nothing ") {
		level, duration = ToastWarning, 4*time.Second
	}
	c.ShowToast(value, level, duration)
}

// SetPadding changes the space Midas keeps between its content and the terminal
// borders, and repaints when the layout moved.
func (c *Chat) SetPadding(value int) {
	if !SetPadding(value) {
		return
	}
	c.repaint()
}

func (c *Chat) repaint() {
	if c.request != nil {
		c.request()
	}
}

func (c *Chat) Render(width int) []string {
	width = max(1, width)
	lines := append([]string{}, c.renderHeader(width)...)
	lines = append(lines, c.renderDocument(width)...)
	lines = append(lines, "")
	lines = append(lines, c.renderPending(width)...)
	lines = append(lines, c.renderDock(width)...)
	lines = append(lines, c.renderFooter(width)...)
	c.mu.Lock()
	legacy := []string{c.session}
	if c.agent != "" && c.agent != "main" {
		legacy = append(legacy, c.agent)
	}
	if c.thinking != "" && c.thinking != ai.ThinkingOff {
		legacy = append(legacy, "thinking "+string(c.thinking))
	}
	c.mu.Unlock()
	if metadata := strings.TrimSpace(strings.Join(legacy, "  •  ")); metadata != "" {
		lines = append(lines, dim(tuitext.TruncateToWidth(metadata, width, "…", true)))
	}
	return lines
}

func (c *Chat) renderTranscript(width int) []string {
	return c.renderTranscriptRuns(width)
}

func renderShellCard(entry ShellEntry, width int) []string {
	if width < 4 {
		return tuitext.WrapTextWithAnsi("$ "+entry.Command, width)
	}
	theme := CurrentTheme()
	border := func(value string) string { return theme.FG("bashMode", value) }
	inner := width - 4
	content := []string{theme.FG("bashMode", "$ "+entry.Command)}
	cleanOutput := strings.TrimRight(tuitext.StripTerminalSequences(entry.Output), "\r\n")
	if cleanOutput != "" {
		content = append(content, "")
		logical := strings.Split(strings.ReplaceAll(cleanOutput, "\r", "\n"), "\n")
		hidden := max(0, len(logical)-20)
		if hidden > 0 {
			logical = logical[len(logical)-20:]
		}
		for _, line := range logical {
			wrapped := tuitext.WrapTextWithAnsi(theme.FG("muted", line), max(1, inner))
			if len(wrapped) == 0 {
				wrapped = []string{""}
			}
			content = append(content, wrapped...)
		}
		if hidden > 0 {
			content = append(content, theme.FG("muted", fmt.Sprintf("... %d more lines", hidden)))
		}
	}
	if entry.Status == "running" {
		content = append(content, "", theme.FG("bashMode", "● ")+theme.FG("muted", "Running… (esc to cancel)"))
	} else if entry.Status == "cancelled" {
		content = append(content, "", theme.FG("warning", "(cancelled)"))
	} else if entry.Status == "error" {
		status := "(failed)"
		if entry.ExitCode != nil {
			status = fmt.Sprintf("(exit %d)", *entry.ExitCode)
		}
		content = append(content, "", theme.FG("error", status))
	}
	edge := func(value string) string {
		return tuitext.DecorationMarker + ContentStartMarker + ContentEndMarker + value
	}
	lines := []string{edge(border("╭" + strings.Repeat("─", width-2) + "╮"))}
	for _, line := range content {
		fitted := tuitext.TruncateToWidth(line, inner, "", true)
		lines = append(lines, border("│")+" "+ContentStartMarker+fitted+ContentEndMarker+strings.Repeat(" ", max(0, inner-tuitext.VisibleWidth(fitted)))+" "+border("│"))
	}
	return append(lines, edge(border("╰"+strings.Repeat("─", width-2)+"╯")))
}

func (c *Chat) renderPending(width int) []string {
	c.mu.Lock()
	queue := append([]string(nil), c.queue...)
	editing := c.queueEdit
	c.mu.Unlock()
	if len(queue) == 0 {
		return nil
	}
	theme := CurrentTheme()
	margin, available := chatRowGeometry(width)
	inset := strings.Repeat(" ", margin)
	lines := make([]string, 0, len(queue)+1)
	targets := make([]pendingTarget, 0, len(queue))
	label := tuitext.TruncateToWidth("Queue:", available, "", false)
	lines = append(lines, inset+theme.FG("muted", label))
	for index, value := range queue {
		prefix := fmt.Sprintf("%d. ", index+1)
		preview := strings.Join(strings.Fields(value), " ")
		row := prefix + preview
		if index == editing {
			row = theme.FG("text", row)
		} else {
			row = theme.FG("dim", row)
		}
		targets = append(targets, pendingTarget{index: index, start: len(lines), end: len(lines) + 1})
		lines = append(lines, inset+tuitext.TruncateToWidth(row, available, "…", false))
	}
	c.mu.Lock()
	c.pendingTargets = targets
	c.mu.Unlock()
	return lines
}

// handlePendingMouse pulls a clicked queued message out of the queue and into the
// editor so it can be edited or discarded. A message already being edited goes
// back to its slot first, so clicking a second row never loses the first edit.
func (c *Chat) handlePendingMouse(event MouseEvent) *MouseResult {
	if event.Type != MouseClick || event.Button != MouseLeft {
		return nil
	}
	c.mu.Lock()
	for _, target := range c.pendingTargets {
		if event.Y < target.start || event.Y >= target.end {
			continue
		}
		if target.index < 0 || target.index >= len(c.queue) {
			break
		}
		previous := c.queueEdit
		value := c.queue[target.index]
		c.queue = append(c.queue[:target.index], c.queue[target.index+1:]...)
		if previous >= 0 && previous < len(c.queue)+1 {
			if current := strings.TrimSpace(c.editor.Text()); current != "" {
				slot := min(previous, len(c.queue))
				c.queue = append(c.queue, "")
				copy(c.queue[slot+1:], c.queue[slot:])
				c.queue[slot] = c.editor.Text()
			}
		}
		c.queueEdit = target.index
		queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
		c.mu.Unlock()
		c.editor.SetText(value)
		if callback != nil {
			callback(queue, held)
		}
		c.setStatus("Editing queued message " + fmt.Sprintf("%d", target.index+1) + ". Enter returns it to the queue; clear it and press Enter to delete it.")
		c.repaint()
		return &MouseResult{Handled: true}
	}
	c.mu.Unlock()
	return nil
}

// finishQueueEdit returns an edited queued message to the slot it came from, or
// deletes it when the editor was cleared. The editor always ends up empty, so
// the next Enter queues a new message and Cmd+Enter steers one as usual.
func (c *Chat) finishQueueEdit(value, trimmed string) {
	c.mu.Lock()
	index := c.queueEdit
	c.queueEdit = -1
	if index >= 0 && trimmed != "" {
		index = min(index, len(c.queue))
		c.queue = append(c.queue, "")
		copy(c.queue[index+1:], c.queue[index:])
		c.queue[index] = value
	}
	queue, held, callback := append([]string(nil), c.queue...), c.queueHeld, c.onQueue
	c.mu.Unlock()
	c.editor.SetText("")
	if callback != nil {
		callback(queue, held)
	}
	if trimmed == "" {
		c.setStatus("Removed the queued message.")
	} else {
		c.setStatus("Updated the queued message.")
	}
	c.repaint()
}

func assistantMessage(message ai.Message) (ai.AssistantMessage, bool) {
	switch value := message.(type) {
	case ai.AssistantMessage:
		return value, true
	case *ai.AssistantMessage:
		if value != nil {
			return *value, true
		}
	}
	return ai.AssistantMessage{}, false
}

func asUserMessage(message ai.Message) (ai.UserMessage, bool) {
	switch value := message.(type) {
	case ai.UserMessage:
		return value, true
	case *ai.UserMessage:
		if value != nil {
			return *value, true
		}
	}
	return ai.UserMessage{}, false
}

func addUsage(total *ai.Usage, usage ai.Usage) {
	total.Input += usage.Input
	total.Output += usage.Output
	total.CacheRead += usage.CacheRead
	total.CacheWrite += usage.CacheWrite
	total.Cost.Input += usage.Cost.Input
	total.Cost.Output += usage.Cost.Output
	total.Cost.CacheRead += usage.Cost.CacheRead
	total.Cost.CacheWrite += usage.Cost.CacheWrite
	total.RecalculateTotals()
}

// contextSize is the context a message leaves behind: its input (including cache
// reads and writes) plus its output, which the next prompt resends.
func contextSize(usage ai.Usage) int {
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// transcriptContext is the context size of the last assistant message in a
// resumed transcript, and whether one was found.
func transcriptContext(messages []ai.Message) (int, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		assistant, ok := assistantMessage(messages[index])
		if !ok {
			continue
		}
		if size := contextSize(assistant.Usage); size > 0 {
			return size, true
		}
	}
	return 0, false
}

// contextReading resolves the footer's ctx pair: the live estimate while a turn
// streams, sampled at most once a second, otherwise the last known context.
func (c *Chat) contextReading(now time.Time) (int, bool) {
	tokens, known := c.liveContext.Read(c.contextTokens, c.modelContext, c.contextKnown)
	tokens, ok := c.contextDisplay.Read(tokens, c.modelContext, known && c.liveContext.active, now)
	if !ok || tokens <= 0 || c.modelContext <= 0 {
		return 0, false
	}
	return tokens, true
}

func transcriptUsage(messages []ai.Message) ai.Usage {
	var result ai.Usage
	for _, message := range messages {
		if assistant, ok := assistantMessage(message); ok {
			addUsage(&result, assistant.Usage)
		}
	}
	return result
}

func cloneStringGroups(groups [][]string) [][]string {
	result := make([][]string, len(groups))
	for index := range groups {
		result[index] = append([]string(nil), groups[index]...)
	}
	return result
}

func cloneArguments(arguments map[string]any) map[string]any {
	return maps.Clone(arguments)
}

func toolArgumentSummary(arguments map[string]any) string {
	for _, key := range []string{"path", "command", "query", "action", "id", "title"} {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return singleLine(value, 80)
		}
	}
	keys := slices.Sorted(maps.Keys(arguments))
	for _, key := range keys {
		switch value := arguments[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return key + "=" + singleLine(value, 72)
			}
		case float64, bool:
			encoded, _ := json.Marshal(value)
			return key + "=" + string(encoded)
		}
	}
	return ""
}

func toolResultText(result *agent.ToolResult) string {
	if result == nil {
		return ""
	}
	var output strings.Builder
	for _, content := range result.Content {
		switch value := content.(type) {
		case ai.TextContent:
			output.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				output.WriteString(value.Text)
			}
		}
	}
	return output.String()
}

func messageText(message ai.Message) string {
	assistant, ok := message.(ai.AssistantMessage)
	if !ok {
		if pointer, pointerOK := message.(*ai.AssistantMessage); pointerOK && pointer != nil {
			assistant, ok = *pointer, true
		}
	}
	if !ok {
		return ""
	}
	var result strings.Builder
	for _, block := range assistant.Content {
		switch value := block.(type) {
		case ai.TextContent:
			result.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				result.WriteString(value.Text)
			}
		}
	}
	return result.String()
}

func transcriptEntries(messages []ai.Message) []chatEntry {
	return buildTranscriptEntries(messages)
}

func storedPromptBorderStyle(message ai.UserMessage) func(string) string {
	if message.Agent == "" && message.Thinking == "" {
		return nil
	}
	level := message.Thinking
	if level == "" {
		level = ai.ThinkingOff
	}
	return func(value string) string { return CurrentTheme().Thinking(level, value) }
}

func userMessageText(message ai.UserMessage) string {
	if text, ok := message.Content.Text(); ok {
		return text
	}
	var result strings.Builder
	for _, part := range message.Content.Parts() {
		switch value := part.(type) {
		case ai.TextContent:
			result.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				result.WriteString(value.Text)
			}
		}
	}
	return result.String()
}

func renderPromptCard(value string, width int, borderStyle func(string) string) []string {
	if width < 4 {
		return tuitext.WrapTextWithAnsi(value, width)
	}
	inner := width - 4
	wrapped := tuitext.WrapTextWithAnsi(value, inner)
	theme := CurrentTheme()
	border := borderStyle
	if border == nil {
		border = func(text string) string { return theme.FG("borderAccent", text) }
	}
	edge := func(value string) string {
		return tuitext.DecorationMarker + ContentStartMarker + ContentEndMarker + value
	}
	result := []string{edge(border("╭" + strings.Repeat("─", width-2) + "╮"))}
	for _, line := range wrapped {
		content := StyleAttachmentMarkers(theme, theme.FG("userMessageText", line), "userMessageText")
		result = append(result, border("│")+" "+ContentStartMarker+content+ContentEndMarker+strings.Repeat(" ", max(0, inner-tuitext.VisibleWidth(line)))+" "+border("│"))
	}
	return append(result, edge(border("╰"+strings.Repeat("─", width-2)+"╯")))
}

func singleLine(value string, width int) string {
	value = strings.Join(strings.Fields(value), " ")
	return tuitext.TruncateToWidth(value, width, "…", false)
}

func compactPath(value string) string {
	if value == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if value == home {
			return "~/"
		}
		if strings.HasPrefix(value, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(value, home)
		}
	}
	return value
}
func bold(value string) string { return "\x1b[1m" + value + "\x1b[22m" }
func dim(value string) string  { return CurrentTheme().FG("dim", value) }
