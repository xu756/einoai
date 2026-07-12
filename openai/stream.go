package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/schema"
)

type chatCompletionChunk struct {
	ID           string   `json:"id"`
	Object       string   `json:"object"`
	Created      int64    `json:"created"`
	Model        string   `json:"model,omitempty"`
	Choices      []choice `json:"choices"`
	Usage        *usage   `json:"usage,omitempty"`
	IncludeUsage bool     `json:"-"`
}

type choice struct {
	Index        int   `json:"index"`
	Delta        delta `json:"delta"`
	FinishReason any   `json:"finish_reason"`
}

type delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function functionCallDelta `json:"function"`
}

type functionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type usage struct {
	PromptTokens            int                     `json:"prompt_tokens"`
	CompletionTokens        int                     `json:"completion_tokens"`
	TotalTokens             int                     `json:"total_tokens"`
	PromptTokensDetails     promptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type completionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type streamState struct {
	completionID string
	created      int64
	modelName    string
	includeUsage bool
	toolCalls    map[string]bool
}

// FlushFunc flushes buffered stream data to the client.
type FlushFunc func()

type chatCompletionStreamWriter struct {
	writer io.Writer
	flush  FlushFunc
}

// SetChatCompletionStreamHeaders sets OpenAI-compatible SSE headers.
func SetChatCompletionStreamHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
}

// WriteChatCompletionStreamTo writes OpenAI-compatible SSE chunks to any writer.
func WriteChatCompletionStreamTo(ctx context.Context, writer io.Writer, flush FlushFunc, req ChatCompletionsRequest, stream einoai.EventStream) error {
	out := chatCompletionStreamWriter{writer: writer, flush: flush}
	state := newStreamState(req)
	if err := out.writeChunk(state.chunk([]choice{{Index: 0, Delta: delta{Role: "assistant"}, FinishReason: nil}}, nil)); err != nil {
		return err
	}

	for {
		ev, err := stream.Next(ctx)
		if err == io.EOF {
			return out.writeDone()
		}
		if err != nil {
			_ = out.writeStreamError(err)
			return err
		}
		if ev == nil {
			continue
		}
		done, err := writeEvent(out, state, ev)
		if err != nil {
			return err
		}
		if done {
			return out.writeDone()
		}
	}
}

func newStreamState(req ChatCompletionsRequest) *streamState {
	modelName := req.Model
	if modelName == "" {
		modelName = "gpt-4"
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	return &streamState{
		completionID: "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		created:      time.Now().Unix(),
		modelName:    modelName,
		includeUsage: includeUsage,
		toolCalls:    make(map[string]bool),
	}
}

func (s *streamState) chunk(choices []choice, u *usage) chatCompletionChunk {
	return chatCompletionChunk{
		ID:           s.completionID,
		Object:       "chat.completion.chunk",
		Created:      s.created,
		Model:        s.modelName,
		Choices:      choices,
		Usage:        u,
		IncludeUsage: s.includeUsage,
	}
}

func (c chatCompletionChunk) MarshalJSON() ([]byte, error) {
	if c.IncludeUsage {
		type chunkWithUsage struct {
			ID      string   `json:"id"`
			Object  string   `json:"object"`
			Created int64    `json:"created"`
			Model   string   `json:"model,omitempty"`
			Choices []choice `json:"choices"`
			Usage   *usage   `json:"usage"`
		}
		return json.Marshal(chunkWithUsage{
			ID:      c.ID,
			Object:  c.Object,
			Created: c.Created,
			Model:   c.Model,
			Choices: c.Choices,
			Usage:   c.Usage,
		})
	}
	type chunkWithoutUsage struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Created int64    `json:"created"`
		Model   string   `json:"model,omitempty"`
		Choices []choice `json:"choices"`
		Usage   *usage   `json:"usage,omitempty"`
	}
	return json.Marshal(chunkWithoutUsage{
		ID:      c.ID,
		Object:  c.Object,
		Created: c.Created,
		Model:   c.Model,
		Choices: c.Choices,
		Usage:   c.Usage,
	})
}

// CollectChatCompletion aggregates a non-streaming response body.
func CollectChatCompletion(ctx context.Context, req ChatCompletionsRequest, stream einoai.EventStream) (map[string]any, error) {
	var content string
	var finishReason any = "stop"
	for {
		ev, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if ev == nil {
			continue
		}
		switch ev.Type {
		case einoai.EventTextDelta:
			data, _ := einoai.DecodeEventData[einoai.TextData](ev)
			content += data.Delta
		case einoai.EventFinish:
			data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
			if data.FinishReason != "" {
				finishReason = data.FinishReason
			}
		case einoai.EventError:
			data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
			return nil, fmt.Errorf("%s", data.Message)
		}
	}
	return map[string]any{
		"id":      "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": finishReason,
		}},
	}, nil
}

func writeEvent(w chatCompletionStreamWriter, state *streamState, ev *einoai.RunEvent) (bool, error) {
	d := delta{}
	var finishReason any

	switch ev.Type {
	case einoai.EventTextDelta:
		data, _ := einoai.DecodeEventData[einoai.TextData](ev)
		d.Content = data.Delta
	case einoai.EventReasoningDelta:
		data, _ := einoai.DecodeEventData[einoai.ReasoningData](ev)
		d.ReasoningContent = data.Delta
	case einoai.EventToolCall:
		data, _ := einoai.DecodeEventData[einoai.ToolCallData](ev)
		d.ToolCalls = []toolCallDelta{state.toolCallDelta(data)}
	case einoai.EventToolResult:
		return false, nil
	case einoai.EventFinish:
		data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
		if data.FinishReason != "" {
			finishReason = normalizeFinishReason(data.FinishReason)
		}
		if finishReason != "tool_calls" && state.includeUsage {
			if err := w.writeChunk(state.chunk([]choice{{Index: 0, Delta: d, FinishReason: finishReason}}, nil)); err != nil {
				return false, err
			}
			if err := w.writeChunk(state.chunk([]choice{}, convertUsage(data.Usage))); err != nil {
				return false, err
			}
			return true, nil
		}
	case einoai.EventError:
		data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
		if err := w.writeErrorData(data.Message); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}

	if err := w.writeChunk(state.chunk([]choice{{Index: 0, Delta: d, FinishReason: finishReason}}, nil)); err != nil {
		return false, err
	}
	return ev.Type == einoai.EventFinish && finishReason != "tool_calls", nil
}

func (s *streamState) toolCallDelta(data einoai.ToolCallData) toolCallDelta {
	out := toolCallDelta{
		Index: data.Index,
		Function: functionCallDelta{
			Arguments: data.Arguments,
		},
	}
	if !s.toolCalls[data.ID] {
		out.ID = data.ID
		out.Type = "function"
		out.Function.Name = data.Name
		s.toolCalls[data.ID] = true
	}
	return out
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "tool-calls":
		return "tool_calls"
	case "content-filter":
		return "content_filter"
	default:
		return reason
	}
}

func convertUsage(u *schema.TokenUsage) *usage {
	if u == nil {
		return nil
	}
	return &usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		PromptTokensDetails: promptTokensDetails{
			CachedTokens: u.PromptTokenDetails.CachedTokens,
		},
		CompletionTokensDetails: completionTokensDetails{
			ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		},
	}
}

func (w chatCompletionStreamWriter) writeChunk(chunk chatCompletionChunk) error {
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w.writer, "data: %s\n\n", b); err != nil {
		return err
	}
	w.flushNow()
	return nil
}

func (w chatCompletionStreamWriter) writeDone() error {
	if _, err := fmt.Fprint(w.writer, "data: [DONE]\n\n"); err != nil {
		return err
	}
	w.flushNow()
	return nil
}

func (w chatCompletionStreamWriter) writeErrorData(message string) error {
	errObj := map[string]any{"error": map[string]any{"message": message, "type": "server_error"}}
	b, _ := json.Marshal(errObj)
	if _, err := fmt.Fprintf(w.writer, "data: %s\n\n", b); err != nil {
		return err
	}
	w.flushNow()
	return nil
}

func (w chatCompletionStreamWriter) writeStreamError(err error) error {
	if err == nil {
		return nil
	}
	if writeErr := w.writeErrorData(err.Error()); writeErr != nil {
		return writeErr
	}
	return w.writeDone()
}

func (w chatCompletionStreamWriter) flushNow() {
	if w.flush != nil {
		w.flush()
	}
}
