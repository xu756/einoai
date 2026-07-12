package einoai

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNewSessionRunResponsePreservesMessageSequence(t *testing.T) {
	messages := []*schema.Message{
		{
			Role:    schema.User,
			Content: "weather",
			Extra: map[string]any{
				"trace_id":        "t1",
				"_einoai_private": true,
			},
		},
		{
			Role:             schema.Assistant,
			ReasoningContent: "need a tool",
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "weather",
					Arguments: `{"city":"郑州"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			ToolName:   "weather",
			Content:    `{"temp":26}`,
		},
		{
			Role:    schema.Assistant,
			Content: "26C",
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "stop",
				Usage: &schema.TokenUsage{
					PromptTokens:     8,
					CompletionTokens: 4,
					TotalTokens:      12,
				},
			},
		},
	}

	got, err := NewSessionRunResponse(&RunInfo{SessionID: "s1"}, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("expected four independent messages, got %#v", got.Messages)
	}
	if got.Messages[1].Parts[0].Type != "reasoning" || got.Messages[1].Parts[1].Type != "tool-call" {
		t.Fatalf("unexpected assistant parts: %#v", got.Messages[1].Parts)
	}
	if got.Messages[2].Role != "tool" || got.Messages[2].Parts[0].Type != "tool-result" {
		t.Fatalf("unexpected tool message: %#v", got.Messages[2])
	}
	if got.Messages[3].Usage == nil || got.Messages[3].Usage.TotalTokens != 12 {
		t.Fatalf("usage missing: %#v", got.Messages[3])
	}
	if got.Messages[0].Metadata["trace_id"] != "t1" {
		t.Fatalf("public metadata missing: %#v", got.Messages[0].Metadata)
	}
	if _, exists := got.Messages[0].Metadata["_einoai_private"]; exists {
		t.Fatalf("internal metadata leaked: %#v", got.Messages[0].Metadata)
	}
}

func TestNewSessionRunResponseRejectsNonJSONMetadata(t *testing.T) {
	_, err := NewSessionRunResponse(nil, []*schema.Message{{
		Role:  schema.User,
		Extra: map[string]any{"bad": make(chan int)},
	}})
	if err == nil || !strings.Contains(err.Error(), "message JSON") {
		t.Fatalf("expected metadata error, got %v", err)
	}
}
