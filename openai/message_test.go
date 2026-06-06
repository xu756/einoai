package openai

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFromSchemaMessagesConvertsStoredHistory(t *testing.T) {
	messages := FromSchemaMessages([]*schema.Message{
		{
			Role:    schema.User,
			Content: "查询郑州天气",
		},
		{
			Role:    schema.Assistant,
			Content: "我来查天气",
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: `{"location":"郑州"}`,
				},
			}},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			ToolName:   "get_weather",
			Content:    `{"weather":"sunny"}`,
		},
		{
			Role:    schema.Assistant,
			Content: "郑州晴天",
		},
	})

	if len(messages) != 4 {
		t.Fatalf("expected four OpenAI messages, got %#v", messages)
	}
	if messages[0].Role != "user" || rawString(t, messages[0].Content) != "查询郑州天气" {
		t.Fatalf("unexpected user message: %#v", messages[0])
	}
	if messages[1].Role != "assistant" || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("unexpected assistant tool message: %#v", messages[1])
	}
	toolCall := messages[1].ToolCalls[0]
	if toolCall.ID != "call_1" || toolCall.Function.Name != "get_weather" || toolCall.Function.Arguments != `{"location":"郑州"}` {
		t.Fatalf("unexpected tool call: %#v", toolCall)
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call_1" || rawString(t, messages[2].Content) != `{"weather":"sunny"}` {
		t.Fatalf("unexpected tool message: %#v", messages[2])
	}
	if messages[3].Role != "assistant" || rawString(t, messages[3].Content) != "郑州晴天" {
		t.Fatalf("unexpected final assistant message: %#v", messages[3])
	}
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatalf("decode content %s: %v", raw, err)
	}
	return text
}
