package aisdk

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

func TestWriteToolCallWritesStandardToolInputEvents(t *testing.T) {
	var buf bytes.Buffer
	state := newStreamState()
	writer := eventStreamWriter{writer: &buf}

	if err := writeToolCall(writer, state, "1-0", einoai.ToolCallData{
		ID:    "call_00_P6ma2c1021vGwXNjT4gp7549",
		Name:  "get_weather",
		Index: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeToolCall(writer, state, "1-1", einoai.ToolCallData{
		ID:        "call_00_P6ma2c1021vGwXNjT4gp7549",
		Name:      "get_weather",
		Arguments: `{"location":"北京"}`,
		Index:     0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writePendingToolsAvailable(writer, state, "1-2"); err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	if strings.Count(body, `"type":"tool-input-available"`) != 1 {
		t.Fatalf("expected one tool-input-available event, got:\n%s", body)
	}
	if !strings.Contains(body, `"toolCallId":"call_00_P6ma2c1021vGwXNjT4gp7549"`) {
		t.Fatalf("expected original tool call id, got:\n%s", body)
	}
	if !strings.Contains(body, `"toolName":"get_weather"`) {
		t.Fatalf("expected original tool name, got:\n%s", body)
	}
	if strings.Contains(body, `"toolCallId":"tool_call_0"`) || strings.Contains(body, `"toolName":"tool"`) {
		t.Fatalf("unexpected fallback tool identity, got:\n%s", body)
	}
	if !strings.Contains(body, `"input":{"location":"北京"}`) {
		t.Fatalf("expected parsed tool input, got:\n%s", body)
	}
}

func TestWriteEventStreamToWritesWithoutGin(t *testing.T) {
	var buf bytes.Buffer
	stream := &aisdkSliceEventStream{events: []*einoai.RunEvent{
		{
			ID:    "1-0",
			RunID: "run_1",
			Type:  einoai.EventTextDelta,
			Data:  einoai.TextData{ID: "text_1", Delta: "hello"},
		},
		{
			ID:    "1-1",
			RunID: "run_1",
			Type:  einoai.EventFinish,
			Data:  einoai.FinishData{FinishReason: "stop"},
		},
	}}

	if err := WriteEventStreamTo(context.Background(), &buf, nil, stream); err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	if !strings.Contains(body, `data: {"messageId":"msg_run_1","type":"start"}`) {
		t.Fatalf("expected start event, got:\n%s", body)
	}
	if !strings.Contains(body, `"delta":"hello"`) {
		t.Fatalf("expected text delta, got:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done event, got:\n%s", body)
	}
}

func TestWriteEventStreamOrdersReasoningToolsAndFinalUsage(t *testing.T) {
	var buf bytes.Buffer
	err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventReasoningStart, Data: einoai.ReasoningData{ID: "reasoning_1"}},
		{RunID: "run_1", Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{ID: "reasoning_1", Delta: "think"}},
		{RunID: "run_1", Type: einoai.EventReasoningEnd, Data: einoai.ReasoningData{ID: "reasoning_1"}},
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{}`, Index: 0}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
		{RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "sunny"}},
		{RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{
			FinishReason: "stop",
			Usage:        &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	ordered := []string{
		`"type":"start"`,
		`"type":"start-step"`,
		`"type":"reasoning-start"`,
		`"type":"reasoning-delta"`,
		`"type":"reasoning-end"`,
		`"type":"tool-input-start"`,
		`"type":"tool-input-delta"`,
		`"type":"tool-input-available"`,
		`"type":"finish-step"`,
		`"type":"start-step"`,
		`"type":"tool-output-available"`,
		`"type":"text-start"`,
		`"type":"text-delta"`,
		`"type":"text-end"`,
		`"type":"finish"`,
		"data: [DONE]",
	}
	position := -1
	for _, marker := range ordered {
		next := strings.Index(body[position+1:], marker)
		if next < 0 {
			t.Fatalf("missing or out-of-order %s:\n%s", marker, body)
		}
		position += next + 1
	}
	if !strings.Contains(body, `"totalTokens":7`) {
		t.Fatalf("final usage missing:\n%s", body)
	}
}

type aisdkSliceEventStream struct {
	events []*einoai.RunEvent
	index  int
}

func (s *aisdkSliceEventStream) Next(context.Context) (*einoai.RunEvent, error) {
	if s.index >= len(s.events) {
		return nil, io.EOF
	}
	ev := s.events[s.index]
	s.index++
	return ev, nil
}

func (s *aisdkSliceEventStream) Close() error {
	return nil
}
