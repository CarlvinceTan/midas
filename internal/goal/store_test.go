package goal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

func TestCreateReuseConflictAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goals.json")
	store := NewStore(path)
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	store.now = func() time.Time { return now }
	created, err := store.Create(context.Background(), "  ship native Midas  ", Limits{TokenBudget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if created.Reused || created.Goal.Objective != "ship native Midas" || created.Goal.Status != StatusActive {
		t.Fatalf("created = %#v", created)
	}
	reused, err := store.Create(context.Background(), "ship native Midas", Limits{})
	if err != nil || !reused.Reused || reused.Goal.ID != created.Goal.ID {
		t.Fatalf("reused = %#v, %v", reused, err)
	}
	conflict, err := store.Create(context.Background(), "different", Limits{})
	if !errors.Is(err, ErrConflict) || conflict.Goal.ID != created.Goal.ID {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	reloaded, err := NewStore(path).Get(context.Background())
	if err != nil || reloaded.ID != created.Goal.ID || reloaded.TokenBudget != 100 {
		t.Fatalf("reloaded = %#v, %v", reloaded, err)
	}
}

func TestAgentToolsSharePersistentLifecycle(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	available := Tools(store)
	if len(available) != 4 {
		t.Fatalf("tools = %d", len(available))
	}
	byName := make(map[string]int)
	for index, tool := range available {
		byName[tool.Definition().Name] = index
		var schema any
		if err := json.Unmarshal(tool.Definition().Parameters, &schema); err != nil {
			t.Fatalf("%s schema: %v", tool.Definition().Name, err)
		}
	}
	create, err := available[byName["create_goal"]].Execute(context.Background(), ai.NewToolCall("1", "create_goal", map[string]any{
		"objective": "native goal", "tokenBudget": float64(20),
	}), nil)
	if err != nil || !strings.Contains(create.Content[0].(ai.TextContent).Text, "native goal") {
		t.Fatalf("create = %#v, %v", create, err)
	}
	update, err := available[byName["update_goal"]].Execute(context.Background(), ai.NewToolCall("2", "update_goal", map[string]any{
		"objective": "updated goal", "status": "paused",
	}), nil)
	if err != nil || !strings.Contains(update.Content[0].(ai.TextContent).Text, "updated goal") {
		t.Fatalf("update = %#v, %v", update, err)
	}
	current, err := store.Get(context.Background())
	if err != nil || current.Objective != "updated goal" || current.Status != StatusPaused {
		t.Fatalf("current = %#v, %v", current, err)
	}
	if _, err := available[byName["clear_goal"]].Execute(context.Background(), ai.NewToolCall("3", "clear_goal", nil), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background()); !errors.Is(err, ErrNoGoal) {
		t.Fatalf("after clear = %v", err)
	}
}

func TestLifecycleEvidenceBlockerAndClosedRule(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	now := time.Unix(100, 0).UTC()
	store.now = func() time.Time { return now }
	if _, err := store.Create(context.Background(), "objective", Limits{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	paused, err := store.SetStatus(context.Background(), StatusPaused, "")
	if err != nil || paused.ActiveDuration(time.Unix(999, 0)) != 3*time.Second {
		t.Fatalf("paused = %#v, %v", paused, err)
	}
	now = now.Add(time.Hour)
	active, err := store.SetStatus(context.Background(), StatusActive, "")
	if err != nil || active.ActiveDuration(now) != 3*time.Second {
		t.Fatalf("active = %#v, %v", active, err)
	}
	if _, err := store.SetStatus(context.Background(), StatusComplete, ""); err == nil {
		t.Fatal("completion without evidence succeeded")
	}
	now = now.Add(2 * time.Second)
	complete, err := store.SetStatus(context.Background(), StatusComplete, "tests pass")
	if err != nil || complete.Evidence != "tests pass" || complete.ActiveDuration(now) != 5*time.Second {
		t.Fatalf("complete = %#v, %v", complete, err)
	}
	if _, err := store.SetStatus(context.Background(), StatusActive, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("resume error = %v", err)
	}
	if _, err := store.UpdateObjective(context.Background(), "late edit"); !errors.Is(err, ErrClosed) {
		t.Fatalf("edit error = %v", err)
	}
}

func TestBlockedRequiresReason(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	if _, err := store.Create(context.Background(), "objective", Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStatus(context.Background(), StatusBlocked, "  "); err == nil {
		t.Fatal("blocked without reason succeeded")
	}
	blocked, err := store.SetStatus(context.Background(), StatusBlocked, "needs user input")
	if err != nil || blocked.Blocker != "needs user input" {
		t.Fatalf("blocked = %#v, %v", blocked, err)
	}
}

func TestUsageLimitsAndPausedDuration(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	now := time.Unix(1_000, 0).UTC()
	store.now = func() time.Time { return now }
	if _, err := store.Create(context.Background(), "objective", Limits{TokenBudget: 10, MaxTurns: 3, MaxDuration: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	goal, reason, err := store.RecordTurn(context.Background(), 4)
	if err != nil || reason != LimitNone || goal.TurnsUsed != 1 {
		t.Fatalf("turn 1 = %#v, %q, %v", goal, reason, err)
	}
	goal, reason, err = store.RecordTurn(context.Background(), 6)
	if err != nil || reason != LimitTokens || goal.TokensUsed != 10 {
		t.Fatalf("turn 2 = %#v, %q, %v", goal, reason, err)
	}
	if err := store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background()); !errors.Is(err, ErrNoGoal) {
		t.Fatalf("get after clear = %v", err)
	}
}

func TestCancellationAndValidationDoNotWrite(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "goals.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Create(ctx, "objective", Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create = %v", err)
	}
	if _, err := store.Create(context.Background(), "", Limits{}); err == nil {
		t.Fatal("empty objective succeeded")
	}
	if _, err := store.Create(context.Background(), "objective", Limits{MaxTurns: -1}); err == nil {
		t.Fatal("negative limit succeeded")
	}
}
