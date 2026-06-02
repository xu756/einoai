package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
)

const defaultUseChatMessage = "帮我解释一下 React hooks"

type UseChatRequest struct {
	// AI SDK useChat 默认会传 messages
	Messages []UseChatMessage `json:"messages,omitempty"`

	// 兼容你原来的请求
	Message string `json:"message,omitempty"`

	// Model 先接收但不使用，后端保持默认
	Model string `json:"model,omitempty"`

	// 其他参数先透传占位，后面你自己接入 agent 包
	Params map[string]any `json:"params,omitempty"`
}

type UseChatMessage struct {
	ID      string         `json:"id,omitempty"`
	Role    string         `json:"role,omitempty"`
	Parts   []UseChatPart  `json:"parts,omitempty"`
	Content string         `json:"content,omitempty"`
	Meta    map[string]any `json:"metadata,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

type UseChatPart struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type useChatStreamState struct {
	messageID string

	textID      string
	textStarted bool

	reasoningID      string
	reasoningStarted bool

	stepStarted bool

	toolCalls         map[string]*toolCallState
	toolCallIndexToID map[int]string
}

type toolCallState struct {
	ID        string
	Name      string
	InputText strings.Builder
	Started   bool
	Available bool
}

func (h *Handler) ChatUseChatStream(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	req, ok := bindUseChatRequest(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	ag, err := h.AgentManager.NewChatModelAgent(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	message := extractUseChatLastUserText(req)
	if strings.TrimSpace(message) == "" {
		message = defaultUseChatMessage
	}

	runner := h.AgentManager.NewRunner(ctx, ag)

	// 参数先不处理，后面你可以把 req.Params / req.Messages 穿到 agent 包
	iter := runner.Query(ctx, message, adk.AgentRunOption{})

	streamAISDKDataProtocol(c, iter)
}

func (h *Handler) DeepChatUseChatStream(c *gin.Context) {
	if h.AgentManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent manager is not initialized"})
		return
	}

	req, ok := bindUseChatRequest(c)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	ag, err := h.AgentManager.NewDeepAgent(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	message := extractUseChatLastUserText(req)
	if strings.TrimSpace(message) == "" {
		message = defaultUseChatMessage
	}

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Query(ctx, message)

	streamAISDKDataProtocol(c, iter)
}

func bindUseChatRequest(c *gin.Context) (UseChatRequest, bool) {
	var req UseChatRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return req, false
	}

	return req, true
}

func extractUseChatLastUserText(req UseChatRequest) string {
	if strings.TrimSpace(req.Message) != "" {
		return req.Message
	}

	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}

		if strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}

		var sb strings.Builder
		for _, part := range msg.Parts {
			if part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}

		if strings.TrimSpace(sb.String()) != "" {
			return sb.String()
		}
	}

	return ""
}

func streamAISDKDataProtocol(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	// AI SDK Data Stream Protocol 必须有这个 header
	c.Writer.Header().Set("x-vercel-ai-ui-message-stream", "v1")

	state := &useChatStreamState{
		messageID:         fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		textID:            fmt.Sprintf("text_%d", time.Now().UnixNano()),
		reasoningID:       fmt.Sprintf("reasoning_%d", time.Now().UnixNano()),
		toolCalls:         make(map[string]*toolCallState),
		toolCallIndexToID: make(map[int]string),
	}

	writeAISDKPart(c, map[string]any{
		"type":      "start",
		"messageId": state.messageID,
	})

	writeAISDKPart(c, map[string]any{
		"type": "start-step",
	})
	state.stepStarted = true

	for {
		event, ok := iter.Next()
		if !ok {
			finishAISDKOpenBlocks(c, state)

			writeAISDKPart(c, map[string]any{
				"type": "finish-step",
			})

			writeAISDKPart(c, map[string]any{
				"type": "finish",
			})

			writeAISDKDone(c)
			return
		}

		if event == nil {
			continue
		}

		if event.Err != nil {
			writeAISDKPart(c, map[string]any{
				"type":      "error",
				"errorText": event.Err.Error(),
			})

			writeAISDKPart(c, map[string]any{
				"type": "finish",
			})

			writeAISDKDone(c)
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

					writeAISDKPart(c, map[string]any{
						"type":      "error",
						"errorText": err.Error(),
					})
					writeAISDKDone(c)
					return
				}

				if msg == nil {
					continue
				}

				writeEinoMessageAsAISDKParts(c, state, msg)
			}

			continue
		}

		if mv.Message != nil {
			writeEinoMessageAsAISDKParts(c, state, mv.Message)
		}
	}
}

func writeEinoMessageAsAISDKParts(c *gin.Context, state *useChatStreamState, msg *schema.Message) {
	if msg == nil {
		return
	}

	// 1. reasoning_content -> reasoning-start / reasoning-delta
	if msg.ReasoningContent != "" {
		if !state.reasoningStarted {
			writeAISDKPart(c, map[string]any{
				"type": "reasoning-start",
				"id":   state.reasoningID,
			})
			state.reasoningStarted = true
		}

		writeAISDKPart(c, map[string]any{
			"type":  "reasoning-delta",
			"id":    state.reasoningID,
			"delta": msg.ReasoningContent,
		})
	}

	// 2. content -> text-start / text-delta
	if msg.Content != "" && msg.Role != schema.Tool {
		if !state.textStarted {
			writeAISDKPart(c, map[string]any{
				"type": "text-start",
				"id":   state.textID,
			})
			state.textStarted = true
		}

		writeAISDKPart(c, map[string]any{
			"type":  "text-delta",
			"id":    state.textID,
			"delta": msg.Content,
		})
	}

	// 3. assistant tool_calls -> tool-input-start / tool-input-delta
	if len(msg.ToolCalls) > 0 {
		for i, tc := range msg.ToolCalls {
			index := i
			if tc.Index != nil {
				index = *tc.Index
			}

			callID := tc.ID

			// 关键修复：
			// 第一段有真实 ID 时，记录 index -> ID。
			if callID != "" {
				state.toolCallIndexToID[index] = callID
			}

			// 后续 arguments 分片 ID 为空时，用之前记录的真实 ID。
			if callID == "" {
				if savedID, ok := state.toolCallIndexToID[index]; ok && savedID != "" {
					callID = savedID
				}
			}

			// 兜底：实在没有真实 ID，才用 index fallback。
			if callID == "" {
				callID = fmt.Sprintf("tool_call_%d", index)
				state.toolCallIndexToID[index] = callID
			}

			st := state.toolCalls[callID]
			if st == nil {
				st = &toolCallState{
					ID: callID,
				}
				state.toolCalls[callID] = st
			}

			if tc.Function.Name != "" {
				st.Name = tc.Function.Name
			}
			if st.Name == "" {
				st.Name = "tool"
			}

			if !st.Started {
				writeAISDKPart(c, map[string]any{
					"type":       "tool-input-start",
					"toolCallId": st.ID,
					"toolName":   st.Name,
				})
				st.Started = true
			}

			if tc.Function.Arguments != "" {
				st.InputText.WriteString(tc.Function.Arguments)

				writeAISDKPart(c, map[string]any{
					"type":           "tool-input-delta",
					"toolCallId":     st.ID,
					"inputTextDelta": tc.Function.Arguments,
				})
			}
		}
	}

	// 4. tool message -> tool-output-available
	if msg.Role == schema.Tool {
		callID := msg.ToolCallID

		if callID == "" {
			callID = fmt.Sprintf("tool_call_%d", len(state.toolCalls))
		}

		st := state.toolCalls[callID]
		if st == nil {
			st = &toolCallState{
				ID:   callID,
				Name: msg.ToolName,
			}
			state.toolCalls[callID] = st
		}

		if msg.ToolName != "" {
			st.Name = msg.ToolName
		}
		if st.Name == "" {
			st.Name = "tool"
		}

		if !st.Available {
			writeToolInputAvailable(c, st)
		}

		writeAISDKPart(c, map[string]any{
			"type":       "tool-output-available",
			"toolCallId": st.ID,
			"output":     parseMaybeJSON(msg.Content),
		})
	}

	// 5. finish_reason / usage
	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.FinishReason == "tool_calls" {
			finishAISDKOpenBlocks(c, state)

			for _, st := range state.toolCalls {
				if st.Started && !st.Available {
					writeToolInputAvailable(c, st)
				}
			}

			writeAISDKPart(c, map[string]any{
				"type": "finish-step",
			})

			writeAISDKPart(c, map[string]any{
				"type": "start-step",
			})
			state.stepStarted = true

			// 工具调用后，下一轮模型输出重新开新的 text/reasoning block。
			state.textID = fmt.Sprintf("text_%d", time.Now().UnixNano())
			state.reasoningID = fmt.Sprintf("reasoning_%d", time.Now().UnixNano())
			state.textStarted = false
			state.reasoningStarted = false
		}

		if msg.ResponseMeta.Usage != nil || msg.ResponseMeta.FinishReason != "" {
			writeAISDKPart(c, map[string]any{
				"type": "data-usage",
				"data": map[string]any{
					"finishReason": msg.ResponseMeta.FinishReason,
					"usage":        convertEinoUsageToAISDKUsage(msg.ResponseMeta.Usage),
				},
			})
		}
	}
}

func finishAISDKOpenBlocks(c *gin.Context, state *useChatStreamState) {
	if state.reasoningStarted {
		writeAISDKPart(c, map[string]any{
			"type": "reasoning-end",
			"id":   state.reasoningID,
		})
		state.reasoningStarted = false
	}

	if state.textStarted {
		writeAISDKPart(c, map[string]any{
			"type": "text-end",
			"id":   state.textID,
		})
		state.textStarted = false
	}
}

func findOrCreateToolState(state *useChatStreamState, callID string, name string) *toolCallState {
	if callID == "" {
		callID = fmt.Sprintf("tool_call_%d", len(state.toolCalls))
	}

	st := state.toolCalls[callID]
	if st == nil {
		st = &toolCallState{
			ID: callID,
		}
		state.toolCalls[callID] = st
	}

	if name != "" {
		st.Name = name
	}
	if st.Name == "" {
		st.Name = "tool"
	}

	if !st.Started {
		st.Started = true
	}

	return st
}

func writeToolInputAvailable(c *gin.Context, st *toolCallState) {
	inputText := st.InputText.String()

	writeAISDKPart(c, map[string]any{
		"type":       "tool-input-available",
		"toolCallId": st.ID,
		"toolName":   st.Name,
		"input":      parseMaybeJSON(inputText),
	})

	st.Available = true
}

func parseMaybeJSON(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return map[string]any{}
	}

	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}

	return s
}

func convertEinoUsageToAISDKUsage(u *schema.TokenUsage) map[string]any {
	if u == nil {
		return nil
	}

	return map[string]any{
		"promptTokens":     u.PromptTokens,
		"completionTokens": u.CompletionTokens,
		"totalTokens":      u.TotalTokens,
		"promptTokenDetails": map[string]any{
			"cachedTokens": u.PromptTokenDetails.CachedTokens,
		},
		"completionTokenDetails": map[string]any{
			"reasoningTokens": u.CompletionTokensDetails.ReasoningTokens,
		},
	}
}

func writeAISDKPart(c *gin.Context, part map[string]any) {
	b, err := json.Marshal(part)
	if err != nil {
		return
	}

	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	c.Writer.Flush()
}

func writeAISDKDone(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}
