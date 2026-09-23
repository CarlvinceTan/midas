package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// CompactionSettings mirrors Pi's compaction configuration: compaction runs when
// the context grows past the model window minus ReserveTokens, and everything
// older than roughly KeepRecentTokens is replaced by one summary checkpoint.
type CompactionSettings struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

// DefaultCompactionSettings matches Pi's defaults.
func DefaultCompactionSettings() CompactionSettings {
	return CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000}
}

// Normalize fills unset fields with Pi's defaults so a stored settings value can
// never disable the reserve that keeps room for the response.
func (s CompactionSettings) Normalize() CompactionSettings {
	if s.ReserveTokens <= 0 {
		s.ReserveTokens = DefaultCompactionSettings().ReserveTokens
	}
	if s.KeepRecentTokens <= 0 {
		s.KeepRecentTokens = DefaultCompactionSettings().KeepRecentTokens
	}
	return s
}

// ShouldCompact reports whether the current context needs a compaction.
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool {
	if !settings.Enabled || contextWindow <= 0 {
		return false
	}
	settings = settings.Normalize()
	return contextTokens > contextWindow-settings.ReserveTokens
}

// estimatedImageChars is the token cost Midas assumes for one attached image,
// the same conservative estimate Pi uses when sizing content it cannot count.
const estimatedImageChars = 4800

// EstimateTokens approximates one message's token cost with Pi's chars/4 rule.
// It deliberately overestimates so a compaction happens early rather than late.
func EstimateTokens(message ai.Message) int {
	chars := 0
	switch value := message.(type) {
	case ai.UserMessage:
		chars = userContentChars(value.Content)
	case *ai.UserMessage:
		if value != nil {
			chars = userContentChars(value.Content)
		}
	case ai.AssistantMessage:
		chars = assistantContentChars(value.Content)
	case *ai.AssistantMessage:
		if value != nil {
			chars = assistantContentChars(value.Content)
		}
	case ai.ToolResultMessage:
		chars = assistantContentChars(value.Content)
	case *ai.ToolResultMessage:
		if value != nil {
			chars = assistantContentChars(value.Content)
		}
	}
	return (chars + 3) / 4
}

func userContentChars(content ai.UserContent) int {
	if text, ok := content.Text(); ok {
		return len(text)
	}
	chars := 0
	for _, part := range content.Parts() {
		switch value := part.(type) {
		case ai.TextContent:
			chars += len(value.Text)
		case *ai.TextContent:
			if value != nil {
				chars += len(value.Text)
			}
		case ai.ImageContent, *ai.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

func assistantContentChars(content []ai.Content) int {
	chars := 0
	for _, part := range content {
		switch value := part.(type) {
		case ai.TextContent:
			chars += len(value.Text)
		case *ai.TextContent:
			if value != nil {
				chars += len(value.Text)
			}
		case ai.ThinkingContent:
			chars += len(value.Thinking)
		case ai.ToolCall:
			arguments, err := json.Marshal(value.Arguments)
			if err == nil {
				chars += len(value.Name) + len(arguments)
			} else {
				chars += len(value.Name)
			}
		case *ai.ToolCall:
			if value != nil {
				chars += len(value.Name)
			}
		case ai.ImageContent, *ai.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

// ContextTokens estimates the live context size: the exact usage the provider
// reported for the last completed assistant turn, plus an estimate for whatever
// was appended after it.
func ContextTokens(messages []ai.Message) int {
	tokens := 0
	lastUsage := -1
	for index, message := range messages {
		assistant, ok := assistantMessage(message)
		if !ok {
			continue
		}
		if assistant.StopReason == ai.StopError || assistant.StopReason == ai.StopAborted {
			continue
		}
		if usage := contextUsageTokens(assistant.Usage); usage > 0 {
			tokens = usage
			lastUsage = index
		}
	}
	for index := lastUsage + 1; index < len(messages); index++ {
		if index < 0 {
			continue
		}
		tokens += EstimateTokens(messages[index])
	}
	if lastUsage < 0 {
		tokens = 0
		for _, message := range messages {
			tokens += EstimateTokens(message)
		}
	}
	return tokens
}

// contextUsageTokens is the context size a completed turn reported: the native
// total when the provider sends one, otherwise the sum of its components.
func contextUsageTokens(usage ai.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

func assistantMessage(message ai.Message) (ai.AssistantMessage, bool) {
	switch value := message.(type) {
	case ai.AssistantMessage:
		return value, true
	case *ai.AssistantMessage:
		if value != nil {
			return *value, true
		}
	}
	return ai.AssistantMessage{}, false
}

// PlanCompaction splits a conversation into the part to summarize and the recent
// tail to keep. It keeps roughly keepRecentTokens from the end and never cuts
// inside a tool call, so a kept tool result always follows its own call.
func PlanCompaction(messages []ai.Message, keepRecentTokens int) (summarize, keep []ai.Message, ok bool) {
	if len(messages) < 2 || keepRecentTokens <= 0 {
		return nil, nil, false
	}
	accumulated := 0
	cut := -1
	for index := len(messages) - 1; index >= 0; index-- {
		accumulated += EstimateTokens(messages[index])
		if accumulated < keepRecentTokens {
			continue
		}
		cut = index
		break
	}
	if cut < 0 {
		// The whole conversation fits the recent budget, so there is nothing old
		// enough to summarize.
		return nil, nil, false
	}
	cut = validCutIndex(messages, cut)
	if cut <= 0 {
		return nil, nil, false
	}
	return messages[:cut], messages[cut:], true
}

// validCutIndex moves a cut point forward until it lands on a message that can
// start the kept tail: never a tool result, which must stay with its call.
func validCutIndex(messages []ai.Message, cut int) int {
	for index := cut; index < len(messages); index++ {
		if _, tool := messages[index].(ai.ToolResultMessage); tool {
			continue
		}
		if _, tool := messages[index].(*ai.ToolResultMessage); tool {
			continue
		}
		return index
	}
	return len(messages)
}

// CompactionPrompt is Pi's structured checkpoint prompt.
const CompactionPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by the user]
- [Or "(none)" if not applicable]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// UpdateCompactionPrompt merges new messages into an existing checkpoint.
const UpdateCompactionPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use the same EXACT format as the previous summary. Keep each section concise.`

// summaryMarker introduces a checkpoint in the transcript so the model can tell
// it apart from a user message.
const summaryMarker = "Compaction summary of earlier conversation:"

// Summarize asks the session's own model for a checkpoint of messages. It uses
// no tools, no cache write, and at most reserveTokens of output, exactly like
// Pi's compaction call.
func Summarize(ctx context.Context, provider ai.Streamer, model ai.Model, options ai.StreamOptions, messages []ai.Message, previousSummary string, settings CompactionSettings) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("compaction: no provider")
	}
	if len(messages) == 0 {
		return "", fmt.Errorf("compaction: no messages to summarize")
	}
	settings = settings.Normalize()
	prompt := buildSummarizationPrompt(messages, previousSummary)
	request := ai.StreamOptions{
		APIKey:         options.APIKey,
		MaxTokens:      settings.ReserveTokens,
		CacheRetention: ai.CacheNone,
		SessionID:      options.SessionID,
		Timeout:        options.Timeout,
	}
	if model.Reasoning && options.Reasoning != "" && options.Reasoning != ai.ThinkingOff {
		request.Reasoning = options.Reasoning
	}
	stream, err := provider.Stream(ctx, model, ai.Context{
		SystemPrompt: "You write concise, structured context checkpoints so another agent can continue the work.",
		Messages:     []ai.Message{ai.NewUserMessage(prompt, time.Now())},
	}, request)
	if err != nil {
		return "", err
	}
	result, err := stream.Result(ctx)
	if err != nil {
		return "", err
	}
	if result.StopReason == ai.StopError || result.StopReason == ai.StopAborted {
		return "", fmt.Errorf("compaction: %s", result.ErrorMessage)
	}
	if result.StopReason == ai.StopLength {
		return "", fmt.Errorf("compaction: the summary hit the token cap and is incomplete")
	}
	var text strings.Builder
	for _, content := range result.Content {
		switch value := content.(type) {
		case ai.TextContent:
			text.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				text.WriteString(value.Text)
			}
		case ai.ToolCall, *ai.ToolCall:
			return "", fmt.Errorf("compaction: the summarizer tried to call a tool")
		}
	}
	summary := strings.TrimSpace(text.String())
	if summary == "" {
		return "", fmt.Errorf("compaction: the summarizer returned no text")
	}
	return summary, nil
}

// buildSummarizationPrompt serializes the messages for the summarizer, matching
// Pi's <conversation> envelope and update rules.
func buildSummarizationPrompt(messages []ai.Message, previousSummary string) string {
	var conversation strings.Builder
	conversation.WriteString("<conversation>\n")
	for _, message := range messages {
		conversation.WriteString(serializeMessage(message))
		conversation.WriteString("\n")
	}
	conversation.WriteString("</conversation>")
	if strings.TrimSpace(previousSummary) == "" {
		return conversation.String() + "\n\n" + CompactionPrompt
	}
	return conversation.String() + "\n\n<previous-summary>\n" + strings.TrimSpace(previousSummary) + "\n</previous-summary>\n\n" + UpdateCompactionPrompt
}

func serializeMessage(message ai.Message) string {
	switch value := message.(type) {
	case ai.UserMessage:
		return "user: " + userText(value.Content)
	case *ai.UserMessage:
		if value != nil {
			return "user: " + userText(value.Content)
		}
	case ai.AssistantMessage:
		return "assistant: " + assistantText(value.Content)
	case *ai.AssistantMessage:
		if value != nil {
			return "assistant: " + assistantText(value.Content)
		}
	case ai.ToolResultMessage:
		return "tool result (" + value.ToolName + "): " + assistantText(value.Content)
	case *ai.ToolResultMessage:
		if value != nil {
			return "tool result (" + value.ToolName + "): " + assistantText(value.Content)
		}
	}
	return ""
}

func userText(content ai.UserContent) string {
	if text, ok := content.Text(); ok {
		return strings.TrimSpace(text)
	}
	var text strings.Builder
	for _, part := range content.Parts() {
		switch value := part.(type) {
		case ai.TextContent:
			text.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				text.WriteString(value.Text)
			}
		}
	}
	return strings.TrimSpace(text.String())
}

func assistantText(content []ai.Content) string {
	var text strings.Builder
	for _, part := range content {
		switch value := part.(type) {
		case ai.TextContent:
			text.WriteString(value.Text)
		case *ai.TextContent:
			if value != nil {
				text.WriteString(value.Text)
			}
		case ai.ThinkingContent:
			// Thinking is scratch work; it never enters a checkpoint.
		case ai.ToolCall:
			text.WriteString("tool call " + value.Name)
		}
	}
	return strings.TrimSpace(text.String())
}

// ApplyCompaction replaces the summarized messages with one synthetic checkpoint
// message ahead of the kept tail.
func ApplyCompaction(summary string, keep []ai.Message, at time.Time) []ai.Message {
	message := ai.NewSyntheticMessage(summaryMarker+"\n\n"+strings.TrimSpace(summary), at)
	compacted := make([]ai.Message, 0, len(keep)+1)
	compacted = append(compacted, message)
	return append(compacted, keep...)
}

// PreviousSummary returns the summary of the newest checkpoint, if the
// conversation already contains one.
func PreviousSummary(messages []ai.Message) string {
	for _, message := range messages {
		user, ok := message.(ai.UserMessage)
		if !ok || !user.Synthetic {
			continue
		}
		if text := userText(user.Content); strings.HasPrefix(text, summaryMarker) {
			return strings.TrimSpace(strings.TrimPrefix(text, summaryMarker))
		}
	}
	return ""
}

// Compact summarizes the old part of a conversation and returns the rewritten
// history. It reports false when the conversation is already short enough.
func Compact(ctx context.Context, provider ai.Streamer, model ai.Model, options ai.StreamOptions, messages []ai.Message, settings CompactionSettings) ([]ai.Message, bool, error) {
	summarize, keep, ok := PlanCompaction(messages, settings.Normalize().KeepRecentTokens)
	if !ok {
		return nil, false, nil
	}
	summary, err := Summarize(ctx, provider, model, options, summarize, PreviousSummary(messages), settings)
	if err != nil {
		return nil, false, err
	}
	return ApplyCompaction(summary, keep, time.Now()), true, nil
}
