package browser

import (
	"context"
	"testing"

	"github.com/carlvincetan/polymux/internal/midas/cdp"
)

func TestDialogType(t *testing.T) {
	tests := []struct {
		name     string
		typ      DialogType
		expected string
	}{
		{"alert", DialogTypeAlert, "alert"},
		{"confirm", DialogTypeConfirm, "confirm"},
		{"prompt", DialogTypePrompt, "prompt"},
		{"beforeunload", DialogTypeBeforeunload, "beforeunload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.typ) != tt.expected {
				t.Errorf("DialogType.%s = %q, want %q", tt.name, tt.typ, tt.expected)
			}
		})
	}
}

func TestDialogProperties(t *testing.T) {
	page := &Page{targetID: "test-target"}
	dialog := newDialog(
		page,
		"frame-123",
		"https://example.com/page",
		DialogTypePrompt,
		"Enter your name:",
		"John Doe",
	)

	if dialog.Type() != DialogTypePrompt {
		t.Errorf("Type() = %q, want %q", dialog.Type(), DialogTypePrompt)
	}
	if dialog.Message() != "Enter your name:" {
		t.Errorf("Message() = %q, want %q", dialog.Message(), "Enter your name:")
	}
	if dialog.URL() != "https://example.com/page" {
		t.Errorf("URL() = %q, want %q", dialog.URL(), "https://example.com/page")
	}
	if dialog.FrameID() != "frame-123" {
		t.Errorf("FrameID() = %q, want %q", dialog.FrameID(), "frame-123")
	}
	if dialog.DefaultPrompt() != "John Doe" {
		t.Errorf("DefaultPrompt() = %q, want %q", dialog.DefaultPrompt(), "John Doe")
	}
}

func TestDialogAlreadyHandled(t *testing.T) {
	page := &Page{targetID: "test-target"}
	dialog := newDialog(page, "frame-1", "about:blank", DialogTypeAlert, "Hello", "")

	ctx := context.Background()

	mockSession := &mockSessionForDialog{
		calls: make([]mockDialogCall, 0),
	}
	page.mainSession = mockSession

	if err := dialog.Accept(ctx); err != nil {
		t.Errorf("first Accept() should succeed, got error: %v", err)
	}

	if len(mockSession.calls) != 1 {
		t.Errorf("expected 1 handleDialog call, got %d", len(mockSession.calls))
	}
	if !mockSession.calls[0].accept {
		t.Error("expected accept=true")
	}

	if err := dialog.Dismiss(ctx); err != ErrDialogAlreadyHandled {
		t.Errorf("second call should return ErrDialogAlreadyHandled, got: %v", err)
	}
}

func TestDialogAcceptWithPromptText(t *testing.T) {
	page := &Page{targetID: "test-target"}
	dialog := newDialog(page, "frame-1", "about:blank", DialogTypePrompt, "Name?", "default")

	ctx := context.Background()

	mockSession := &mockSessionForDialog{
		calls: make([]mockDialogCall, 0),
	}
	page.mainSession = mockSession

	if err := dialog.Accept(ctx, "custom input"); err != nil {
		t.Errorf("Accept() should succeed, got error: %v", err)
	}

	if len(mockSession.calls) != 1 {
		t.Errorf("expected 1 handleDialog call, got %d", len(mockSession.calls))
	}
	if mockSession.calls[0].promptText != "custom input" {
		t.Errorf("expected promptText='custom input', got %q", mockSession.calls[0].promptText)
	}
}

func TestDialogAcceptUsesDefaultPrompt(t *testing.T) {
	page := &Page{targetID: "test-target"}
	dialog := newDialog(page, "frame-1", "about:blank", DialogTypePrompt, "Name?", "default value")

	ctx := context.Background()

	mockSession := &mockSessionForDialog{
		calls: make([]mockDialogCall, 0),
	}
	page.mainSession = mockSession

	if err := dialog.Accept(ctx); err != nil {
		t.Errorf("Accept() should succeed, got error: %v", err)
	}

	if mockSession.calls[0].promptText != "default value" {
		t.Errorf("expected promptText='default value', got %q", mockSession.calls[0].promptText)
	}
}

func TestDialogDismiss(t *testing.T) {
	page := &Page{targetID: "test-target"}
	dialog := newDialog(page, "frame-1", "about:blank", DialogTypeConfirm, "Continue?", "")

	ctx := context.Background()

	mockSession := &mockSessionForDialog{
		calls: make([]mockDialogCall, 0),
	}
	page.mainSession = mockSession

	if err := dialog.Dismiss(ctx); err != nil {
		t.Errorf("Dismiss() should succeed, got error: %v", err)
	}

	if len(mockSession.calls) != 1 {
		t.Errorf("expected 1 handleDialog call, got %d", len(mockSession.calls))
	}
	if mockSession.calls[0].accept {
		t.Error("expected accept=false for dismiss")
	}
	if mockSession.calls[0].promptText != "" {
		t.Errorf("expected promptText='' for dismiss, got %q", mockSession.calls[0].promptText)
	}
}

type mockDialogCall struct {
	accept     bool
	promptText string
}

type mockSessionForDialog struct {
	calls []mockDialogCall
}

func (m *mockSessionForDialog) ID() string                      { return "mock-session" }
func (m *mockSessionForDialog) Close(ctx context.Context) error { return nil }
func (m *mockSessionForDialog) On(event string, handler cdp.EventHandler) cdp.Unsubscribe {
	return func() {}
}
func (m *mockSessionForDialog) Send(ctx context.Context, method string, params any, result any) error {
	if method == "Page.handleJavaScriptDialog" {
		if p, ok := params.(map[string]any); ok {
			call := mockDialogCall{}
			if v, ok := p["accept"].(bool); ok {
				call.accept = v
			}
			if v, ok := p["promptText"].(string); ok {
				call.promptText = v
			}
			m.calls = append(m.calls, call)
		}
	}
	return nil
}
