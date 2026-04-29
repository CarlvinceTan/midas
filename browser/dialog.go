package browser

import (
	"context"
	"errors"
	"sync"
)

type DialogType string

const (
	DialogTypeAlert        DialogType = "alert"
	DialogTypeConfirm      DialogType = "confirm"
	DialogTypePrompt       DialogType = "prompt"
	DialogTypeBeforeunload DialogType = "beforeunload"
)

var ErrDialogAlreadyHandled = errors.New("dialog already handled")

type Dialog struct {
	page          *Page
	frameID       string
	url           string
	dialogType    DialogType
	message       string
	defaultPrompt string

	mu      sync.RWMutex
	handled bool
}

func newDialog(page *Page, frameID, url string, dialogType DialogType, message, defaultPrompt string) *Dialog {
	return &Dialog{
		page:          page,
		frameID:       frameID,
		url:           url,
		dialogType:    dialogType,
		message:       message,
		defaultPrompt: defaultPrompt,
	}
}

func (d *Dialog) Type() DialogType {
	return d.dialogType
}

func (d *Dialog) Message() string {
	return d.message
}

func (d *Dialog) URL() string {
	return d.url
}

func (d *Dialog) FrameID() string {
	return d.frameID
}

func (d *Dialog) DefaultPrompt() string {
	return d.defaultPrompt
}

func (d *Dialog) Accept(ctx context.Context, promptText ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.handled {
		return ErrDialogAlreadyHandled
	}
	d.handled = true

	text := ""
	if len(promptText) > 0 {
		text = promptText[0]
	} else if d.dialogType == DialogTypePrompt {
		text = d.defaultPrompt
	}

	return d.page.handleDialog(ctx, true, text)
}

func (d *Dialog) Dismiss(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.handled {
		return ErrDialogAlreadyHandled
	}
	d.handled = true

	return d.page.handleDialog(ctx, false, "")
}
