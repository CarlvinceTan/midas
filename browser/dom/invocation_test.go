package dom

import (
	"strings"
	"testing"
)

func TestBootstrapScriptIdempotent(t *testing.T) {
	script := BootstrapScript()
	if script == "" {
		t.Fatal("BootstrapScript() returned empty string")
	}
	if !strings.Contains(script, "__polymuxSelectorHelper") {
		t.Error("BootstrapScript missing __polymuxSelectorHelper global")
	}
	if !strings.Contains(script, "if (globalThis.__polymuxSelectorHelper)") {
		t.Error("BootstrapScript missing idempotency check")
	}
}

func TestGetFunctionSource(t *testing.T) {
	tests := []struct {
		name     HelperName
		contains string
	}{
		{HelperFillElementValue, "this.focus()"},
		{HelperSetChecked, "this.click()"},
		{HelperSelectOptions, "this.options"},
		{HelperScrollToPercent, "scrollTop"},
		{HelperFocusElement, "this.focus()"},
		{HelperGetInputValue, "this.value"},
		{HelperGetTextContent, "textContent"},
		{HelperGetInnerHTML, "innerHTML"},
		{HelperGetInnerText, "innerText"},
		{HelperIsChecked, "this.checked"},
	}

	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			source := GetFunctionSource(tt.name)
			if source == "" {
				t.Fatalf("GetFunctionSource(%s) returned empty string", tt.name)
			}
			if !strings.Contains(source, tt.contains) {
				t.Errorf("GetFunctionSource(%s) missing expected content: %s", tt.name, tt.contains)
			}
		})
	}
}

func TestGetFunctionSourceUnknown(t *testing.T) {
	source := GetFunctionSource("unknown")
	if source != "" {
		t.Error("GetFunctionSource for unknown helper should return empty string")
	}
}

func TestBuildContextInvocation(t *testing.T) {
	tests := []struct {
		name     string
		helper   HelperName
		args     []any
		contains string
	}{
		{
			name:     "resolve with args",
			helper:   HelperResolve,
			args:     []any{"div.selector", true, 0},
			contains: "resolve",
		},
		{
			name:     "count with args",
			helper:   HelperCount,
			args:     []any{".item", false},
			contains: "count",
		},
		{
			name:     "actionability with args",
			helper:   HelperActionability,
			args:     []any{"button", true, 1},
			contains: "actionability",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invocation := BuildContextInvocation(tt.helper, tt.args...)
			if invocation == "" {
				t.Fatal("BuildContextInvocation returned empty string")
			}
			if !strings.Contains(invocation, string(tt.helper)) {
				t.Errorf("Invocation missing helper name: %s", tt.helper)
			}
			if !strings.Contains(invocation, "__polymuxSelectorHelper") {
				t.Error("Invocation missing __polymuxSelectorHelper")
			}
		})
	}
}

func TestBuildInvocation(t *testing.T) {
	invocation := BuildInvocation(HelperResolve, `"selector"`, "true", "0")
	if !strings.Contains(invocation, "__polymuxSelectorHelper") {
		t.Error("BuildInvocation missing __polymuxSelectorHelper")
	}
	if !strings.Contains(invocation, "resolve") {
		t.Error("BuildInvocation missing helper name")
	}
}

func TestBuildElementInvocation(t *testing.T) {
	invocation := BuildElementInvocation(HelperFillElementValue, `"test value"`)
	if !strings.Contains(invocation, ".call(this") {
		t.Error("BuildElementInvocation missing .call(this)")
	}
	if !strings.Contains(invocation, "fillElementValue") {
		t.Error("BuildElementInvocation missing helper name")
	}
}
