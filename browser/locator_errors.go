package browser

import (
	"errors"
	"fmt"
)

var (
	ErrElementNotFound    = errors.New("element not found")
	ErrElementDetached    = errors.New("element detached from DOM during action")
	ErrElementNotVisible  = errors.New("element is not visible")
	ErrElementNotEnabled  = errors.New("element is not enabled")
	ErrElementNotEditable = errors.New("element is not editable")
	ErrNotInputElement    = errors.New("element is not an input")
	ErrNotSelectElement   = errors.New("element is not a select")
	ErrNotCheckable       = errors.New("element is not a checkbox or radio input")
)

type LocatorError struct {
	Selector string
	Err      error
	Index    int
	FrameID  string
}

func (e *LocatorError) Error() string {
	if e.Selector == "" {
		return e.Err.Error()
	}
	if e.Index >= 0 {
		return fmt.Sprintf("locator %q (index %d): %v", e.Selector, e.Index, e.Err)
	}
	return fmt.Sprintf("locator %q: %v", e.Selector, e.Err)
}

func (e *LocatorError) Unwrap() error {
	return e.Err
}

func newLocatorError(selector string, index int, frameID string, err error) error {
	return &LocatorError{Selector: selector, Err: err, Index: index, FrameID: frameID}
}

func notFoundError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrElementNotFound)
}

func detachedError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrElementDetached)
}

func notVisibleError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrElementNotVisible)
}

func notEnabledError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrElementNotEnabled)
}

func notEditableError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrElementNotEditable)
}

func notInputError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrNotInputElement)
}

func notSelectError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrNotSelectElement)
}

func notCheckableError(selector string, index int, frameID string) error {
	return newLocatorError(selector, index, frameID, ErrNotCheckable)
}

type SelectorError struct {
	Selector string
	Err      error
}

func (e *SelectorError) Error() string {
	return fmt.Sprintf("selector %q: %v", e.Selector, e.Err)
}

func (e *SelectorError) Unwrap() error {
	return e.Err
}

func selectorError(selector string, err error) error {
	if err == nil {
		return nil
	}
	return &SelectorError{Selector: selector, Err: err}
}
