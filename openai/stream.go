package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
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
	runErr       error
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
//
// The returned messages are the complete Eino output messages for the run. They
// are not written into the OpenAI wire protocol; callers can persist them after
// the stream finishes.
func WriteChatCompletionStreamTo(ctx context.Context, writer io.Writer, flush FlushFunc, req ChatCompletionsRequest, stream einoai.EventStream) ([]*schema.Message, error) {
	out := chatCompletionStreamWriter{writer: writer, flush: flush}
	state := newStreamState(req)
	var output []*schema.Message
	if err := out.writeChunk(state.chunk([]choice{{Index: 0, Delta: delta{Role: "assistant"}, FinishReason: nil}}, nil)); err != nil {
		return nil, err
	}

	for {
		ev, err := stream.Next(ctx)
		if err == io.EOF {
			if writeErr := out.writeDone(); writeErr != nil {
				return output, writeErr
			}
			return output, state.runErr
		}
		if errors.Is(err, context.Canceled) {
			return output, nil
		}
		if err != nil {
			_ = out.writeStreamError(err)
			return output, err
		}
		if ev == nil {
			continue
		}
		if ev.Type == einoai.EventFinish {
			if data, ok := einoai.DecodeEventData[einoai.FinishData](ev); ok && data.FinishReason != "tool_calls" && data.FinishReason != "tool-calls" && data.Output != nil {
				output = data.Output
			}
		}
		done, err := writeEvent(out, state, ev)
		if err != nil {
			return output, err
		}
		if done {
			if writeErr := out.writeDone(); writeErr != nil {
				return output, writeErr
			}
			return output, state.runErr
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

// CollectChatCompletion aggregates the protocol-level final response while also
// returning the complete Eino output messages. HTTP handlers do not use this to
// switch response modes; completions are always streamed.
func CollectChatCompletion(ctx context.Context, req ChatCompletionsRequest, stream einoai.EventStream) (map[string]any, []*schema.Message, error) {
	var output []*schema.Message
	var content strings.Builder
	var finalUsage *usage
	var streamErr error
	finishReason := "stop"
	for {
		ev, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, output, err
		}
		if ev == nil {
			continue
		}
		switch ev.Type {
		case einoai.EventTextDelta:
			data, _ := einoai.DecodeEventData[einoai.TextData](ev)
			content.WriteString(data.Delta)
		case einoai.EventReasoningDelta, einoai.EventToolCall, einoai.EventToolResult:
			// Chat Completions has no standard server-executed agent-step or raw
			// reasoning event. Keep those details in FinishData.Output instead of
			// leaking provider-specific fields into the OpenAI wire response.
		case einoai.EventFinish:
			data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
			if data.FinishReason == "tool_calls" || data.FinishReason == "tool-calls" {
				continue
			}
			if data.FinishReason != "" {
				finishReason = normalizeFinishReason(data.FinishReason)
			}
			if data.Usage != nil {
				finalUsage = convertUsage(data.Usage)
			}
			if data.Output != nil {
				output = data.Output
			}
		case einoai.EventError:
			data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
			streamErr = fmt.Errorf("%s", data.Message)
		}
	}
	if streamErr != nil {
		return nil, output, streamErr
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content.String(),
	}
	body := map[string]any{
		"id":      "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if finalUsage != nil {
		body["usage"] = finalUsage
	}
	return body, output, nil
}

func writeEvent(w chatCompletionStreamWriter, state *streamState, ev *einoai.RunEvent) (bool, error) {
	d := delta{}
	var finishReason any

	switch ev.Type {
	case einoai.EventTextDelta:
		data, _ := einoai.DecodeEventData[einoai.TextData](ev)
		d.Content = data.Delta
	case einoai.EventReasoningStart, einoai.EventReasoningDelta, einoai.EventReasoningEnd,
		einoai.EventToolCall, einoai.EventToolResult:
		// Eino agent reasoning and server-executed tools are internal agent
		// steps. Chat Completions does not define a provider-executed tool-step
		// lifecycle, so these are intentionally omitted from the wire stream.
		return false, nil
	case einoai.EventFinish:
		data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
		if data.FinishReason != "" {
			finishReason = normalizeFinishReason(data.FinishReason)
		}
		// Tool calls are executed inside the Eino agent. An intermediate
		// tool_calls finish marker would prematurely terminate an OpenAI client
		// choice, so only the terminal agent step gets a finish_reason.
		if finishReason == "tool_calls" {
			return false, nil
		}
		// A run failure is sent as an OpenAI-compatible error payload first. The
		// following internal finish event only exists so we can collect the exact
		// Eino output messages; do not leak the internal "error" reason as an
		// invalid ChatCompletion finish_reason.
		if state.runErr != nil {
			return true, nil
		}
		if state.includeUsage {
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
		state.runErr = errors.New(data.Message)
		return false, nil
	default:
		return false, nil
	}

	if err := w.writeChunk(state.chunk([]choice{{Index: 0, Delta: d, FinishReason: finishReason}}, nil)); err != nil {
		return false, err
	}
	return ev.Type == einoai.EventFinish && finishReason != "tool_calls", nil
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "", "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "tool-calls":
		return "tool_calls"
	case "content_filter", "content-filter":
		return "content_filter"
	case "function_call", "function-call":
		return "function_call"
	default:
		// Internal reasons such as cancelled/error are not valid OpenAI
		// ChatCompletion finish reasons. Error runs are handled separately;
		// cancellation is represented as an ordinary stopped stream.
		return "stop"
	}
}

func convertUsage(u *schema.TokenUsage) *usage {
	normalized := einoai.NormalizeTokenUsage(u)
	if normalized == nil {
		return nil
	}
	return &usage{
		PromptTokens:     normalized.InputTokens,
		CompletionTokens: normalized.OutputTokens,
		TotalTokens:      normalized.TotalTokens,
		PromptTokensDetails: promptTokensDetails{
			CachedTokens: normalized.CachedInputTokens,
		},
		CompletionTokensDetails: completionTokensDetails{
			ReasoningTokens: normalized.ReasoningTokens,
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
