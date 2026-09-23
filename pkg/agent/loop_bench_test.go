package agent

import (
	stdctx "context"
	"strings"
	"testing"
	"time"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// benchProvider streams a fixed reply so the benchmark measures the loop's own
// work: message assembly, event fan-out, and transcript handling.
type benchProvider struct{ deltas []string }

func (benchProvider) ID() string                                { return "bench" }
func (benchProvider) Models(stdctx.Context) ([]ai.Model, error) { return nil, nil }

func (p benchProvider) Stream(_ stdctx.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (*ai.AssistantStream, error) {
	stream := ai.NewAssistantStream()
	partial := ai.AssistantMessage{Role: ai.RoleAssistant, StopReason: ai.StopPending}
	stream.Push(ai.AssistantEvent{Type: ai.EventStart, Partial: &partial})
	for _, delta := range p.deltas {
		stream.Push(ai.AssistantEvent{Type: ai.EventTextDelta, Delta: delta})
	}
	message := ai.AssistantMessage{
		Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(strings.Join(p.deltas, ""))},
		StopReason: ai.StopComplete,
	}
	stream.Push(ai.AssistantEvent{Type: ai.EventDone, Reason: ai.StopComplete, Message: &message})
	return stream, nil
}

func benchmarkLoop(b *testing.B, turns, deltas int) {
	b.Helper()
	reply := make([]string, deltas)
	for index := range reply {
		reply[index] = "token "
	}
	filler := strings.Repeat("context ", 400)
	now := time.Unix(1, 0)
	messages := make([]ai.Message, 0, turns*2)
	for range turns {
		messages = append(messages,
			ai.NewUserMessage(filler, now),
			ai.AssistantMessage{Role: ai.RoleAssistant, Content: []ai.Content{ai.NewText(filler)}, StopReason: ai.StopComplete},
		)
	}
	config := Config{Provider: benchProvider{deltas: reply}, Model: ai.Model{ID: "bench"}}
	chatContext := Context{SystemPrompt: "You are Midas.", Messages: messages}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		stream := Run(stdctx.Background(), []ai.Message{ai.NewUserMessage("hi", now)}, chatContext, config)
		for {
			if _, ok, err := stream.Next(stdctx.Background()); err != nil || !ok {
				break
			}
		}
		if _, err := stream.Result(stdctx.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAgentLoop covers the hot path: one turn over a long transcript with a
// streamed reply.
func BenchmarkAgentLoop(b *testing.B) { benchmarkLoop(b, 20, 200) }

// BenchmarkAgentLoopShortTranscript isolates the fixed per-turn cost.
func BenchmarkAgentLoopShortTranscript(b *testing.B) { benchmarkLoop(b, 2, 50) }
