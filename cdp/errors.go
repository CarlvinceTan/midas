package cdp

import "fmt"

var ErrConnectionClosed = fmt.Errorf("cdp connection closed")

type ConnectionClosedError struct {
	Why string
}

func (e *ConnectionClosedError) Error() string {
	if e == nil || e.Why == "" {
		return ErrConnectionClosed.Error()
	}
	return fmt.Sprintf("%s: %s", ErrConnectionClosed.Error(), e.Why)
}

func (e *ConnectionClosedError) Unwrap() error {
	return ErrConnectionClosed
}

type SessionDetachedError struct {
	SessionID string
	TargetID  string
}

func (e *SessionDetachedError) Error() string {
	if e == nil {
		return "cdp session detached"
	}
	if e.TargetID == "" {
		return fmt.Sprintf("cdp session detached: session=%s", e.SessionID)
	}
	return fmt.Sprintf("cdp session detached: session=%s target=%s", e.SessionID, e.TargetID)
}
