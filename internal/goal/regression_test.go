package goal

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// TestIntegerArgumentRejectsOutOfRangeNumbers covers the conversion boundary: a
// float64 outside the int64 range converts to a different value on each
// architecture, so it must be refused before the conversion.
func TestIntegerArgumentRejectsOutOfRangeNumbers(t *testing.T) {
	for _, value := range []float64{math.MaxInt64, 1 << 63, 1e300, math.Inf(1)} {
		if _, err := integerArgument(map[string]any{"tokenBudget": value}, "tokenBudget"); err == nil {
			t.Fatalf("integerArgument(%v) was accepted", value)
		}
	}
	if value, err := integerArgument(map[string]any{"tokenBudget": float64(1500)}, "tokenBudget"); err != nil || value != 1500 {
		t.Fatalf("integerArgument(1500) = %d, %v", value, err)
	}
	if value, err := integerArgument(map[string]any{}, "tokenBudget"); err != nil || value != 0 {
		t.Fatalf("a missing argument = %d, %v", value, err)
	}
}

// TestCreateRejectsOutOfRangeBudget keeps the tool surface safe too.
// findGoalTool picks one tool by name.
func findGoalTool(t *testing.T, tools []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Definition().Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool %q", name)
	return nil
}

// callWith builds a tool call with the given arguments.
func callWith(arguments map[string]any) ai.ToolCall {
	return ai.NewToolCall("call", "goal", arguments)
}

func osStat(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func TestCreateRejectsOutOfRangeBudget(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	create := findGoalTool(t, Tools(store), "create_goal")
	_, err := create.Execute(context.Background(), callWith(map[string]any{
		"objective": "ship it", "tokenBudget": float64(1 << 63),
	}), nil)
	if err == nil {
		t.Fatal("an out-of-range budget was accepted")
	}
}

// TestHistoryIsBounded: every turn appends an event, so the log stored with a goal
// has to stay bounded over a long goal.
func TestHistoryIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goals.json")
	store := NewStore(path)
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.Create(context.Background(), "long running", Limits{}); err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < maxGoalHistory+50; turn++ {
		now = now.Add(time.Second)
		if _, _, err := store.RecordTurn(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	goal, err := store.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(goal.History) > maxGoalHistory {
		t.Fatalf("history = %d events", len(goal.History))
	}
	if goal.TurnsUsed != int64(maxGoalHistory+50) {
		t.Fatalf("turns used = %d", goal.TurnsUsed)
	}
	// The file itself stays small as well.
	if info, err := osStat(path); err == nil && info > 1<<20 {
		t.Fatalf("goal state file = %d bytes", info)
	}
}

// TestDetailMustBeAString: a non-string detail used to be dropped silently.
func TestDetailMustBeAString(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	if _, err := store.Create(context.Background(), "objective", Limits{}); err != nil {
		t.Fatal(err)
	}
	update := findGoalTool(t, Tools(store), "update_goal")
	_, err := update.Execute(context.Background(), callWith(map[string]any{
		"status": "complete", "detail": float64(3),
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "detail must be a string") {
		t.Fatalf("numeric detail = %v", err)
	}
}

// TestClearGoalReportsSuccessOnlyWhenItWorked: clear_goal answered
// {"cleared": true} even when the clear failed.
func TestClearGoalReportsSuccessOnlyWhenItWorked(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	if _, err := store.Create(context.Background(), "objective", Limits{}); err != nil {
		t.Fatal(err)
	}
	clear := findGoalTool(t, Tools(store), "clear_goal")
	result, err := clear.Execute(context.Background(), callWith(map[string]any{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].(ai.TextContent).Text, `"cleared":true`) {
		t.Fatalf("clear result = %#v", result)
	}
	if _, err := store.Get(context.Background()); !errors.Is(err, ErrNoGoal) {
		t.Fatalf("goal survived a clear: %v", err)
	}
}
