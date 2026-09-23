package ai

import "strings"

type StopReason string

const (
	StopPending  StopReason = "pending"
	StopComplete StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
)

type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
)

type ModelCost struct {
	Input      float64         `json:"input"`
	Output     float64         `json:"output"`
	CacheRead  float64         `json:"cacheRead"`
	CacheWrite float64         `json:"cacheWrite"`
	Tiers      []ModelCostTier `json:"tiers,omitempty"`
}

type ModelCostTier struct {
	Input            float64 `json:"input"`
	Output           float64 `json:"output"`
	CacheRead        float64 `json:"cacheRead"`
	CacheWrite       float64 `json:"cacheWrite"`
	InputTokensAbove int     `json:"inputTokensAbove"`
}

// ModelPromptCache describes the provider's best-effort cache lifetime for
// each retention tier. A zero value means the lifetime is not known and the
// model must not be warmed automatically.
type ModelPromptCache struct {
	Short int `json:"short,omitempty"`
	Long  int `json:"long,omitempty"`
}

// SplitModelReference splits "provider/model" into its parts, keeping the
// fallback provider for a bare model ID.
func SplitModelReference(provider, model string) (string, string) {
	model = strings.TrimSpace(model)
	if slash := strings.IndexByte(model, '/'); slash > 0 && slash < len(model)-1 {
		return model[:slash], model[slash+1:]
	}
	return strings.TrimSpace(provider), model
}

type Model struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	API              string                    `json:"api"`
	Provider         string                    `json:"provider"`
	BaseURL          string                    `json:"baseUrl"`
	Reasoning        bool                      `json:"reasoning"`
	AdaptiveThinking bool                      `json:"adaptiveThinking,omitempty"`
	ThinkingLevelMap map[ThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	Input            []Modality                `json:"input"`
	Cost             ModelCost                 `json:"cost"`
	PromptCache      ModelPromptCache          `json:"promptCache,omitempty"`
	ContextWindow    int                       `json:"contextWindow"`
	MaxTokens        int                       `json:"maxTokens"`
	SamplingParams   map[string]any            `json:"samplingParams,omitempty"`
	Headers          map[string]string         `json:"headers,omitempty"`
}

type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Usage struct {
	Input        int       `json:"input"`
	Output       int       `json:"output"`
	CacheRead    int       `json:"cacheRead"`
	CacheWrite   int       `json:"cacheWrite"`
	CacheWrite1H *int      `json:"cacheWrite1h,omitempty"`
	Reasoning    *int      `json:"reasoning,omitempty"`
	TotalTokens  int       `json:"totalTokens"`
	Cost         UsageCost `json:"cost"`
}

func (u *Usage) RecalculateTotals() {
	u.TotalTokens = u.Input + u.Output + u.CacheRead + u.CacheWrite
	u.Cost.Total = u.Cost.Input + u.Cost.Output + u.Cost.CacheRead + u.Cost.CacheWrite
}

// CalculateCost applies the model's per-million-token rates. The highest
// matching input tier prices the whole request.
func CalculateCost(model Model, usage *Usage) UsageCost {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost
	matchedThreshold := -1
	for _, tier := range model.Cost.Tiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
			rates.Input = tier.Input
			rates.Output = tier.Output
			rates.CacheRead = tier.CacheRead
			rates.CacheWrite = tier.CacheWrite
			matchedThreshold = tier.InputTokensAbove
		}
	}
	longWrite := 0
	if usage.CacheWrite1H != nil {
		longWrite = *usage.CacheWrite1H
	}
	shortWrite := usage.CacheWrite - longWrite
	usage.Cost.Input = rates.Input / 1_000_000 * float64(usage.Input)
	usage.Cost.Output = rates.Output / 1_000_000 * float64(usage.Output)
	usage.Cost.CacheRead = rates.CacheRead / 1_000_000 * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (rates.CacheWrite*float64(shortWrite) + rates.Input*2*float64(longWrite)) / 1_000_000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage.Cost
}
