package einoai

import "github.com/cloudwego/eino/schema"

// NormalizedTokenUsage is a protocol-neutral token usage breakdown.
type NormalizedTokenUsage struct {
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
	CachedInputTokens   int
	UncachedInputTokens int
	ReasoningTokens     int
	TextOutputTokens    int
}

// NormalizeTokenUsage converts Eino usage into non-negative derived counts.
func NormalizeTokenUsage(usage *schema.TokenUsage) *NormalizedTokenUsage {
	if usage == nil {
		return nil
	}
	cached := max(usage.PromptTokenDetails.CachedTokens, 0)
	reasoning := max(usage.CompletionTokensDetails.ReasoningTokens, 0)
	return &NormalizedTokenUsage{
		InputTokens:         usage.PromptTokens,
		OutputTokens:        usage.CompletionTokens,
		TotalTokens:         usage.TotalTokens,
		CachedInputTokens:   cached,
		UncachedInputTokens: max(usage.PromptTokens-cached, 0),
		ReasoningTokens:     reasoning,
		TextOutputTokens:    max(usage.CompletionTokens-reasoning, 0),
	}
}
