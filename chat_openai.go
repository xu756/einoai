package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
)

type OpenAIChatCompletionChunk struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model,omitempty"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
	Choices           []OpenAIChoice         `json:"choices"`
	Usage             *OpenAICompletionUsage `json:"usage,omitempty"`
}

type OpenAIChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason any         `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`

	// OpenAI 标准字段：注意是 tool_calls，复数，数组。
	ToolCalls []OpenAIToolCallDelta `json:"tool_calls,omitempty"`

	// 非 OpenAI 官方字段，但对 Agent UI 很有用。
	// OpenAI 官方 chat completion stream 不会把 tool result 作为 assistant delta 返回；
	// 这里作为兼容扩展，前端可以用它渲染工具结果。
	ToolResult *OpenAIToolResultDelta `json:"tool_result,omitempty"`
}

type OpenAIToolCallDelta struct {
	Index    int                     `json:"index"`
	ID       string                  `json:"id,omitempty"`
	Type     string                  `json:"type,omitempty"`
	Function OpenAIFunctionCallDelta `json:"function"`
}

type OpenAIFunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type OpenAIToolResultDelta struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content,omitempty"`
}

type OpenAICompletionUsage struct {
	PromptTokens            int                           `json:"prompt_tokens"`
	CompletionTokens        int                           `json:"completion_tokens"`
	TotalTokens             int                           `json:"total_tokens"`
	PromptTokensDetails     OpenAIPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails OpenAICompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type OpenAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type OpenAICompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func streamOpenAICompatible(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent], modelName string) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	id := "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
	created := time.Now().Unix()

	for {
		event, ok := iter.Next()
		if !ok {
			writeOpenAISSEData(c, "[DONE]")
			return
		}

		if event == nil {
			continue
		}

		if event.Err != nil {
			// OpenAI error object 风格
			errObj := map[string]any{
				"error": map[string]any{
					"message": event.Err.Error(),
					"type":    "server_error",
				},
			}
			writeOpenAISSEJSON(c, errObj)
			writeOpenAISSEData(c, "[DONE]")
			return
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput

		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}

					errObj := map[string]any{
						"error": map[string]any{
							"message": err.Error(),
							"type":    "stream_error",
						},
					}
					writeOpenAISSEJSON(c, errObj)
					writeOpenAISSEData(c, "[DONE]")
					return
				}

				if msg == nil {
					continue
				}

				writeEinoMessageAsOpenAIChunk(c, id, created, modelName, msg)
			}

			continue
		}

		if mv.Message != nil {
			writeEinoMessageAsOpenAIChunk(c, id, created, modelName, mv.Message)
		}
	}
}

func writeEinoMessageAsOpenAIChunk(
	c *gin.Context,
	id string,
	created int64,
	modelName string,
	msg *schema.Message,
) {
	delta := OpenAIDelta{}

	if msg.Role != "" {
		delta.Role = string(msg.Role)
	}

	if msg.Content != "" {
		delta.Content = msg.Content
	}

	// 关键：保留 reasoning_content
	if msg.ReasoningContent != "" {
		delta.ReasoningContent = msg.ReasoningContent
	}

	// 关键：保留 OpenAI 标准 tool_calls 数组
	if len(msg.ToolCalls) > 0 {
		delta.ToolCalls = make([]OpenAIToolCallDelta, 0, len(msg.ToolCalls))

		for i, tc := range msg.ToolCalls {
			index := i
			if tc.Index != nil {
				index = *tc.Index
			}

			delta.ToolCalls = append(delta.ToolCalls, OpenAIToolCallDelta{
				Index: index,
				ID:    tc.ID,
				Type:  tc.Type,
				Function: OpenAIFunctionCallDelta{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
	}

	// Eino tool message -> Agent UI 扩展字段
	// OpenAI 官方协议里，tool result 通常是下一轮请求里的 role=tool message；
	// 但如果你要让前端实时展示工具结果，可以保留成 tool_result。
	if msg.Role == schema.Tool {
		delta.ToolResult = &OpenAIToolResultDelta{
			ToolCallID: msg.ToolCallID,
			Name:       msg.ToolName,
			Content:    msg.Content,
		}

		// 避免工具结果也被普通 content 渲染两次
		delta.Content = ""
	}

	var finishReason any = nil
	var usage *OpenAICompletionUsage

	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.FinishReason != "" {
			finishReason = msg.ResponseMeta.FinishReason
		}

		if msg.ResponseMeta.Usage != nil {
			usage = convertEinoUsage(msg.ResponseMeta.Usage)
		}
	}

	// 空 delta 但带 finish_reason / usage 的 chunk 也必须发。
	// 你的样例里 token 花费就是这种 chunk。
	chunk := OpenAIChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelName,
		Choices: []OpenAIChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
		Usage: usage,
	}

	writeOpenAISSEJSON(c, chunk)
}

func convertEinoUsage(u *schema.TokenUsage) *OpenAICompletionUsage {
	if u == nil {
		return nil
	}

	return &OpenAICompletionUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		PromptTokensDetails: OpenAIPromptTokensDetails{
			CachedTokens: u.PromptTokenDetails.CachedTokens,
		},
		CompletionTokensDetails: OpenAICompletionTokensDetails{
			ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		},
	}
}

func writeOpenAISSEJSON(c *gin.Context, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}

	writeOpenAISSEData(c, string(b))
}

func writeOpenAISSEData(c *gin.Context, data string) {
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	c.Writer.Flush()
}

// streamAgentEvents 将 eino AgentEvent 原样以 SSE 流式推送给前端。
// 流式消息会逐 chunk 从 MessageStream 读取，每个 chunk（*schema.Message）直接 JSON 序列化发送，不做任何字段裁剪。
func streamAgentEvents(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	for {
		event, ok := iter.Next()
		if !ok {
			c.SSEvent("done", "[DONE]")
			c.Writer.Flush()
			return
		}

		if event.Err != nil {
			errData, _ := json.Marshal(map[string]string{"error": event.Err.Error()})
			c.SSEvent("error", string(errData))
			c.Writer.Flush()
			return
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput

		// 流式：逐 chunk 读 MessageStream，每个 chunk 就是一个 *schema.Message，原样发送
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					break // io.EOF = 流结束
				}
				data, _ := json.Marshal(msg)
				c.SSEvent("message", string(data))
				c.Writer.Flush()
			}
			continue
		}

		// 非流式：完整 *schema.Message，原样发送
		if mv.Message != nil {
			data, _ := json.Marshal(mv.Message)
			c.SSEvent("message", string(data))
			c.Writer.Flush()
		}
	}
}
