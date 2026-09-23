package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/CarlvinceTan/midas/pkg/agent"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

var (
	createSchema = json.RawMessage(`{"type":"object","properties":{"objective":{"type":"string"},"tokenBudget":{"type":"integer","minimum":0},"maxTurns":{"type":"integer","minimum":0},"maxDurationSeconds":{"type":"integer","minimum":0}},"required":["objective"],"additionalProperties":false}`)
	getSchema    = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	updateSchema = json.RawMessage(`{"type":"object","properties":{"objective":{"type":"string"},"status":{"type":"string","enum":["active","paused","complete","blocked"]},"detail":{"type":"string","description":"Completion evidence or blocker reason"}},"anyOf":[{"required":["objective"]},{"required":["status"]}],"additionalProperties":false}`)
	clearSchema  = getSchema
)

// Tools returns the goal lifecycle tools backed by store.
func Tools(store *Store) []agent.Tool {
	return []agent.Tool{
		goalTool{name: "create_goal", description: "Create or reuse the current persistent goal.", schema: createSchema, store: store},
		goalTool{name: "get_goal", description: "Read the current goal, counters, limits, and status.", schema: getSchema, store: store},
		goalTool{name: "update_goal", description: "Atomically edit the current goal objective or lifecycle status.", schema: updateSchema, store: store},
		goalTool{name: "clear_goal", description: "Clear the current goal state.", schema: clearSchema, store: store},
	}
}

type goalTool struct {
	name        string
	description string
	schema      json.RawMessage
	store       *Store
}

func (t goalTool) Definition() ai.Tool {
	return ai.Tool{Name: t.name, Description: t.description, Parameters: t.schema}
}

func (goalTool) Sequential() bool { return true }

func (t goalTool) Execute(ctx context.Context, call ai.ToolCall, _ agent.ToolUpdate) (agent.ToolResult, error) {
	if t.store == nil {
		return agent.ToolResult{}, errors.New("goal: store is nil")
	}
	var value any
	var err error
	switch t.name {
	case "create_goal":
		objective, ok := call.Arguments["objective"].(string)
		if !ok {
			return agent.ToolResult{}, errors.New("goal: objective must be a string")
		}
		limits := Limits{}
		if limits.TokenBudget, err = integerArgument(call.Arguments, "tokenBudget"); err != nil {
			return agent.ToolResult{}, err
		}
		if limits.MaxTurns, err = integerArgument(call.Arguments, "maxTurns"); err != nil {
			return agent.ToolResult{}, err
		}
		seconds, parseErr := integerArgument(call.Arguments, "maxDurationSeconds")
		if parseErr != nil {
			return agent.ToolResult{}, parseErr
		}
		limits.MaxDuration = time.Duration(seconds) * time.Second
		value, err = t.store.Create(ctx, objective, limits)
	case "get_goal":
		value, err = t.store.Get(ctx)
	case "update_goal":
		patch := Patch{}
		if raw, ok := call.Arguments["objective"]; ok {
			objective, stringOK := raw.(string)
			if !stringOK {
				return agent.ToolResult{}, errors.New("goal: objective must be a string")
			}
			patch.Objective = &objective
		}
		if raw, ok := call.Arguments["status"]; ok {
			statusText, stringOK := raw.(string)
			if !stringOK {
				return agent.ToolResult{}, errors.New("goal: status must be a string")
			}
			status := Status(statusText)
			patch.Status = &status
		}
		if raw, ok := call.Arguments["detail"]; ok {
			detail, stringOK := raw.(string)
			if !stringOK {
				return agent.ToolResult{}, errors.New("goal: detail must be a string")
			}
			patch.Detail = detail
		}
		value, err = t.store.Update(ctx, patch)
	case "clear_goal":
		// A failed clear is returned as an error, so reaching here means it worked.
		err = t.store.Clear(ctx)
		value = map[string]any{"cleared": true}
	default:
		err = fmt.Errorf("goal: unsupported tool %q", t.name)
	}
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: []ai.Content{ai.NewText(string(encoded))}}, nil
}

func integerArgument(arguments map[string]any, name string) (int64, error) {
	value, ok := arguments[name]
	if !ok || value == nil {
		return 0, nil
	}
	switch value := value.(type) {
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		// A float64 outside the int64 range converts unpredictably (and differs by
		// architecture), so it is rejected before the conversion.
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt64 || value >= math.MaxInt64 {
			return 0, fmt.Errorf("goal: %s must be an integer", name)
		}
		return int64(value), nil
	case json.Number:
		integer, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("goal: %s must be an integer", name)
		}
		return integer, nil
	default:
		return 0, fmt.Errorf("goal: %s must be an integer", name)
	}
}
