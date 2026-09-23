// Package remote exposes the running Midas terminal through a password-gated
// browser session and an account-free public tunnel.
package remote

import (
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/internal/tui"
)

const (
	defaultColumns = 80
	defaultRows    = 24
	clientQueue    = 256
)

// Terminal mirrors every render to connected browser clients while preserving
// the local process terminal. Browser input is fed into the same TUI input
// callback, so remote access controls the current session rather than spawning
// a second Midas process.
type Terminal struct {
	local tui.Terminal

	mu            sync.Mutex
	inputMu       sync.Mutex
	onInput       func(string)
	onResize      func()
	requestRender func()
	clients       map[uint64]*Client
	nextClientID  uint64
	activeID      uint64
}

// Client is one attached browser terminal.
type Client struct {
	id      uint64
	term    *Terminal
	output  chan string
	cols    int
	rows    int
	closed  bool
	closeMu sync.Once
}

// NewTerminal wraps the local terminal with remote mirroring support.
func NewTerminal(local tui.Terminal) *Terminal {
	return &Terminal{local: local, clients: make(map[uint64]*Client)}
}

// SetRequestRender installs the callback used to paint a complete frame for a
// newly attached browser or after its viewport changes.
func (t *Terminal) SetRequestRender(request func()) {
	t.mu.Lock()
	t.requestRender = request
	t.mu.Unlock()
}

func (t *Terminal) Start(onInput func(string), onResize func()) {
	dispatchInput := func(data string) {
		t.inputMu.Lock()
		defer t.inputMu.Unlock()
		onInput(data)
	}
	t.mu.Lock()
	t.onInput = dispatchInput
	t.onResize = onResize
	t.mu.Unlock()
	t.local.Start(dispatchInput, func() {
		t.mu.Lock()
		remoteActive := len(t.clients) > 0
		resize := t.onResize
		t.mu.Unlock()
		if !remoteActive && resize != nil {
			resize()
		}
	})
}

func (t *Terminal) Stop() {
	t.mu.Lock()
	clients := make([]*Client, 0, len(t.clients))
	for _, client := range t.clients {
		clients = append(clients, client)
	}
	t.onInput = nil
	t.onResize = nil
	t.requestRender = nil
	t.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
	t.local.Stop()
}

func (t *Terminal) DrainInput(maxDuration, idleDuration time.Duration) error {
	return t.local.DrainInput(maxDuration, idleDuration)
}

func (t *Terminal) Write(data string) {
	t.local.Write(data)
	t.mu.Lock()
	stalled := make([]*Client, 0)
	for _, client := range t.clients {
		select {
		case client.output <- data:
		default:
			// A stalled browser must not block the local TUI. Disconnecting it is
			// safer than dropping ANSI deltas and leaving a corrupted screen.
			stalled = append(stalled, client)
		}
	}
	t.mu.Unlock()
	for _, client := range stalled {
		client.Close()
	}
}

func (t *Terminal) Columns() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if client := t.clients[t.activeID]; client != nil {
		return clampDimension(client.cols, defaultColumns, 20, 500)
	}
	return t.local.Columns()
}

func (t *Terminal) Rows() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if client := t.clients[t.activeID]; client != nil {
		return clampDimension(client.rows, defaultRows, 5, 300)
	}
	return t.local.Rows()
}

func (t *Terminal) KittyProtocolActive() bool { return t.local.KittyProtocolActive() }
func (t *Terminal) MoveBy(lines int)          { t.Write(moveSequence(lines)) }
func (t *Terminal) HideCursor()               { t.Write("\x1b[?25l") }
func (t *Terminal) ShowCursor()               { t.Write("\x1b[?25h") }
func (t *Terminal) ClearLine()                { t.Write("\x1b[K") }
func (t *Terminal) ClearFromCursor()          { t.Write("\x1b[J") }
func (t *Terminal) ClearScreen()              { t.Write("\x1b[2J\x1b[H") }
func (t *Terminal) SetTitle(title string)     { t.Write("\x1b]0;" + title + "\x07") }
func (t *Terminal) SetProgress(active bool)   { t.local.SetProgress(active) }

// Attach adds a browser using its current cell dimensions.
func (t *Terminal) Attach(cols, rows int) *Client {
	client := &Client{term: t, output: make(chan string, clientQueue)}
	t.mu.Lock()
	t.nextClientID++
	client.id = t.nextClientID
	client.cols = clampDimension(cols, defaultColumns, 20, 500)
	client.rows = clampDimension(rows, defaultRows, 5, 300)
	t.clients[client.id] = client
	t.activeID = client.id
	// The clear frame is sent while holding the lock that Close takes before it
	// closes the output channel, so the send can never hit a closed channel.
	client.output <- "\x1b[2J\x1b[H"
	resize, render := t.onResize, t.requestRender
	t.mu.Unlock()
	if resize != nil {
		resize()
	}
	if render != nil {
		render()
	}
	return client
}

// Output carries raw ANSI frames to the browser.
func (c *Client) Output() <-chan string { return c.output }

// Input feeds browser keystrokes into the running TUI.
func (c *Client) Input(data string) {
	c.term.mu.Lock()
	if c.closed || c.term.clients[c.id] != c {
		c.term.mu.Unlock()
		return
	}
	c.term.activeID = c.id
	input := c.term.onInput
	c.term.mu.Unlock()
	if input != nil {
		input(data)
	}
}

// Resize makes this browser the active viewport and repaints the TUI to fit it.
func (c *Client) Resize(cols, rows int) {
	cols = clampDimension(cols, defaultColumns, 20, 500)
	rows = clampDimension(rows, defaultRows, 5, 300)
	c.term.mu.Lock()
	if c.closed || c.term.clients[c.id] != c {
		c.term.mu.Unlock()
		return
	}
	changed := c.cols != cols || c.rows != rows || c.term.activeID != c.id
	c.cols, c.rows = cols, rows
	c.term.activeID = c.id
	resize, render := c.term.onResize, c.term.requestRender
	c.term.mu.Unlock()
	if !changed {
		return
	}
	if resize != nil {
		resize()
	}
	if render != nil {
		render()
	}
}

// Close detaches this browser and restores local terminal dimensions when the
// final browser leaves.
func (c *Client) Close() {
	c.closeMu.Do(func() {
		c.term.mu.Lock()
		if c.term.clients[c.id] == c {
			delete(c.term.clients, c.id)
		}
		c.closed = true
		wasActive := c.term.activeID == c.id
		if wasActive {
			c.term.activeID = 0
			for id := range c.term.clients {
				if id > c.term.activeID {
					c.term.activeID = id
				}
			}
		}
		resize, render := c.term.onResize, c.term.requestRender
		close(c.output)
		c.term.mu.Unlock()
		if wasActive && resize != nil {
			resize()
		}
		if wasActive && render != nil {
			render()
		}
	})
}

func clampDimension(value, fallback, minimum, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	return min(maximum, max(minimum, value))
}

func moveSequence(lines int) string {
	if lines > 0 {
		return "\x1b[" + itoa(lines) + "B"
	}
	if lines < 0 {
		return "\x1b[" + itoa(-lines) + "A"
	}
	return ""
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
