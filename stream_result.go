package einoai

import "github.com/cloudwego/eino/schema"

// StreamResult is the protocol-neutral terminal data collected by an SSE writer.
// Output can contain partial messages when the returned error is non-nil.
type StreamResult struct {
	Output       []*schema.Message
	Usage        *schema.TokenUsage
	FinishReason string
}

func addTokenUsage(total, next *schema.TokenUsage) *schema.TokenUsage {
	if total == nil {
		return cloneTokenUsage(next)
	}
	if next == nil {
		return total
	}
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	total.TotalTokens += next.TotalTokens
	total.PromptTokenDetails.CachedTokens += next.PromptTokenDetails.CachedTokens
	total.CompletionTokensDetails.ReasoningTokens += next.CompletionTokensDetails.ReasoningTokens
	return total
}
