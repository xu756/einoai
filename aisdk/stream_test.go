package aisdk

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

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
