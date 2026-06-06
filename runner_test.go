package einoai

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRunEventBuilderConvertsToolCallChunksToCompleteEvents(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	index := 0
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index: &index,
			ID:    "call_00_P6ma2c1021vGwXNjT4gp7549",
			Type:  "function",
			Function: schema.FunctionCall{
				Name: "get_weather",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index: &index,
			Type:  "function",
			Function: schema.FunctionCall{
				Arguments: `{"location":"北京"}`,
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "tool_calls",
		},
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.readAfter(ctx, "s1", "r1", "0-0", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var toolEvents []ToolCallData
	for _, ev := range events {
		if ev.Type != EventToolCall {
			continue
		}
		data, ok := DecodeEventData[ToolCallData](ev)
		if !ok {
			t.Fatalf("decode tool call event: %#v", ev)
		}
		toolEvents = append(toolEvents, data)
	}
	if len(toolEvents) != 2 {
		t.Fatalf("expected start and delta tool events, got %#v", toolEvents)
	}
	for _, data := range toolEvents {
		if data.ID != "call_00_P6ma2c1021vGwXNjT4gp7549" {
			t.Fatalf("unexpected tool call id: %#v", data)
		}
		if data.Name != "get_weather" {
			t.Fatalf("unexpected tool name: %#v", data)
		}
	}
	if toolEvents[1].Arguments != `{"location":"北京"}` {
		t.Fatalf("unexpected tool arguments: %#v", toolEvents[1])
	}
}
