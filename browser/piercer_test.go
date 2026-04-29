package browser

import (
	"context"
	"testing"
)

func TestInstallPiercerSendsCorrectCommands(t *testing.T) {
	session := newFakeSession("test-session")

	var calls []string
	session.respond("Page.enable", func(_ any, result any) error {
		calls = append(calls, "Page.enable")
		return nil
	})
	session.respond("Runtime.enable", func(_ any, result any) error {
		calls = append(calls, "Runtime.enable")
		return nil
	})
	session.respond("Page.addScriptToEvaluateOnNewDocument", func(params any, result any) error {
		calls = append(calls, "Page.addScriptToEvaluateOnNewDocument")
		p := params.(map[string]any)
		if p["source"] == nil {
			t.Error("expected source parameter")
		}
		if p["runImmediately"] != true {
			t.Error("expected runImmediately to be true")
		}
		return nil
	})
	session.respond("Runtime.evaluate", func(params any, result any) error {
		p := params.(map[string]any)
		expr, ok := p["expression"].(string)
		if !ok {
			return nil
		}
		if len(expr) > 50 && expr[:50] == v3PiercerScript[:50] {
			calls = append(calls, "Runtime.evaluate(piercer)")
		} else if len(expr) > 20 && expr[:20] == rerenderMissingShadowsScript[:20] {
			calls = append(calls, "Runtime.evaluate(rerender)")
		}
		return nil
	})

	err := InstallPiercer(context.Background(), session)
	if err != nil {
		t.Fatalf("InstallPiercer returned error: %v", err)
	}
	methods := session.methods()

	var foundPageEnable, foundRuntimeEnable, foundAddScript, foundPiercerEval bool
	for _, m := range methods {
		switch m {
		case "Page.enable":
			foundPageEnable = true
		case "Runtime.enable":
			foundRuntimeEnable = true
		case "Page.addScriptToEvaluateOnNewDocument":
			foundAddScript = true
		case "Runtime.evaluate":
		}
	}
	for _, c := range calls {
		if c == "Runtime.evaluate(piercer)" {
			foundPiercerEval = true
		}
	}

	if !foundPageEnable {
		t.Error("expected Page.enable to be called")
	}
	if !foundRuntimeEnable {
		t.Error("expected Runtime.enable to be called")
	}
	if !foundAddScript {
		t.Error("expected Page.addScriptToEvaluateOnNewDocument to be called")
	}
	if !foundPiercerEval {
		t.Error("expected Runtime.evaluate with piercer script to be called")
	}
}

func TestInstallPiercerIdempotent(t *testing.T) {
	session := newFakeSession("test-session")

	callCount := 0
	session.respond("Page.addScriptToEvaluateOnNewDocument", func(_ any, result any) error {
		callCount++
		return nil
	})
	session.respond("Runtime.evaluate", func(_ any, result any) error {
		callCount++
		return nil
	})

	for i := 0; i < 3; i++ {
		err := InstallPiercer(context.Background(), session)
		if err != nil {
			t.Fatalf("InstallPiercer returned error: %v", err)
		}
	}

	if callCount > 2 {
		t.Errorf("expected piercer to be installed only once, but got %d calls", callCount)
	}
}

func TestGetPiercerStatsNotInstalled(t *testing.T) {
	session := newFakeSession("test-session")

	session.respond("Runtime.evaluate", func(params any, result any) error {
		res := result.(*struct {
			Result struct {
				Value *struct {
					Installed bool   `json:"installed"`
					URL       string `json:"url"`
					IsTop     bool   `json:"isTop"`
					Open      int    `json:"open"`
					Closed    int    `json:"closed"`
				} `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = nil
		return nil
	})

	stats, err := GetPiercerStats(context.Background(), session)
	if err != nil {
		t.Fatalf("GetPiercerStats returned error: %v", err)
	}
	if stats != nil {
		t.Error("expected nil stats when piercer not installed")
	}
}

func TestGetPiercerStatsInstalled(t *testing.T) {
	session := newFakeSession("test-session")

	session.respond("Runtime.evaluate", func(params any, result any) error {
		res := result.(*struct {
			Result struct {
				Value *struct {
					Installed bool   `json:"installed"`
					URL       string `json:"url"`
					IsTop     bool   `json:"isTop"`
					Open      int    `json:"open"`
					Closed    int    `json:"closed"`
				} `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		})
		res.Result.Value = &struct {
			Installed bool   `json:"installed"`
			URL       string `json:"url"`
			IsTop     bool   `json:"isTop"`
			Open      int    `json:"open"`
			Closed    int    `json:"closed"`
		}{
			Installed: true,
			URL:       "https://example.com",
			IsTop:     true,
			Open:      5,
			Closed:    3,
		}
		return nil
	})

	stats, err := GetPiercerStats(context.Background(), session)
	if err != nil {
		t.Fatalf("GetPiercerStats returned error: %v", err)
	}
	if stats == nil {
		t.Fatal("expected stats when piercer installed")
	}
	if !stats.Installed {
		t.Error("expected Installed to be true")
	}
	if stats.URL != "https://example.com" {
		t.Errorf("expected URL to be https://example.com, got %s", stats.URL)
	}
	if stats.Open != 5 {
		t.Errorf("expected Open to be 5, got %d", stats.Open)
	}
	if stats.Closed != 3 {
		t.Errorf("expected Closed to be 3, got %d", stats.Closed)
	}
}

func TestPiercerScriptContent(t *testing.T) {
	if len(v3PiercerScript) == 0 {
		t.Error("v3PiercerScript should not be empty")
	}
	if len(rerenderMissingShadowsScript) == 0 {
		t.Error("rerenderMissingShadowsScript should not be empty")
	}

	containsFunc := func(s, substr string) bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}

	if !containsFunc(v3PiercerScript, "attachShadow") {
		t.Error("v3PiercerScript should contain attachShadow")
	}
	if !containsFunc(v3PiercerScript, "__stagehandV3__") {
		t.Error("v3PiercerScript should contain __stagehandV3__")
	}
	if !containsFunc(v3PiercerScript, "getClosedRoot") {
		t.Error("v3PiercerScript should contain getClosedRoot")
	}

	if !containsFunc(rerenderMissingShadowsScript, "__stagehandV3__") {
		t.Error("rerenderMissingShadowsScript should contain __stagehandV3__")
	}
	if !containsFunc(rerenderMissingShadowsScript, "cloneNode") {
		t.Error("rerenderMissingShadowsScript should contain cloneNode for rerendering")
	}
}
