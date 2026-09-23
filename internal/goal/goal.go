// Package goal implements Midas' persistent, single-active-goal lifecycle.
// It is independent of the TUI and provider so slash commands and agents share
// exactly the same state transitions.
package goal

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusComplete Status = "complete"
	StatusBlocked  Status = "blocked"
)

var (
	ErrNoGoal   = errors.New("goal: no current goal")
	ErrConflict = errors.New("goal: a different non-closed goal already exists")
	ErrClosed   = errors.New("goal: closed goals cannot be resumed")
)

type Limits struct {
	TokenBudget int64         `json:"tokenBudget,omitempty"`
	MaxTurns    int64         `json:"maxTurns,omitempty"`
	MaxDuration time.Duration `json:"-"`
}

type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
	Tokens int64     `json:"tokens,omitempty"`
	Turns  int64     `json:"turns,omitempty"`
}

type Goal struct {
	ID               string     `json:"id"`
	Objective        string     `json:"objective"`
	Status           Status     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	ActiveSince      *time.Time `json:"activeSince,omitempty"`
	ActiveDurationMS int64      `json:"activeDurationMs"`
	TokenBudget      int64      `json:"tokenBudget,omitempty"`
	TokensUsed       int64      `json:"tokensUsed"`
	MaxTurns         int64      `json:"maxTurns,omitempty"`
	TurnsUsed        int64      `json:"turnsUsed"`
	MaxDurationMS    int64      `json:"maxDurationMs,omitempty"`
	Evidence         string     `json:"evidence,omitempty"`
	Blocker          string     `json:"blocker,omitempty"`
	History          []Event    `json:"history"`
}

func (g Goal) Closed() bool { return g.Status == StatusComplete || g.Status == StatusBlocked }

func (g Goal) ActiveDuration(at time.Time) time.Duration {
	duration := time.Duration(g.ActiveDurationMS) * time.Millisecond
	if g.Status == StatusActive && g.ActiveSince != nil && at.After(*g.ActiveSince) {
		duration += at.Sub(*g.ActiveSince)
	}
	return duration
}

type LimitReason string

const (
	LimitNone     LimitReason = ""
	LimitTokens   LimitReason = "token_budget"
	LimitTurns    LimitReason = "turn_limit"
	LimitDuration LimitReason = "duration_limit"
)

func (g Goal) LimitReached(at time.Time) LimitReason {
	if g.TokenBudget > 0 && g.TokensUsed >= g.TokenBudget {
		return LimitTokens
	}
	if g.MaxTurns > 0 && g.TurnsUsed >= g.MaxTurns {
		return LimitTurns
	}
	if g.MaxDurationMS > 0 && g.ActiveDuration(at) >= time.Duration(g.MaxDurationMS)*time.Millisecond {
		return LimitDuration
	}
	return LimitNone
}

func normalizeObjective(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("goal: objective must not be empty")
	}
	return value, nil
}

func validateLimits(limits Limits) error {
	if limits.TokenBudget < 0 || limits.MaxTurns < 0 || limits.MaxDuration < 0 {
		return fmt.Errorf("goal: limits must not be negative")
	}
	return nil
}
