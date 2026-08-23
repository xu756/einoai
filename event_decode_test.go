package einoai

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestDecodeFinishDataPreservesOutputMessagesAfterJSONRoundTrip(t *testing.T) {
	original := FinishData{
		FinishReason: "stop",
		Output: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "weather",
						Arguments: `{"city":"Singapore"}`,
					},
				}},
			},
			{Role: schema.Tool, ToolCallID: "call_1", ToolName: "weather", Content: "sunny"},
			{Role: schema.Assistant, Content: "It is sunny.", ReasoningContent: "checked weather"},
		},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var redisLikeData map[string]any
	if err := json.Unmarshal(raw, &redisLikeData); err != nil {
		t.Fatal(err)
	}

	got, ok := DecodeEventData[FinishData](&RunEvent{Type: EventFinish, Data: redisLikeData})
	if !ok {
		t.Fatal("failed to decode FinishData after JSON round trip")
	}
	if len(got.Output) != 3 {
		t.Fatalf("expected 3 output messages, got %#v", got.Output)
	}
	if got.Output[0].ToolCalls[0].Function.Name != "weather" || got.Output[1].Role != schema.Tool || got.Output[2].ReasoningContent != "checked weather" {
		t.Fatalf("output messages lost fields after persisted event round trip: %#v", got.Output)
	}
}
