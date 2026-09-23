package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// slashRouting records how one submitted slash line was routed.
type slashRouting struct {
	prompts  []string
	commands []string
	overlays []string
	rendered string
}

func routeSlashLine(t *testing.T, value string) slashRouting {
	t.Helper()
	routing := slashRouting{}
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend:  backend,
		Provider: "fake",
		Model:    "test",
		OverlayCommand: func(name, args string) bool {
			routing.overlays = append(routing.overlays, name+"|"+args)
			return true
		},
		Command: func(_ context.Context, name, args string) (string, error) {
			routing.commands = append(routing.commands, name+"|"+args)
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.submit(value)
	routing.prompts, _ = backend.snapshot()
	routing.rendered = strings.Join(plainLines(chat.Render(60)), "\n")
	chat.Close()
	return routing
}

func TestSlashTextWithoutAMatchingCommandIsSentAsAPrompt(t *testing.T) {
	for _, value := range []string{
		"/commnad explain the parser", // typo, no such command
		"/tasks some random text",     // arguments for a command taking none
		"/model extra",                // arguments for a command taking none
		"/sessions last week please",  // arguments for a command taking none
		"/tools list everything",      // unknown command with arguments
		"/notacommand",                // unknown bare command
	} {
		routing := routeSlashLine(t, value)
		if !slices.Equal(routing.prompts, []string{value}) {
			t.Fatalf("%q prompts = %#v", value, routing.prompts)
		}
		if len(routing.commands) != 0 || len(routing.overlays) != 0 {
			t.Fatalf("%q ran a command: %#v %#v", value, routing.commands, routing.overlays)
		}
		if !strings.Contains(routing.rendered, value) {
			t.Fatalf("%q missing from the transcript:\n%s", value, routing.rendered)
		}
	}
}

func TestSlashCommandCallsKeepRoutingToTheirHandler(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"/title Rename this session", "title|Rename this session"},
		{"/voice on", "voice|on"},
		{"/goal edit ship the change", "goal|edit ship the change"},
		{"/mcp status", "mcp|status"}, // the legacy alias keeps its spelling
	}
	for _, test := range tests {
		routing := routeSlashLine(t, test.value)
		got := append(append([]string(nil), routing.overlays...), routing.commands...)
		if len(got) != 1 || got[0] != test.want {
			t.Fatalf("%q routed to %#v, want %q", test.value, got, test.want)
		}
		if len(routing.prompts) != 0 {
			t.Fatalf("%q was also sent as a prompt: %#v", test.value, routing.prompts)
		}
	}
}

func TestSlashMenuSelectionStillRunsTheTopMatch(t *testing.T) {
	commands := make([]string, 0, 1)
	backend := &fakeChatBackend{}
	chat, err := NewChat(ChatOptions{
		Backend: backend, Provider: "fake", Model: "test",
		OverlayCommand: func(name, args string) bool { commands = append(commands, name+"|"+args); return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer chat.Close()
	chat.editor.SetText("/stat")
	if !chat.editor.SlashMenuVisible() {
		t.Fatal("slash menu was not offered for a partial name")
	}
	chat.editor.HandleInput("\r")
	if !slices.Equal(commands, []string{"stats|"}) {
		t.Fatalf("menu selection routed to %#v", commands)
	}
	if prompts, _ := backend.snapshot(); len(prompts) != 0 {
		t.Fatalf("menu selection was sent as a prompt: %#v", prompts)
	}
}

func TestRemovedMultitaskCommandsAreNotCommandsAnyMore(t *testing.T) {
	for _, value := range []string{"/multitask", "/multitask on", "/tasks"} {
		if isSlashCommandCall(value) {
			t.Fatalf("%q still routes as a command", value)
		}
	}
}
