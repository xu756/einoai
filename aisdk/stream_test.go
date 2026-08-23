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
	if !strings.Contains(body, `"providerExecuted":true`) {
		t.Fatalf("server-executed tools must be marked providerExecuted, got:\n%s", body)
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

	if _, err := WriteEventStreamTo(context.Background(), &buf, nil, stream); err != nil {
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
	_, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
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
		`"type":"tool-output-available"`,
		`"type":"finish-step"`,
		`"type":"start-step"`,
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

func TestWriteEventStreamScopesRepeatedToolCallIDPerStep(t *testing.T) {
	var buf bytes.Buffer
	events := []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_0", Name: "first_tool", Arguments: `{"step":1}`}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_0", Name: "first_tool", Content: "first result"}},
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_0", Name: "second_tool", Arguments: `{"step":2}`}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_0", Name: "second_tool", Content: "second result"}},
		{RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "done"}},
		{RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}

	if _, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: events}); err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	for _, partType := range []string{"tool-input-start", "tool-input-available", "tool-output-available"} {
		if got := strings.Count(body, `"type":"`+partType+`"`); got != 2 {
			t.Fatalf("expected two independent %s parts, got %d:\n%s", partType, got, body)
		}
	}
	if !strings.Contains(body, `"toolName":"first_tool"`) || !strings.Contains(body, `"toolName":"second_tool"`) {
		t.Fatalf("tool names from both steps must be preserved:\n%s", body)
	}
}

func TestWriteEventStreamWaitsForAllParallelToolResults(t *testing.T) {
	var buf bytes.Buffer
	events := []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "first_tool", Arguments: `{}`}},
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_2", Name: "second_tool", Arguments: `{}`}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "first_tool", Content: "first result"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_2", Name: "second_tool", Content: "second result"}},
		{RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "done"}},
		{RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}

	if _, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: events}); err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	firstOutput := strings.Index(body, `"output":"first result"`)
	secondOutput := strings.Index(body, `"output":"second result"`)
	firstStepFinish := strings.Index(body, `"type":"finish-step"`)
	if firstOutput < 0 || secondOutput < 0 || firstStepFinish < 0 {
		t.Fatalf("parallel tool lifecycle is incomplete:\n%s", body)
	}
	if firstStepFinish < secondOutput {
		t.Fatalf("tool step finished before every parallel result was available:\n%s", body)
	}
	if nextStep := strings.Index(body[firstStepFinish:], `"type":"start-step"`); nextStep < 0 {
		t.Fatalf("next step did not start after parallel tool completion:\n%s", body)
	}
}

func TestWriteEventStreamReturnsCompleteEinoOutputAndUsesDataLinesOnly(t *testing.T) {
	var buf bytes.Buffer
	want := []*schema.Message{{Role: schema.Assistant, Content: "hello"}}
	got, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
		{ID: "1-0", RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{ID: "1-1", RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "hello"}},
		{ID: "1-2", RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{ID: "1-3", RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop", Output: want}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("complete Eino output was not returned: %#v", got)
	}
	if strings.Contains(buf.String(), "id: ") {
		t.Fatalf("AI SDK wire stream must contain data frames only, got:\n%s", buf.String())
	}
}

func TestWriteEventStreamTreatsContextCancellationAsClientDisconnect(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteEventStreamTo(context.Background(), &buf, nil, &canceledEventStream{})
	if err != nil {
		t.Fatalf("client cancellation must not be a stream error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("canceled client must not receive error or DONE data: %q", buf.String())
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

type canceledEventStream struct{}

func (*canceledEventStream) Next(context.Context) (*einoai.RunEvent, error) {
	return nil, context.Canceled
}

func (*canceledEventStream) Close() error {
	return nil
}

func TestWriteEventStreamReturnsOutputAfterRunError(t *testing.T) {
	var buf bytes.Buffer
	want := []*schema.Message{{Role: schema.Assistant, Content: "partial"}}
	got, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "partial"}},
		{RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventError, Data: einoai.ErrorData{Message: "boom"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "error", Output: want}},
	}})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected run error after stream terminates, got %v", err)
	}
	if len(got) != 1 || got[0].Content != "partial" {
		t.Fatalf("expected partial Eino output after error, got %#v", got)
	}
	body := buf.String()
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"finishReason":"error"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected AI SDK error/finish/DONE lifecycle, got:\n%s", body)
	}
}

func TestWriteEventStreamUsesAbortForCancelledRunAndReturnsPartialOutput(t *testing.T) {
	var buf bytes.Buffer
	want := []*schema.Message{{Role: schema.Assistant, Content: "partial"}}
	got, err := WriteEventStreamTo(context.Background(), &buf, nil, &aisdkSliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventTextStart, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{ID: "text_1", Delta: "partial"}},
		{RunID: "run_1", Type: einoai.EventTextEnd, Data: einoai.TextData{ID: "text_1"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "cancelled", Output: want}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "partial" {
		t.Fatalf("expected partial output on cancellation, got %#v", got)
	}
	body := buf.String()
	if !strings.Contains(body, `"type":"abort"`) || strings.Contains(body, `"finishReason":"other"`) {
		t.Fatalf("cancelled run must use AI SDK abort chunk: %s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("DONE is not last after abort: %s", body)
	}
}
