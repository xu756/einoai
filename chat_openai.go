package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
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

type OpenAISink interface {
	WriteJSON(ctx context.Context, v any) error
	WriteData(ctx context.Context, data string) error
}

type RedisOpenAISink struct {
	store     *RunStore
	sessionID string
	runID     string
}

func (s *RedisOpenAISink) WriteJSON(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.WriteData(ctx, string(b))
}

func (s *RedisOpenAISink) WriteData(ctx context.Context, data string) error {
	_, err := s.store.Append(ctx, s.sessionID, s.runID, data)
	return err
}

func streamOpenAICompatibleToSink(
	ctx context.Context,
	sink OpenAISink,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	modelName string,
) error {
	id := "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano())
	created := time.Now().Unix()

	for {
		event, ok := iter.Next()
		if !ok {
			return sink.WriteData(ctx, "[DONE]")
		}

		if event == nil {
			continue
		}

		if event.Err != nil {
			errObj := map[string]any{
				"error": map[string]any{
					"message": event.Err.Error(),
					"type":    "server_error",
				},
			}
			_ = sink.WriteJSON(ctx, errObj)
			_ = sink.WriteData(ctx, "[DONE]")
			return event.Err
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
					_ = sink.WriteJSON(ctx, errObj)
					_ = sink.WriteData(ctx, "[DONE]")
					return err
				}

				if msg == nil {
					continue
				}

				if err := writeEinoMessageAsOpenAIChunkToSink(ctx, sink, id, created, modelName, msg); err != nil {
					return err
				}
			}

			continue
		}

		if mv.Message != nil {
			if err := writeEinoMessageAsOpenAIChunkToSink(ctx, sink, id, created, modelName, mv.Message); err != nil {
				return err
			}
		}
	}
}

func writeEinoMessageAsOpenAIChunkToSink(
	ctx context.Context,
	sink OpenAISink,
	id string,
	created int64,
	modelName string,
	msg *schema.Message,
) error {
	delta := OpenAIDelta{}

	if msg.Role != "" {
		delta.Role = string(msg.Role)
	}

	if msg.Content != "" {
		delta.Content = msg.Content
	}

	if msg.ReasoningContent != "" {
		delta.ReasoningContent = msg.ReasoningContent
	}

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

	if msg.Role == schema.Tool {
		delta.ToolResult = &OpenAIToolResultDelta{
			ToolCallID: msg.ToolCallID,
			Name:       msg.ToolName,
			Content:    msg.Content,
		}
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

	return sink.WriteJSON(ctx, chunk)
}
