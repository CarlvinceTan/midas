package goal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store keeps the current goal in one JSON file. Its mutex serialises readers and
// writers inside one process; two processes sharing a goal file can still lose
// each other's update, because the read-modify-write is not locked across them.
type Store struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path, now: time.Now}
}

func (s *Store) Path() string { return s.path }

type CreateResult struct {
	Goal   Goal
	Reused bool
}

func (s *Store) Create(ctx context.Context, objective string, limits Limits) (CreateResult, error) {
	objective, err := normalizeObjective(objective)
	if err != nil {
		return CreateResult{}, err
	}
	if err := validateLimits(limits); err != nil {
		return CreateResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CreateResult{}, err
	}
	current, exists, err := s.read()
	if err != nil {
		return CreateResult{}, err
	}
	if exists && !current.Closed() {
		if current.Objective == objective {
			return CreateResult{Goal: current, Reused: true}, nil
		}
		return CreateResult{Goal: current}, ErrConflict
	}
	now := s.now().UTC()
	id, err := newID()
	if err != nil {
		return CreateResult{}, err
	}
	goal := Goal{
		ID: id, Objective: objective, Status: StatusActive,
		CreatedAt: now, UpdatedAt: now, ActiveSince: &now,
		TokenBudget: limits.TokenBudget, MaxTurns: limits.MaxTurns,
		MaxDurationMS: limits.MaxDuration.Milliseconds(),
		History:       []Event{{At: now, Kind: "created"}},
	}
	if err := s.write(goal); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Goal: goal}, nil
}

func (s *Store) Get(ctx context.Context) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Goal{}, err
	}
	goal, exists, err := s.read()
	if err != nil {
		return Goal{}, err
	}
	if !exists {
		return Goal{}, ErrNoGoal
	}
	return goal, nil
}

func (s *Store) UpdateObjective(ctx context.Context, objective string) (Goal, error) {
	return s.Update(ctx, Patch{Objective: &objective})
}

func (s *Store) SetStatus(ctx context.Context, status Status, detail string) (Goal, error) {
	return s.Update(ctx, Patch{Status: &status, Detail: detail})
}

type Patch struct {
	Objective *string
	Status    *Status
	Detail    string
}

// Update atomically changes the objective, status, or both.
func (s *Store) Update(ctx context.Context, patch Patch) (Goal, error) {
	if patch.Objective == nil && patch.Status == nil {
		return Goal{}, errors.New("goal: update requires an objective or status")
	}
	var objective string
	if patch.Objective != nil {
		var err error
		objective, err = normalizeObjective(*patch.Objective)
		if err != nil {
			return Goal{}, err
		}
	}
	detail := strings.TrimSpace(patch.Detail)
	if patch.Status != nil {
		switch *patch.Status {
		case StatusActive, StatusPaused:
		case StatusComplete:
			if detail == "" {
				return Goal{}, errors.New("goal: completion evidence is required")
			}
		case StatusBlocked:
			if detail == "" {
				return Goal{}, errors.New("goal: blocker is required")
			}
		default:
			return Goal{}, fmt.Errorf("goal: invalid status %q", *patch.Status)
		}
	}
	return s.mutate(ctx, func(goal *Goal, now time.Time) error {
		if goal.Closed() {
			return ErrClosed
		}
		if patch.Objective != nil {
			goal.Objective = objective
			goal.History = append(goal.History, Event{At: now, Kind: "objective_updated", Detail: objective})
		}
		if patch.Status == nil {
			return nil
		}
		status := *patch.Status
		switch status {
		case StatusActive:
			if goal.Status == StatusPaused {
				goal.ActiveSince = &now
			}
			goal.Blocker = ""
		case StatusPaused:
			freezeActiveDuration(goal, now)
		case StatusComplete:
			freezeActiveDuration(goal, now)
			goal.Evidence = detail
		case StatusBlocked:
			freezeActiveDuration(goal, now)
			goal.Blocker = detail
		}
		goal.Status = status
		goal.History = append(goal.History, Event{At: now, Kind: "status_" + string(status), Detail: detail})
		return nil
	})
}

func (s *Store) RecordTurn(ctx context.Context, tokens int64) (Goal, LimitReason, error) {
	if tokens < 0 {
		return Goal{}, LimitNone, errors.New("goal: token usage must not be negative")
	}
	goal, err := s.mutate(ctx, func(goal *Goal, now time.Time) error {
		if goal.Status != StatusActive {
			return fmt.Errorf("goal: cannot record a turn while %s", goal.Status)
		}
		goal.TurnsUsed++
		goal.TokensUsed += tokens
		goal.History = append(goal.History, Event{At: now, Kind: "turn", Tokens: tokens, Turns: 1})
		return nil
	})
	if err != nil {
		return Goal{}, LimitNone, err
	}
	return goal, goal.LimitReached(s.now().UTC()), nil
}

func (s *Store) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("goal: clear: %w", err)
	}
	return nil
}

func (s *Store) mutate(ctx context.Context, change func(*Goal, time.Time) error) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Goal{}, err
	}
	goal, exists, err := s.read()
	if err != nil {
		return Goal{}, err
	}
	if !exists {
		return Goal{}, ErrNoGoal
	}
	now := s.now().UTC()
	if err := change(&goal, now); err != nil {
		return Goal{}, err
	}
	goal.UpdatedAt = now
	trimHistory(&goal)
	if err := s.write(goal); err != nil {
		return Goal{}, err
	}
	return goal, nil
}

// maxGoalHistory bounds the event log stored with a goal. Every turn appends an
// event, so an unbounded log would grow the state file for the life of a goal.
const maxGoalHistory = 500

// trimHistory drops the oldest events once a goal's log is over its bound.
func trimHistory(goal *Goal) {
	if len(goal.History) > maxGoalHistory {
		goal.History = goal.History[len(goal.History)-maxGoalHistory:]
	}
}

func freezeActiveDuration(goal *Goal, now time.Time) {
	if goal.Status == StatusActive && goal.ActiveSince != nil {
		if now.After(*goal.ActiveSince) {
			goal.ActiveDurationMS += now.Sub(*goal.ActiveSince).Milliseconds()
		}
		goal.ActiveSince = nil
	}
}

func (s *Store) read() (Goal, bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Goal{}, false, nil
	}
	if err != nil {
		return Goal{}, false, fmt.Errorf("goal: read: %w", err)
	}
	var envelope struct {
		Version int  `json:"version"`
		Goal    Goal `json:"goal"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Goal{}, false, fmt.Errorf("goal: decode: %w", err)
	}
	if envelope.Version != 1 || envelope.Goal.ID == "" {
		return Goal{}, false, errors.New("goal: invalid state file")
	}
	return envelope.Goal, true, nil
}

func (s *Store) write(goal Goal) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("goal: create state directory: %w", err)
	}
	data, err := json.MarshalIndent(struct {
		Version int  `json:"version"`
		Goal    Goal `json:"goal"`
	}{Version: 1, Goal: goal}, "", "  ")
	if err != nil {
		return fmt.Errorf("goal: encode: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".goal-*.tmp")
	if err != nil {
		return fmt.Errorf("goal: create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("goal: protect temporary state: %w", err), temporary.Close())
	}
	if _, err := temporary.Write(data); err != nil {
		return errors.Join(fmt.Errorf("goal: write temporary state: %w", err), temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(fmt.Errorf("goal: sync temporary state: %w", err), temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("goal: close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("goal: replace state: %w", err)
	}
	temporaryPath = ""
	// A rename is only durable once the containing directory is synced.
	directory, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return fmt.Errorf("goal: open state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("goal: sync state directory: %w", err)
	}
	return nil
}

func newID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("goal: generate id: %w", err)
	}
	return "goal_" + hex.EncodeToString(data), nil
}
