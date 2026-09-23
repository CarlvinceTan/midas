package stats

import (
	"context"

	"github.com/CarlvinceTan/midas/pkg/storage"
	"github.com/CarlvinceTan/midas/pkg/ai"
)

// ScanMidas returns a scanner restricted to transcripts registered in the
// supplied Midas session store. It never discovers another agent's files.
func ScanMidas(store *storage.Store) ScanFunc {
	return func(ctx context.Context) (Summary, error) {
		if store == nil {
			return Summary{}, nil
		}
		registered := store.ReadSessions()
		summary := Summary{Sessions: int64(len(registered))}
		for _, session := range registered {
			if err := ctx.Err(); err != nil {
				return Summary{}, err
			}
			messages, err := store.ReadTranscript(session.ID)
			if err != nil {
				return Summary{}, err
			}
			entry := SessionCache{ID: session.ID, Title: session.Title}
			for _, message := range messages {
				usage, ok := assistantUsage(message)
				if !ok {
					continue
				}
				entry.Calls++
				entry.Input += int64(usage.Input)
				entry.Output += int64(usage.Output)
				entry.CacheRead += int64(usage.CacheRead)
				entry.CacheWrite += int64(usage.CacheWrite)
				summary.Calls++
				summary.Tokens.Input += int64(usage.Input)
				summary.Tokens.Output += int64(usage.Output)
				summary.Tokens.CacheRead += int64(usage.CacheRead)
				summary.Tokens.CacheWrite += int64(usage.CacheWrite)
				summary.Cost += usage.Cost.Total
			}
			if entry.Calls > 0 && entry.PromptTokens() > 0 {
				summary.SessionCache = append(summary.SessionCache, entry)
			}
		}
		return summary, nil
	}
}

func assistantUsage(message ai.Message) (ai.Usage, bool) {
	switch message := message.(type) {
	case ai.AssistantMessage:
		return message.Usage, true
	case *ai.AssistantMessage:
		if message != nil {
			return message.Usage, true
		}
	}
	return ai.Usage{}, false
}
