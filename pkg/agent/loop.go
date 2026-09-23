package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// Run starts a new agent run and returns immediately. The returned stream owns
// a copy of the context's message and tool slices; the caller's slices are not
// mutated.
func Run(ctx context.Context, prompts []ai.Message, initial Context, config Config) *Stream {
	stream := newStream()
	go run(ctx, prompts, initial, config, stream)
	return stream
}

func run(ctx context.Context, prompts []ai.Message, initial Context, config Config, stream *Stream) {
	current := Context{
		SystemPrompt: initial.SystemPrompt,
		Messages:     append([]ai.Message(nil), initial.Messages...),
		Tools:        append([]Tool(nil), initial.Tools...),
	}
	newMessages := append([]ai.Message(nil), prompts...)
	current.Messages = append(current.Messages, prompts...)
	warmer := config.CacheWarmer
	ownedWarmer := false
	if warmer == nil && config.CacheWarming != CacheWarmingOff {
		warmer = NewCacheWarmer(config.Provider, config.OnCacheWarm)
		ownedWarmer = true
	}
	if warmer != nil {
		defer func() {
			if ownedWarmer {
				// A warmer built for this run has no owner to warm for once it ends,
				// so it is always closed: settling it would leave a timer warming a
				// prefix nobody is waiting for.
				warmer.Close()
				return
			}
			warmer.settle(config.CacheWarming)
		}()
	}

	stream.Push(Event{Type: EventAgentStart})
	stream.Push(Event{Type: EventTurnStart})
	for _, prompt := range prompts {
		stream.Push(Event{Type: EventMessageStart, Message: prompt})
		stream.Push(Event{Type: EventMessageEnd, Message: prompt})
	}

	var runErr error
	for {
		message, modelErr := streamAssistant(ctx, current, config, stream, warmer)
		runErr = modelErr
		current.Messages = append(current.Messages, message)
		newMessages = append(newMessages, message)

		if message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
			stream.Push(Event{Type: EventTurnEnd, Message: message, Err: modelErr})
			break
		}

		calls := toolCalls(message)
		results := executeToolBatch(ctx, current.Tools, calls, message.StopReason, config.ToolExecution, stream)
		toolMessages := make([]ai.ToolResultMessage, 0, len(results))
		terminate := len(results) > 0
		for _, result := range results {
			toolMessage := toolResultMessage(result)
			toolMessages = append(toolMessages, toolMessage)
			current.Messages = append(current.Messages, toolMessage)
			newMessages = append(newMessages, toolMessage)
			stream.Push(Event{Type: EventMessageStart, Message: toolMessage})
			stream.Push(Event{Type: EventMessageEnd, Message: toolMessage})
			terminate = terminate && result.result.Terminate
		}

		stream.Push(Event{Type: EventTurnEnd, Message: message, ToolResults: toolMessages})
		terminal := message.StopReason == ai.StopError || message.StopReason == ai.StopAborted || ctx.Err() != nil
		if terminal {
			if config.Steering != nil {
				appendSteers(config.Steering.close(), &current, &newMessages, stream)
			}
			break
		}

		wouldFinish := len(calls) == 0 || terminate
		var steers []ai.Message
		if config.Steering != nil {
			if wouldFinish {
				var closed bool
				steers, closed = config.Steering.drainOrClose()
				if closed {
					break
				}
			} else {
				steers = config.Steering.drain()
			}
		} else if wouldFinish {
			break
		}
		appendSteers(steers, &current, &newMessages, stream)
		stream.Push(Event{Type: EventTurnStart})
	}

	stream.Push(Event{Type: EventAgentEnd, Messages: newMessages, Err: runErr})
}

func appendSteers(messages []ai.Message, current *Context, newMessages *[]ai.Message, stream *Stream) {
	for _, message := range messages {
		current.Messages = append(current.Messages, message)
		*newMessages = append(*newMessages, message)
		stream.Push(Event{Type: EventMessageStart, Message: message})
		stream.Push(Event{Type: EventMessageEnd, Message: message})
	}
}

func streamAssistant(ctx context.Context, current Context, config Config, events *Stream, warmer *CacheWarmer) (ai.AssistantMessage, error) {
	if config.Provider == nil {
		err := errors.New("agent: provider is nil")
		message := errorMessage(config.Model, err)
		events.Push(Event{Type: EventMessageStart, Message: message})
		events.Push(Event{Type: EventMessageEnd, Message: message})
		return message, err
	}

	definitions := make([]ai.Tool, 0, len(current.Tools))
	for _, tool := range current.Tools {
		definitions = append(definitions, tool.Definition())
	}
	input := ai.Context{
		SystemPrompt: current.SystemPrompt,
		Messages:     current.Messages,
		Tools:        definitions,
	}
	if warmer != nil {
		warmer.Stop()
	}
	response, err := config.Provider.Stream(ctx, config.Model, input, config.StreamOptions)
	if err != nil {
		message := errorMessage(config.Model, err)
		events.Push(Event{Type: EventMessageStart, Message: message})
		events.Push(Event{Type: EventMessageEnd, Message: message})
		return message, err
	}

	started := false
	for {
		event, ok, nextErr := response.Next(ctx)
		if nextErr != nil {
			message := errorMessage(config.Model, nextErr)
			if !started {
				events.Push(Event{Type: EventMessageStart, Message: message})
			}
			events.Push(Event{Type: EventMessageEnd, Message: message})
			return message, nextErr
		}
		if !ok {
			break
		}

		switch event.Type {
		case ai.EventStart:
			if event.Partial != nil {
				started = true
				events.Push(Event{Type: EventMessageStart, Message: event.Partial})
			}
		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd,
			ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd,
			ai.EventToolCallStart, ai.EventToolCallDelta, ai.EventToolCallEnd:
			if event.Partial != nil {
				copy := event
				events.Push(Event{Type: EventMessageUpdate, Message: event.Partial, AssistantEvent: &copy})
			}
		case ai.EventDone, ai.EventError:
			message, resultErr := response.Result(ctx)
			if resultErr != nil {
				message = errorMessage(config.Model, resultErr)
			}
			if !started {
				events.Push(Event{Type: EventMessageStart, Message: message})
			}
			events.Push(Event{Type: EventMessageEnd, Message: message})
			startCacheWarming(warmer, config, input, message)
			return message, assistantFailure(message, resultErr)
		}
	}

	message, err := response.Result(ctx)
	if err != nil {
		message = errorMessage(config.Model, err)
	}
	if !started {
		events.Push(Event{Type: EventMessageStart, Message: message})
	}
	events.Push(Event{Type: EventMessageEnd, Message: message})
	startCacheWarming(warmer, config, input, message)
	return message, assistantFailure(message, err)
}

func assistantFailure(message ai.AssistantMessage, err error) error {
	if err != nil {
		return err
	}
	if message.StopReason == ai.StopError && strings.TrimSpace(message.ErrorMessage) != "" {
		return errors.New(message.ErrorMessage)
	}
	if message.StopReason == ai.StopAborted {
		return context.Canceled
	}
	return nil
}

func startCacheWarming(warmer *CacheWarmer, config Config, input ai.Context, message ai.AssistantMessage) {
	if warmer == nil || message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
		return
	}
	warmer.start(cacheWarmRequest{
		model: config.Model, input: input, options: config.StreamOptions,
		promptTokens: message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheWrite,
	}, config.CacheWarming)
}

func errorMessage(model ai.Model, err error) ai.AssistantMessage {
	reason := ai.StopError
	if errors.Is(err, context.Canceled) {
		reason = ai.StopAborted
	}
	return ai.AssistantMessage{
		Role:         ai.RoleAssistant,
		API:          model.API,
		Provider:     model.Provider,
		Model:        model.ID,
		StopReason:   reason,
		ErrorMessage: err.Error(),
		Timestamp:    ai.UnixMillis(time.Now()),
	}
}

func toolCalls(message ai.AssistantMessage) []ai.ToolCall {
	var calls []ai.ToolCall
	for _, content := range message.Content {
		switch call := content.(type) {
		case ai.ToolCall:
			calls = append(calls, call)
		case *ai.ToolCall:
			if call != nil {
				calls = append(calls, *call)
			}
		}
	}
	return calls
}

type executedCall struct {
	call    ai.ToolCall
	result  ToolResult
	isError bool
}

func executeToolBatch(ctx context.Context, tools []Tool, calls []ai.ToolCall, stopReason ai.StopReason, mode ToolExecutionMode, events *Stream) []executedCall {
	if len(calls) == 0 {
		return nil
	}
	if stopReason == ai.StopLength {
		results := make([]executedCall, 0, len(calls))
		for _, call := range calls {
			events.Push(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
			result := failedCall(call, fmt.Errorf("tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated", call.Name))
			events.Push(toolEndEvent(result))
			results = append(results, result)
		}
		return results
	}

	if mode == ToolExecutionSequential || containsSequentialTool(tools, calls) {
		results := make([]executedCall, 0, len(calls))
		for _, call := range calls {
			if ctx.Err() != nil {
				// Every tool call needs a result, even when the run was cancelled:
				// the transcript pairs an assistant call with one result, and a
				// missing result makes the next provider request invalid.
				events.Push(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
				result := failedCall(call, ctx.Err())
				events.Push(toolEndEvent(result))
				results = append(results, result)
				continue
			}
			results = append(results, executeTool(ctx, tools, call, events))
		}
		return results
	}

	results := make([]executedCall, len(calls))
	var wg sync.WaitGroup
	for index, call := range calls {
		index, call := index, call
		events.Push(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[index] = executeToolAfterStart(ctx, tools, call, events)
		}()
	}
	wg.Wait()
	return results
}

func executeTool(ctx context.Context, tools []Tool, call ai.ToolCall, events *Stream) executedCall {
	events.Push(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
	return executeToolAfterStart(ctx, tools, call, events)
}

func executeToolAfterStart(ctx context.Context, tools []Tool, call ai.ToolCall, events *Stream) executedCall {
	var selected Tool
	for _, tool := range tools {
		if tool.Definition().Name == call.Name {
			selected = tool
			break
		}
	}
	if selected == nil {
		result := failedCall(call, fmt.Errorf("tool %s not found", call.Name))
		events.Push(toolEndEvent(result))
		return result
	}

	accepting := true
	var updateMu sync.Mutex
	result, err := selected.Execute(ctx, call, func(update ToolResult) {
		updateMu.Lock()
		defer updateMu.Unlock()
		if !accepting {
			return
		}
		copy := update
		events.Push(Event{
			Type:          EventToolExecutionUpdate,
			ToolCallID:    call.ID,
			ToolName:      call.Name,
			Arguments:     call.Arguments,
			PartialResult: &copy,
		})
	})
	updateMu.Lock()
	accepting = false
	updateMu.Unlock()
	if err != nil {
		executed := failedCall(call, err)
		events.Push(toolEndEvent(executed))
		return executed
	}
	executed := executedCall{call: call, result: result}
	events.Push(toolEndEvent(executed))
	return executed
}

func failedCall(call ai.ToolCall, err error) executedCall {
	return executedCall{
		call: call,
		result: ToolResult{
			Content: []ai.Content{ai.NewText(err.Error())},
			Details: map[string]any{},
		},
		isError: true,
	}
}

func toolEndEvent(executed executedCall) Event {
	result := executed.result
	return Event{
		Type:       EventToolExecutionEnd,
		ToolCallID: executed.call.ID,
		ToolName:   executed.call.Name,
		Result:     &result,
		IsError:    executed.isError,
	}
}

func toolResultMessage(executed executedCall) ai.ToolResultMessage {
	return ai.ToolResultMessage{
		Role:           ai.RoleToolResult,
		ToolCallID:     executed.call.ID,
		ToolName:       executed.call.Name,
		Content:        append([]ai.Content(nil), executed.result.Content...),
		Details:        executed.result.Details,
		Usage:          executed.result.Usage,
		AddedToolNames: append([]string(nil), executed.result.AddedToolNames...),
		IsError:        executed.isError,
		Timestamp:      ai.UnixMillis(time.Now()),
	}
}

func containsSequentialTool(tools []Tool, calls []ai.ToolCall) bool {
	for _, call := range calls {
		for _, tool := range tools {
			if tool.Definition().Name != call.Name {
				continue
			}
			if sequential, ok := tool.(SequentialTool); ok && sequential.Sequential() {
				return true
			}
		}
	}
	return false
}
