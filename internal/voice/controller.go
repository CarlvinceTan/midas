// Package voice turns a microphone stream into text with a managed Parakeet
// recognizer, and speaks replies through the platform's text-to-speech.
package voice

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event is one JSONL event emitted by an STT helper.
type Event struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Message string `json:"message,omitempty"`
}

var eventTypes = map[string]bool{"ready": true, "listening": true, "paused": true, "partial": true, "final": true, "error": true}

func ParseLine(line string) (Event, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Event{}, false
	}
	var raw map[string]any
	if json.Unmarshal([]byte(line), &raw) != nil {
		return Event{}, false
	}
	kind, ok := raw["type"].(string)
	if !ok || !eventTypes[kind] {
		return Event{}, false
	}
	event := Event{Type: kind}
	if text, ok := raw["text"].(string); ok {
		event.Text = text
	}
	if message, ok := raw["message"].(string); ok {
		event.Message = message
	}
	return event, true
}
func ComposeText(base, committed, partial string) string {
	speech := appendTranscript(strings.TrimSpace(committed), partial)
	if speech == "" {
		return base
	}
	if base == "" || strings.HasSuffix(base, " ") || strings.HasSuffix(base, "\n") || strings.HasSuffix(base, "\t") {
		return base + speech
	}
	return base + " " + speech
}

func appendTranscript(current, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" || strings.HasSuffix(current, " ") || strings.HasSuffix(current, "\n") || strings.HasSuffix(current, "\t") {
		return current + next
	}
	return current + " " + next
}

type Process interface {
	Stdout() io.Reader
	Stdin() io.Writer
	PID() int
	Wait() error
	KillTree() error
}
type Spawn func(command string, args []string) (Process, error)
type Options struct {
	Command string
	Args    []string
	Spawn   Spawn
	OnText  func(committed, partial string)
	OnError func(string)
	OnReady func()
	OnStop  func()
}

// Controller owns a warm long-lived speech-to-text helper.
type Controller struct {
	mu        sync.Mutex
	sendMu    sync.Mutex
	options   Options
	child     Process
	committed string
	partial   string
	teardown  bool
	ready     bool
	listening bool
	fatal     bool
	pauseWait chan struct{}
}

func New(options Options) *Controller {
	if options.Spawn == nil {
		options.Spawn = spawnProcess
	}
	return &Controller{options: options}
}
func (c *Controller) Ready() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.ready }

func (c *Controller) Listen() {
	child, ok := c.ensureStarted()
	if !ok {
		return
	}
	c.mu.Lock()
	if c.listening {
		c.mu.Unlock()
		return
	}
	c.listening = true
	c.committed = ""
	c.partial = ""
	c.mu.Unlock()
	c.send(child, "listen")
}

func (c *Controller) Pause() {
	c.mu.Lock()
	if !c.listening {
		c.mu.Unlock()
		return
	}
	c.listening = false
	child := c.child
	c.mu.Unlock()
	if child != nil {
		c.send(child, "pause")
	}
}

// PauseAndWait keeps the session open until the helper emits its final text and
// acknowledges the pause. The TUI uses it before committing dictated text.
func (c *Controller) PauseAndWait(timeout time.Duration) {
	c.mu.Lock()
	if !c.listening {
		c.mu.Unlock()
		return
	}
	child := c.child
	if child == nil {
		c.listening = false
		c.mu.Unlock()
		return
	}
	wait := make(chan struct{})
	c.pauseWait = wait
	c.mu.Unlock()
	c.send(child, "pause")
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	select {
	case <-wait:
	case <-time.After(timeout):
	}
	c.mu.Lock()
	if c.pauseWait == wait {
		c.pauseWait = nil
	}
	c.listening = false
	c.mu.Unlock()
}

func (c *Controller) Stop() {
	c.mu.Lock()
	c.teardown = true
	c.listening = false
	child := c.child
	c.child = nil
	c.ready = false
	if c.pauseWait != nil {
		close(c.pauseWait)
		c.pauseWait = nil
	}
	c.mu.Unlock()
	if child == nil {
		return
	}
	c.send(child, "stop")
	_ = child.KillTree()
}

func (c *Controller) ensureStarted() (Process, bool) {
	c.mu.Lock()
	if c.child != nil {
		child := c.child
		c.mu.Unlock()
		return child, true
	}
	c.teardown = false
	c.ready = false
	c.fatal = false
	spawn := c.options.Spawn
	command := c.options.Command
	args := append([]string(nil), c.options.Args...)
	c.mu.Unlock()
	child, err := spawn(command, args)
	if err != nil {
		c.reportError(err.Error())
		return nil, false
	}
	c.mu.Lock()
	if c.child != nil {
		existing := c.child
		c.mu.Unlock()
		_ = child.KillTree()
		return existing, true
	}
	c.child = child
	c.mu.Unlock()
	go func() { c.readOutput(child); c.wait(child) }()
	return child, true
}

func (c *Controller) readOutput(child Process) {
	scanner := bufio.NewScanner(child.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		c.handleLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		c.reportError(err.Error())
	}
}
func (c *Controller) wait(child Process) {
	_ = child.Wait()
	c.mu.Lock()
	if c.child != child {
		c.mu.Unlock()
		return
	}
	c.child = nil
	c.ready = false
	c.listening = false
	if c.pauseWait != nil {
		close(c.pauseWait)
		c.pauseWait = nil
	}
	if c.teardown {
		c.mu.Unlock()
		return
	}
	if c.partial != "" {
		c.committed = appendTranscript(c.committed, c.partial)
		c.partial = ""
	}
	callback := c.options.OnStop
	c.mu.Unlock()
	if callback != nil {
		callback()
	}
}

func (c *Controller) handleLine(line string) {
	event, ok := ParseLine(line)
	if !ok {
		return
	}
	var readyCallback func()
	var textCallback func(string, string)
	var committed, partial string
	var errorMessage string
	c.mu.Lock()
	markReady := func() {
		if !c.ready {
			c.ready = true
			readyCallback = c.options.OnReady
		}
	}
	switch event.Type {
	case "ready":
		markReady()
	case "partial":
		markReady()
		if c.listening {
			if event.Text != "" || c.partial == "" {
				c.partial = event.Text
				textCallback = c.options.OnText
				committed, partial = c.committed, c.partial
			}
		}
	case "final":
		markReady()
		if !c.listening {
			c.partial = ""
		} else {
			c.committed = appendTranscript(c.committed, event.Text)
			c.partial = ""
			textCallback = c.options.OnText
			committed = c.committed
		}
	case "paused":
		if c.pauseWait != nil {
			close(c.pauseWait)
			c.pauseWait = nil
		}
	case "error":
		if !c.fatal {
			c.fatal = true
			errorMessage = event.Message
			if errorMessage == "" {
				errorMessage = "Speech recognition failed"
			}
		}
	}
	c.mu.Unlock()
	if readyCallback != nil {
		readyCallback()
	}
	if textCallback != nil {
		textCallback(committed, partial)
	}
	if errorMessage != "" {
		c.reportError(errorMessage)
		c.Stop()
	}
}

func (c *Controller) reportError(message string) {
	if callback := c.options.OnError; callback != nil {
		callback(message)
	}
}
func (c *Controller) send(child Process, command string) {
	writer := child.Stdin()
	if writer == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"type": command})
	payload = append(payload, '\n')
	c.sendMu.Lock()
	_, _ = writer.Write(payload)
	c.sendMu.Unlock()
}

type execProcess struct {
	command   *exec.Cmd
	stdout    io.ReadCloser
	stdin     io.WriteCloser
	owner     voiceProcessOwnership
	closeOnce sync.Once
}

func spawnProcess(command string, args []string) (Process, error) {
	cmd := exec.Command(command, args...)
	prepareVoiceCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	owner, err := ownVoiceProcess(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return &execProcess{command: cmd, stdout: stdout, stdin: stdin, owner: owner}, nil
}
func (p *execProcess) Stdout() io.Reader { return p.stdout }
func (p *execProcess) Stdin() io.Writer  { return p.stdin }
func (p *execProcess) PID() int          { return p.command.Process.Pid }
func (p *execProcess) closeOwner()       { p.closeOnce.Do(p.owner.close) }
func (p *execProcess) Wait() error       { err := p.command.Wait(); p.closeOwner(); return err }
func (p *execProcess) KillTree() error {
	err := p.owner.kill(p.command.Process)
	p.closeOwner()
	return err
}
