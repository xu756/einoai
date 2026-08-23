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

type canceledEventStream struct{}

func (*canceledEventStream) Next(context.Context) (*einoai.RunEvent, error) {
	return nil, context.Canceled
}

func (*canceledEventStream) Close() error {
	return nil
}

func TestWriteChatCompletionStreamHidesServerExecutedAgentSteps(t *testing.T) {
	var buf bytes.Buffer

	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: true,
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{RunID: "run_1", Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "get_weather", Arguments: `{"location":"北京"}`, Index: 0}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{RunID: "run_1", Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "get_weather", Content: "sunny"}},
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "sunny"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	chunks := decodeChunks(t, buf.String())
	if len(chunks) != 3 {
		t.Fatalf("expected role, final text and terminal finish chunks, got %d: %#v", len(chunks), chunks)
	}
	if chunks[1].Choices[0].Delta.Content != "sunny" || chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected strict OpenAI stream: %#v", chunks)
	}
	body := buf.String()
	if strings.Contains(body, "reasoning_content") || strings.Contains(body, "tool_calls") {
		t.Fatalf("internal Eino reasoning/tool steps leaked into Chat Completions wire: %s", body)
	}
}

func TestWriteChatCompletionStreamWritesUsageChunkWhenRequested(t *testing.T) {
	var buf bytes.Buffer

	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
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
	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
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

func TestWriteChatCompletionStreamAlwaysUsesSSEWhenRequestStreamIsFalse(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:  "gpt-4o",
		Stream: false,
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "hello"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	body := buf.String()
	if !strings.HasPrefix(body, "data: ") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("completions must always use SSE regardless of req.Stream:\n%s", body)
	}
	chunks := decodeChunks(t, body)
	if len(chunks) != 3 || chunks[1].Choices[0].Delta.Content != "hello" || chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected Chat Completions SSE chunks: %#v", chunks)
	}
}

func TestWriteChatCompletionStreamUsesStrictDataLinesOnly(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
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
	if strings.Contains(buf.String(), "reasoning_content") {
		t.Fatalf("non-standard reasoning_content leaked into OpenAI stream:\n%s", buf.String())
	}
}

func TestWriteChatCompletionStreamKeepsInternalAgentStepsOutOfWireAndFinalUsageLast(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{
		Model:         "gpt-4o",
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
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
	if len(chunks) != 4 {
		t.Fatalf("expected role, text, finish, usage chunks, got %d: %#v", len(chunks), chunks)
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" || chunks[1].Choices[0].Delta.Content != "sunny" || chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("strict final response order is wrong: %#v", chunks)
	}
	final := chunks[3]
	if len(final.Choices) != 0 || final.Usage == nil || final.Usage.TotalTokens != 7 {
		t.Fatalf("final usage chunk is wrong: %#v", final)
	}
	if strings.Contains(buf.String(), "reasoning_content") || strings.Contains(buf.String(), "tool_calls") {
		t.Fatalf("internal agent steps leaked into strict OpenAI wire: %s", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "data: [DONE]\n\n") {
		t.Fatalf("DONE is not last:\n%s", buf.String())
	}
}

func TestCollectChatCompletionUsesStrictFinalMessageAndPreservesUsage(t *testing.T) {
	body, _, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventReasoningDelta, Data: einoai.ReasoningData{Delta: "think"}},
		{Type: einoai.EventToolCall, Data: einoai.ToolCallData{ID: "call_1", Name: "weather", Arguments: `{"city":"郑州"}`, Index: 0}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "tool_calls"}},
		{Type: einoai.EventToolResult, Data: einoai.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "sunny"}},
		{Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "It is sunny."}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{
			FinishReason: "stop",
			Usage:        &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	choices := body["choices"].([]map[string]any)
	message := choices[0]["message"].(map[string]any)
	if message["content"] != "It is sunny." {
		t.Fatalf("final content missing: %#v", body)
	}
	if _, ok := message["reasoning_content"]; ok {
		t.Fatalf("non-standard reasoning_content leaked into aggregate OpenAI response: %#v", body)
	}
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("server-executed tool calls leaked into aggregate OpenAI response: %#v", body)
	}
	completionUsage, ok := body["usage"].(*usage)
	if !ok || completionUsage.TotalTokens != 7 {
		t.Fatalf("usage missing: %#v", body)
	}
}

func TestCollectChatCompletionOmitsAutomaticallyExecutedIntermediateTools(t *testing.T) {
	body, _, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
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

func TestWriteChatCompletionStreamReturnsCompleteEinoOutput(t *testing.T) {
	var buf bytes.Buffer
	want := []*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "weather", Arguments: `{}`}}}},
		{Role: schema.Tool, ToolCallID: "call_1", ToolName: "weather", Content: `{"temp":26}`},
		{Role: schema.Assistant, Content: "26°C"},
	}

	got, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "26°C"}},
		{RunID: "run_1", Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "stop", Output: want}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d output messages, got %#v", len(want), got)
	}
	if got[0].ToolCalls[0].ID != "call_1" || got[1].Role != schema.Tool || got[2].Content != "26°C" {
		t.Fatalf("complete Eino output was not returned: %#v", got)
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

func TestWriteChatCompletionStreamTreatsContextCancellationAsClientDisconnect(t *testing.T) {
	var buf bytes.Buffer
	_, err := WriteChatCompletionStreamTo(
		context.Background(),
		&buf,
		nil,
		ChatCompletionsRequest{Model: "gpt-4o", Stream: true},
		&canceledEventStream{},
	)
	if err != nil {
		t.Fatalf("client cancellation must not be a stream error: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, `"error"`) || strings.Contains(body, "[DONE]") {
		t.Fatalf("canceled client received terminal error data: %s", body)
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

func TestWriteChatCompletionStreamReturnsOutputAfterRunError(t *testing.T) {
	var buf bytes.Buffer
	want := []*schema.Message{{Role: schema.Assistant, Content: "partial"}}

	got, err := WriteChatCompletionStreamTo(context.Background(), &buf, nil, ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{RunID: "run_1", Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "partial"}},
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
	if !strings.Contains(body, `"type":"server_error"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected streamed error followed by DONE, got:\n%s", body)
	}
	if strings.Contains(body, `"finish_reason":"error"`) {
		t.Fatalf("internal error reason leaked into OpenAI finish_reason: %s", body)
	}
}

func TestCollectChatCompletionReturnsOutputAfterRunError(t *testing.T) {
	want := []*schema.Message{{Role: schema.Assistant, Content: "partial"}}
	body, got, err := CollectChatCompletion(context.Background(), ChatCompletionsRequest{Model: "gpt-4o"}, &sliceEventStream{events: []*einoai.RunEvent{
		{Type: einoai.EventTextDelta, Data: einoai.TextData{Delta: "partial"}},
		{Type: einoai.EventError, Data: einoai.ErrorData{Message: "boom"}},
		{Type: einoai.EventFinish, Data: einoai.FinishData{FinishReason: "error", Output: want}},
	}})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected run error, got %v", err)
	}
	if body != nil {
		t.Fatalf("failed collection must not return a successful completion body: %#v", body)
	}
	if len(got) != 1 || got[0].Content != "partial" {
		t.Fatalf("expected partial Eino output after error, got %#v", got)
	}
}
