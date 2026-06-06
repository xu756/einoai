package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
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

// WriteChatCompletionStream writes einoai events as OpenAI-compatible SSE chunks.
func WriteChatCompletionStream(c *gin.Context, req ChatCompletionsRequest, stream einoai.EventStream) {
	setStreamHeaders(c)
	state := newStreamState(req)
	writeChunk(c, "", state.chunk([]choice{{Index: 0, Delta: delta{Role: "assistant"}, FinishReason: nil}}, nil))

	for {
		ev, err := stream.Next(c.Request.Context())
		if err == io.EOF {
			writeDone(c)
			return
		}
		if err != nil {
			writeStreamError(c, err)
			return
		}
		if ev == nil {
			continue
		}
		if writeEvent(c, state, ev) {
			writeDone(c)
			return
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

func writeEvent(c *gin.Context, state *streamState, ev *einoai.RunEvent) bool {
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
		return false
	case einoai.EventFinish:
		data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
		if data.FinishReason != "" {
			finishReason = normalizeFinishReason(data.FinishReason)
		}
		if finishReason != "tool_calls" && state.includeUsage {
			writeChunk(c, ev.ID, state.chunk([]choice{{Index: 0, Delta: d, FinishReason: finishReason}}, nil))
			writeChunk(c, "", state.chunk([]choice{}, convertUsage(data.Usage)))
			return true
		}
	case einoai.EventError:
		data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
		writeErrorData(c, data.Message)
		return true
	default:
		return false
	}

	writeChunk(c, ev.ID, state.chunk([]choice{{Index: 0, Delta: d, FinishReason: finishReason}}, nil))
	return ev.Type == einoai.EventFinish && finishReason != "tool_calls"
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

func setStreamHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

func writeChunk(c *gin.Context, eventID string, chunk chatCompletionChunk) {
	b, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	if eventID != "" {
		_, _ = fmt.Fprintf(c.Writer, "id: %s\n", eventID)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	c.Writer.Flush()
}

func writeDone(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

func writeErrorData(c *gin.Context, message string) {
	errObj := map[string]any{"error": map[string]any{"message": message, "type": "server_error"}}
	b, _ := json.Marshal(errObj)
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
}

func writeStreamError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	writeErrorData(c, err.Error())
	writeDone(c)
}
