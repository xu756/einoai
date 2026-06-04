package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	enioai "enio-ai/enioai"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
)

type chatCompletionChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model,omitempty"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
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

// WriteChatCompletionStream writes enioai events as OpenAI-compatible SSE chunks.
func WriteChatCompletionStream(c *gin.Context, req ChatCompletionsRequest, stream enioai.EventStream) {
	setStreamHeaders(c)
	id := "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
	created := time.Now().Unix()
	modelName := req.Model
	if modelName == "" {
		modelName = "gpt-4"
	}
	writeChunk(c, "", chatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []choice{{Index: 0, Delta: delta{Role: "assistant"}, FinishReason: nil}},
	})

	for {
		ev, err := stream.Next(c.Request.Context())
		if err == io.EOF {
			writeDone(c)
			return
		}
		if err != nil {
			WriteStreamError(c, err)
			return
		}
		if ev == nil {
			continue
		}
		if writeEvent(c, ev, id, created, modelName) {
			writeDone(c)
			return
		}
	}
}

// CollectChatCompletion aggregates a non-streaming response body.
func CollectChatCompletion(ctx context.Context, req ChatCompletionsRequest, stream enioai.EventStream) (map[string]any, error) {
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
		case enioai.EventTextDelta:
			data, _ := enioai.DecodeEventData[enioai.TextData](ev)
			content += data.Delta
		case enioai.EventFinish:
			data, _ := enioai.DecodeEventData[enioai.FinishData](ev)
			if data.FinishReason != "" {
				finishReason = data.FinishReason
			}
		case enioai.EventError:
			data, _ := enioai.DecodeEventData[enioai.ErrorData](ev)
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

func writeEvent(c *gin.Context, ev *enioai.RunEvent, id string, created int64, modelName string) bool {
	d := delta{}
	var finishReason any
	var u *usage

	switch ev.Type {
	case enioai.EventTextDelta:
		data, _ := enioai.DecodeEventData[enioai.TextData](ev)
		d.Content = data.Delta
	case enioai.EventReasoningDelta:
		data, _ := enioai.DecodeEventData[enioai.ReasoningData](ev)
		d.ReasoningContent = data.Delta
	case enioai.EventToolCall:
		data, _ := enioai.DecodeEventData[enioai.ToolCallData](ev)
		d.ToolCalls = []toolCallDelta{{
			Index: data.Index,
			ID:    data.ID,
			Type:  "function",
			Function: functionCallDelta{
				Name:      data.Name,
				Arguments: data.Arguments,
			},
		}}
	case enioai.EventToolResult:
		return false
	case enioai.EventFinish:
		data, _ := enioai.DecodeEventData[enioai.FinishData](ev)
		if data.FinishReason != "" {
			finishReason = normalizeFinishReason(data.FinishReason)
		}
		if finishReason != "tool_calls" {
			u = convertUsage(data.Usage)
		}
	case enioai.EventError:
		data, _ := enioai.DecodeEventData[enioai.ErrorData](ev)
		writeErrorData(c, data.Message)
		return true
	default:
		return false
	}

	writeChunk(c, ev.ID, chatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []choice{{Index: 0, Delta: d, FinishReason: finishReason}},
		Usage:   u,
	})
	return ev.Type == enioai.EventFinish && finishReason != "tool_calls"
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
