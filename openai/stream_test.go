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

func TestWriteChatCompletionStreamUsesDataLinesOnly(t *testing.T) {
	var buf bytes.Buffer
	err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: true,
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{ID: "1-0", Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{ID: "1-1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "id: ") {
		t.Fatalf("unexpected SSE id field:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"reasoning_content":"think"`) {
		t.Fatalf("reasoning chunk missing:\n%s", buf.String())
	}
}

func TestWriteChatCompletionStreamOrdersReasoningToolsFinishAndUsage(t *testing.T) {
	var buf bytes.Buffer
	err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:         "gpt-4o",
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Index: 0}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "sunny"}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{
			FinishReason: "stop",
			Usage:        &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	chunks := decodeChunks(t, buf.String())
	if len(chunks) != 8 {
		t.Fatalf("expected 8 ordered chunks, got %d: %#v", len(chunks), chunks)
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" || chunks[1].Choices[0].Delta.ReasoningContent != "think" {
		t.Fatalf("role/reasoning order is wrong: %#v", chunks[:2])
	}
	if chunks[2].Choices[0].Delta.ToolCalls[0].ID != "call_1" || chunks[3].Choices[0].Delta.ToolCalls[0].Function.Arguments == "" {
		t.Fatalf("tool deltas are wrong: %#v", chunks[2:4])
	}
	if chunks[4].Choices[0].FinishReason != "tool_calls" || chunks[6].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish chunks are wrong: %#v %#v", chunks[4], chunks[6])
	}
	final := chunks[7]
	if len(final.Choices) != 0 || final.Usage == nil || final.Usage.TotalTokens != 7 {
		t.Fatalf("final usage chunk is wrong: %#v", final)
	}
	if !strings.HasSuffix(buf.String(), "data: [DONE]\n\n") {
		t.Fatalf("DONE is not last:\n%s", buf.String())
	}
}

func TestCollectChatCompletionIncludesReasoningToolsAndUsage(t *testing.T) {
	body, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Index: 0}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{
			FinishReason: "tool_calls",
			Usage:        &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	choices := body["choices"].([]map[string]any)
	message := choices[0]["message"].(map[string]any)
	toolCalls, ok := message["tool_calls"].([]ToolCall)
	if message["reasoning_content"] != "think" || !ok || len(toolCalls) != 1 {
		t.Fatalf("missing completion data: %#v", body)
	}
	if toolCalls[0].Function.Arguments != `{"city":"郑州"}` {
		t.Fatalf("tool arguments missing: %#v", toolCalls)
	}
	completionUsage, ok := body["usage"].(*usage)
	if !ok || completionUsage.TotalTokens != 7 {
		t.Fatalf("usage missing: %#v", body)
	}
}

func TestCollectChatCompletionOmitsAutomaticallyExecutedIntermediateTools(t *testing.T) {
	body, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{}`, Index: 0}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
		{Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "It is sunny."}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	message := body["choices"].([]map[string]any)[0]["message"].(map[string]any)
	if _, exists := message["tool_calls"]; exists {
		t.Fatalf("intermediate tool calls leaked into final answer: %#v", message)
	}
	if message["content"] != "It is sunny." {
		t.Fatalf("final content missing: %#v", message)
	}
}

func TestConvertUsagePreservesNormalizedDetails(t *testing.T) {
	got := convertUsage(&schema.TokenUsage{
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
	if got.PromptTokensDetails.CachedTokens != 5 || got.CompletionTokensDetails.ReasoningTokens != 4 {
		t.Fatalf("detail counts were lost: %#v", got)
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
