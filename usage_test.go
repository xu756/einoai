package einoai

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNormalizeTokenUsage(t *testing.T) {
	got := NormalizeTokenUsage(&schema.TokenUsage{
		PromptTokens:     10,
		CompletionTokens: 7,
		TotalTokens:      17,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 4,
		},
		CompletionTokensDetails: schema.CompletionTokensDetails{
			ReasoningTokens: 3,
		},
	})
	if got == nil || got.InputTokens != 10 || got.UncachedInputTokens != 6 {
		t.Fatalf("unexpected input usage: %#v", got)
	}
	if got.OutputTokens != 7 || got.TextOutputTokens != 4 || got.ReasoningTokens != 3 {
		t.Fatalf("unexpected output usage: %#v", got)
	}
}

func TestNormalizeTokenUsageClampsDerivedCounts(t *testing.T) {
	got := NormalizeTokenUsage(&schema.TokenUsage{
		PromptTokens:     2,
		CompletionTokens: 1,
		TotalTokens:      3,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 5,
		},
		CompletionTokensDetails: schema.CompletionTokensDetails{
			ReasoningTokens: 4,
		},
	})
	if got.UncachedInputTokens != 0 || got.TextOutputTokens != 0 {
		t.Fatalf("derived counts must be non-negative: %#v", got)
	}
}

func TestNormalizeTokenUsageAcceptsNil(t *testing.T) {
	if got := NormalizeTokenUsage(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
