package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/schema"
)

type sliceEventStream struct {
	events []*einoai.RunEvent
	index  int
}

func (s *sliceEventStream) Next(ctx context.Context) (*einoai.RunEvent, error) {
	if s.index >= len(s.events) {
		return nil, io.EOF
	}
	ev := s.events[s.index]
	s.index++
	return ev, nil
}

func (s *sliceEventStream) Close() error {
	return nil
}

func TestWriteChatCompletionStreamWritesStandardToolCallDeltas(t *testing.T) {
	var buf bytes.Buffer

	err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: true,
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{
			ID:    "1-0",
			RunID: "run_1",
			Type:  einoai.EventToolCall,
			Data: einoai.ToolCallData{
				ID:    "call_00_P6ma2c1021vGwXNjT4gp7549",
				Name:  "get_weather",
				Index: 0,
			},
		},
		{
			ID:    "1-1",
			RunID: "run_1",
			Type:  einoai.EventToolCall,
			Data: einoai.ToolCallData{
				ID:        "call_00_P6ma2c1021vGwXNjT4gp7549",
				Name:      "get_weather",
				Arguments: `{"location":"北京"}`,
				Index:     0,
			},
		},
		{
			ID:    "1-2",
			RunID: "run_1",
			Type:  einoai.EventFinish,
			Data:  einoai.FinishData{FinishReason: "tool_calls"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	chunks := decodeChunks(t, buf.String())
	if len(chunks) != 4 {
		t.Fatalf("expected role, tool start, tool args, finish chunks, got %d", len(chunks))
	}

	start := chunks[1].Choices[0].Delta.ToolCalls[0]
	if start.ID != "call_00_P6ma2c1021vGwXNjT4gp7549" || start.Type != "function" || start.Function.Name != "get_weather" {
		t.Fatalf("unexpected tool start delta: %#v", start)
	}
	if start.Function.Arguments != "" {
		t.Fatalf("tool start should not include arguments, got %#v", start)
	}

	args := chunks[2].Choices[0].Delta.ToolCalls[0]
	if args.ID != "" || args.Type != "" || args.Function.Name != "" {
		t.Fatalf("arguments delta should not repeat id/type/name: %#v", args)
	}
	if args.Function.Arguments != `{"location":"北京"}` {
		t.Fatalf("unexpected arguments delta: %#v", args)
	}

	if chunks[3].Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected tool_calls finish reason, got %#v", chunks[3].Choices[0].FinishReason)
	}
}

func TestWriteChatCompletionStreamWritesUsageChunkWhenRequested(t *testing.T) {
	var buf bytes.Buffer

	err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{
			ID:    "1-0",
			RunID: "run_1",
			Type:  einoai.EventTextDelta,
			Data:  einoai.TextData{Delta: "hello"},
		},
		{
			ID:    "1-1",
			RunID: "run_1",
			Type:  einoai.EventFinish,
			Data: einoai.FinishData{
				FinishReason: "stop",
				Usage: &schema.TokenUsage{
					PromptTokens:     3,
					CompletionTokens: 2,
					TotalTokens:      5,
				},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	if strings.Count(body, `"usage":null`) != 3 {
		t.Fatalf("expected usage:null on non-usage chunks, got:\n%s", body)
	}
	chunks := decodeChunks(t, buf.String())
	if len(chunks) != 4 {
		t.Fatalf("expected role, text, finish, usage chunks, got %d", len(chunks))
	}
	if chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("expected stop finish reason, got %#v", chunks[2].Choices[0].FinishReason)
	}
	if chunks[2].Usage != nil {
		t.Fatalf("finish chunk should not include usage: %#v", chunks[2].Usage)
	}
	if len(chunks[3].Choices) != 0 {
		t.Fatalf("usage chunk should have empty choices: %#v", chunks[3].Choices)
	}
	if chunks[3].Usage == nil || chunks[3].Usage.TotalTokens != 5 {
		t.Fatalf("expected final usage chunk, got %#v", chunks[3].Usage)
	}
}

func TestWriteChatCompletionStreamToWritesWithoutGin(t *testing.T) {
	var buf bytes.Buffer
	err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: true,
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{
			ID:    "1-0",
			RunID: "run_1",
			Type:  einoai.EventTextDelta,
			Data:  einoai.TextData{Delta: "hello"},
		},
		{
			ID:    "1-1",
			RunID: "run_1",
			Type:  einoai.EventFinish,
			Data:  einoai.FinishData{FinishReason: "stop"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}

	chunks := decodeChunks(t, buf.String())
	if len(chunks) != 3 {
		t.Fatalf("expected role, text, finish chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != "hello" {
		t.Fatalf("expected text delta, got %#v", chunks[1])
	}
	if !strings.Contains(buf.String(), "data: [DONE]") {
		t.Fatalf("expected done event, got:\n%s", buf.String())
	}
}

func decodeChunks(t *testing.T, body string) []chatCompletionChunk {
	t.Helper()
	var chunks []chatCompletionChunk
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}
