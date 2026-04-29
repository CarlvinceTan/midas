package cache

import (
	"testing"
)

func TestParseSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		isXPath  bool
		expected string
	}{
		{
			name:     "xpath with prefix",
			selector: "xpath=//div[@id='test']",
			isXPath:  true,
			expected: "//div[@id='test']",
		},
		{
			name:     "xpath without prefix",
			selector: "//div[@id='test']",
			isXPath:  true,
			expected: "//div[@id='test']",
		},
		{
			name:     "css selector",
			selector: "#test .class",
			isXPath:  false,
			expected: "#test .class",
		},
		{
			name:     "css with brackets",
			selector: "input[type='text']",
			isXPath:  false,
			expected: "input[type='text']",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseSelector(tt.selector)
			if info == nil {
				t.Fatalf("parseSelector returned nil")
			}
			if info.isXPath != tt.isXPath {
				t.Errorf("expected isXPath=%v, got %v", tt.isXPath, info.isXPath)
			}
			if info.isXPath && info.xpath != tt.expected {
				t.Errorf("expected xpath=%s, got %s", tt.expected, info.xpath)
			}
			if !info.isXPath && info.css != tt.expected {
				t.Errorf("expected css=%s, got %s", tt.expected, info.css)
			}
		})
	}
}

func TestExtractSignatureFromXPath(t *testing.T) {
	tests := []struct {
		name                string
		xpath               string
		expectedTag         string
		expectedID          string
		expectedName        string
		expectedType        string
		expectedValue       string
		expectedPlaceholder string
	}{
		{
			name:        "simple id selector",
			xpath:       "//*[@id='submit-button']",
			expectedTag: "*",
			expectedID:  "submit-button",
		},
		{
			name:         "input with type",
			xpath:        "//input[@type='text']",
			expectedTag:  "input",
			expectedType: "text",
		},
		{
			name:         "input with name",
			xpath:        "//input[@name='email']",
			expectedTag:  "input",
			expectedName: "email",
		},
		{
			name:        "button with id",
			xpath:       "//button[@id='login']",
			expectedTag: "button",
			expectedID:  "login",
		},
		{
			name:                "input with placeholder",
			xpath:               "//input[@placeholder='Enter email']",
			expectedTag:         "input",
			expectedPlaceholder: "Enter email",
		},
		{
			name:          "input with value",
			xpath:         "//input[@value='Submit']",
			expectedTag:   "input",
			expectedValue: "Submit",
		},
		{
			name:        "element with data attribute",
			xpath:       "//div[@data-testid='form-container']",
			expectedTag: "div",
		},
		{
			name:         "multiple attributes",
			xpath:        "//input[@id='email'][@type='email'][@name='user-email']",
			expectedTag:  "input",
			expectedID:   "email",
			expectedType: "email",
			expectedName: "user-email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := extractSignatureFromXPath(tt.xpath)
			if sig == nil {
				t.Fatalf("extractSignatureFromXPath returned nil")
			}
			if sig.tag != tt.expectedTag {
				t.Errorf("expected tag=%s, got %s", tt.expectedTag, sig.tag)
			}
			if sig.id != tt.expectedID {
				t.Errorf("expected id=%s, got %s", tt.expectedID, sig.id)
			}
			if sig.nameAttr != tt.expectedName {
				t.Errorf("expected name=%s, got %s", tt.expectedName, sig.nameAttr)
			}
			if sig.typeAttr != tt.expectedType {
				t.Errorf("expected type=%s, got %s", tt.expectedType, sig.typeAttr)
			}
			if sig.value != tt.expectedValue {
				t.Errorf("expected value=%s, got %s", tt.expectedValue, sig.value)
			}
			if sig.placeholder != tt.expectedPlaceholder {
				t.Errorf("expected placeholder=%s, got %s", tt.expectedPlaceholder, sig.placeholder)
			}
		})
	}
}

func TestParseXPathSegments(t *testing.T) {
	tests := []struct {
		name          string
		xpath         string
		expectedCount int
		expectedTags  []string
	}{
		{
			name:          "simple path",
			xpath:         "/html/body/div",
			expectedCount: 3,
			expectedTags:  []string{"html", "body", "div"},
		},
		{
			name:          "path with id",
			xpath:         "//div[@id='container']",
			expectedCount: 1,
			expectedTags:  []string{"div"},
		},
		{
			name:          "path with index",
			xpath:         "//div[2]/span",
			expectedCount: 2,
			expectedTags:  []string{"div", "span"},
		},
		{
			name:          "complex path",
			xpath:         "//form[@id='login']/div[1]//input[@type='text']",
			expectedCount: 3,
			expectedTags:  []string{"form", "div", "input"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := parseXPathSegments(tt.xpath)
			if len(segments) != tt.expectedCount {
				t.Errorf("expected %d segments, got %d", tt.expectedCount, len(segments))
				return
			}
			for i, seg := range segments {
				if i < len(tt.expectedTags) && seg.tag != tt.expectedTags[i] {
					t.Errorf("segment %d: expected tag=%s, got %s", i, tt.expectedTags[i], seg.tag)
				}
			}
		})
	}
}

func TestEscapeXPathString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple string",
			input:    "hello",
			expected: `"hello"`,
		},
		{
			name:     "string with double quotes",
			input:    `hello "world"`,
			expected: `'hello "world"'`,
		},
		{
			name:     "string with single quotes",
			input:    "hello 'world'",
			expected: `"hello 'world'"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeXPathString(tt.input)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestTagToRole(t *testing.T) {
	tests := []struct {
		tag      string
		expected string
	}{
		{"button", "button"},
		{"a", "link"},
		{"input", "textbox"},
		{"textarea", "textbox"},
		{"select", "combobox"},
		{"img", "img"},
		{"h1", "heading"},
		{"h2", "heading"},
		{"ul", "list"},
		{"li", "listitem"},
		{"nav", "navigation"},
		{"form", "form"},
		{"table", "table"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := tagToRole(tt.tag)
			if result != tt.expected {
				t.Errorf("tagToRole(%s) = %s, expected %s", tt.tag, result, tt.expected)
			}
		})
	}
}

func TestRoleToTag(t *testing.T) {
	tests := []struct {
		role     string
		expected string
	}{
		{"button", "button"},
		{"link", "a"},
		{"textbox", "input"},
		{"combobox", "select"},
		{"img", "img"},
		{"heading", "h1 | h2 | h3 | h4 | h5 | h6"},
		{"checkbox", "input[@type='checkbox']"},
		{"radio", "input[@type='radio']"},
		{"unknown", "*"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			result := roleToTag(tt.role)
			if result != tt.expected {
				t.Errorf("roleToTag(%s) = %s, expected %s", tt.role, result, tt.expected)
			}
		})
	}
}

func TestSelfHealConfig(t *testing.T) {
	t.Run("default timeout", func(t *testing.T) {
		cfg := SelfHealConfig{Enabled: true}
		if cfg.Timeout <= 0 {
			cfg.Timeout = 15e9 // 15 seconds in nanoseconds
		}
		// This test just verifies the config can be created
	})

	t.Run("with custom logger", func(t *testing.T) {
		logger := defaultLogger{}
		cfg := SelfHealConfig{
			Enabled: true,
			Logger:  logger,
		}
		if cfg.Logger == nil {
			t.Error("expected logger to be set")
		}
	})
}

func TestHealLogger(t *testing.T) {
	t.Run("nopLogger", func(t *testing.T) {
		logger := nopLogger{}
		logger.Debug("test message", "key", "value")
		logger.Warn("test warning", "key", "value")
	})

	t.Run("defaultLogger", func(t *testing.T) {
		logger := defaultLogger{}
		logger.Debug("test message", "key", "value")
		logger.Warn("test warning", "key", "value")
	})
}

func TestIsSelectorAction(t *testing.T) {
	replayer := &Replayer{}

	tests := []struct {
		actionType ActionType
		selector   string
		expected   bool
	}{
		{ActionTypeClick, "#button", true},
		{ActionTypeClick, "", false},
		{ActionTypeType, "#input", true},
		{ActionTypeGoto, "", false},
		{ActionTypeScroll, "#container", true},
		{ActionTypeWait, "#element", true},
		{ActionTypePress, "", false},
		{ActionTypeFillForm, "#field", true},
	}

	for _, tt := range tests {
		t.Run(string(tt.actionType), func(t *testing.T) {
			action := Action{Type: tt.actionType, Selector: tt.selector}
			result := replayer.isSelectorAction(action)
			if result != tt.expected {
				t.Errorf("isSelectorAction(%+v) = %v, expected %v", action, result, tt.expected)
			}
		})
	}
}
