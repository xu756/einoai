package main

import (
	"context"
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
	Messages []UseChatMessage `json:"messages,omitempty"`
	Message  string           `json:"message,omitempty"`
	Model    string           `json:"model,omitempty"`
	Params   map[string]any   `json:"params,omitempty"`
}

type UseChatMessage struct {
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
	messageID         string
	textID            string
	textStarted       bool
	reasoningID       string
	reasoningStarted  bool
	stepStarted       bool
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

// AISDKSink writes AISDK format events to a destination.
type AISDKSink interface {
	WritePart(part map[string]any)
	Done()
}

// RedisAISDKSink writes AISDK events to Redis Stream.
type RedisAISDKSink struct {
	store     *RunStore
	sessionID string
	runID     string
}

func (s *RedisAISDKSink) WritePart(part map[string]any) {
	b, _ := json.Marshal(part)
	_, _ = s.store.Append(context.Background(), s.sessionID, s.runID, string(b))
}

func (s *RedisAISDKSink) Done() {
	_, _ = s.store.Append(context.Background(), s.sessionID, s.runID, "[DONE]")
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
	iter := runner.Query(ctx, message, adk.AgentRunOption{})

	streamAISDKDataProtocolWithSink(c, iter, nil)
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

	streamAISDKDataProtocolWithSink(c, iter, nil)
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

// streamAISDKDataProtocol writes AISDK events directly to HTTP SSE response (no Redis).
func streamAISDKDataProtocol(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	streamAISDKDataProtocolWithSink(c, iter, nil)
}

// streamAISDKDataProtocolWithSink streams AISDK Data Stream Protocol events.
// If sink is non-nil, also writes each event to sink (for Redis persistence).
func streamAISDKDataProtocolWithSink(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent], sink AISDKSink) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("x-vercel-ai-ui-message-stream", "v1")

	state := &useChatStreamState{
		messageID:         fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		textID:            fmt.Sprintf("text_%d", time.Now().UnixNano()),
		reasoningID:       fmt.Sprintf("reasoning_%d", time.Now().UnixNano()),
		toolCalls:         make(map[string]*toolCallState),
		toolCallIndexToID: make(map[int]string),
	}

	writePart(c, sink, map[string]any{"type": "start", "messageId": state.messageID})
	writePart(c, sink, map[string]any{"type": "start-step"})
	state.stepStarted = true

	for {
		event, ok := iter.Next()
		if !ok {
			finishOpenBlocks(c, sink, state)
			writePart(c, sink, map[string]any{"type": "finish-step"})
			writePart(c, sink, map[string]any{"type": "finish"})
			writeDone(c, sink)
			return
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			writePart(c, sink, map[string]any{"type": "error", "errorText": event.Err.Error()})
			writePart(c, sink, map[string]any{"type": "finish"})
			writeDone(c, sink)
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
					writePart(c, sink, map[string]any{"type": "error", "errorText": err.Error()})
					writeDone(c, sink)
					return
				}
				if msg == nil {
					continue
				}
				writeEinoMsgAsAISDKParts(c, sink, state, msg)
			}
			continue
		}
		if mv.Message != nil {
			writeEinoMsgAsAISDKParts(c, sink, state, mv.Message)
		}
	}
}

func writeEinoMsgAsAISDKParts(c *gin.Context, sink AISDKSink, state *useChatStreamState, msg *schema.Message) {
	if msg == nil {
		return
	}
	if msg.ReasoningContent != "" {
		if !state.reasoningStarted {
			writePart(c, sink, map[string]any{"type": "reasoning-start", "id": state.reasoningID})
			state.reasoningStarted = true
		}
		writePart(c, sink, map[string]any{"type": "reasoning-delta", "id": state.reasoningID, "delta": msg.ReasoningContent})
	}
	if msg.Content != "" && msg.Role != schema.Tool {
		if !state.textStarted {
			writePart(c, sink, map[string]any{"type": "text-start", "id": state.textID})
			state.textStarted = true
		}
		writePart(c, sink, map[string]any{"type": "text-delta", "id": state.textID, "delta": msg.Content})
	}
	if len(msg.ToolCalls) > 0 {
		for i, tc := range msg.ToolCalls {
			index := i
			if tc.Index != nil {
				index = *tc.Index
			}
			callID := tc.ID
			if callID != "" {
				state.toolCallIndexToID[index] = callID
			}
			if callID == "" {
				if savedID, ok := state.toolCallIndexToID[index]; ok && savedID != "" {
					callID = savedID
				}
			}
			if callID == "" {
				callID = fmt.Sprintf("tool_call_%d", index)
				state.toolCallIndexToID[index] = callID
			}
			st := state.toolCalls[callID]
			if st == nil {
				st = &toolCallState{ID: callID}
				state.toolCalls[callID] = st
			}
			if tc.Function.Name != "" {
				st.Name = tc.Function.Name
			}
			if st.Name == "" {
				st.Name = "tool"
			}
			if !st.Started {
				writePart(c, sink, map[string]any{"type": "tool-input-start", "toolCallId": st.ID, "toolName": st.Name})
				st.Started = true
			}
			if tc.Function.Arguments != "" {
				st.InputText.WriteString(tc.Function.Arguments)
				writePart(c, sink, map[string]any{"type": "tool-input-delta", "toolCallId": st.ID, "inputTextDelta": tc.Function.Arguments})
			}
		}
	}
	if msg.Role == schema.Tool {
		callID := msg.ToolCallID
		if callID == "" {
			callID = fmt.Sprintf("tool_call_%d", len(state.toolCalls))
		}
		st := state.toolCalls[callID]
		if st == nil {
			st = &toolCallState{ID: callID, Name: msg.ToolName}
			state.toolCalls[callID] = st
		}
		if msg.ToolName != "" {
			st.Name = msg.ToolName
		}
		if st.Name == "" {
			st.Name = "tool"
		}
		if !st.Available {
			writeToolAvailable(c, sink, st)
		}
		writePart(c, sink, map[string]any{"type": "tool-output-available", "toolCallId": st.ID, "output": parseMaybeJSON(msg.Content)})
	}
	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.FinishReason == "tool_calls" {
			finishOpenBlocks(c, sink, state)
			for _, st := range state.toolCalls {
				if st.Started && !st.Available {
					writeToolAvailable(c, sink, st)
				}
			}
			writePart(c, sink, map[string]any{"type": "finish-step"})
			writePart(c, sink, map[string]any{"type": "start-step"})
			state.stepStarted = true
			state.textID = fmt.Sprintf("text_%d", time.Now().UnixNano())
			state.reasoningID = fmt.Sprintf("reasoning_%d", time.Now().UnixNano())
			state.textStarted = false
			state.reasoningStarted = false
		}
		if msg.ResponseMeta.Usage != nil || msg.ResponseMeta.FinishReason != "" {
			writePart(c, sink, map[string]any{
				"type": "data-usage",
				"data": map[string]any{
					"finishReason": msg.ResponseMeta.FinishReason,
					"usage":        convertEinoUsageToAISDKUsage(msg.ResponseMeta.Usage),
				},
			})
		}
	}
}

func finishOpenBlocks(c *gin.Context, sink AISDKSink, state *useChatStreamState) {
	if state.reasoningStarted {
		writePart(c, sink, map[string]any{"type": "reasoning-end", "id": state.reasoningID})
		state.reasoningStarted = false
	}
	if state.textStarted {
		writePart(c, sink, map[string]any{"type": "text-end", "id": state.textID})
		state.textStarted = false
	}
}

func writeToolAvailable(c *gin.Context, sink AISDKSink, st *toolCallState) {
	inputText := st.InputText.String()
	writePart(c, sink, map[string]any{
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

// writePart writes a single AISDK event to both the sink and the SSE response.
func writePart(c *gin.Context, sink AISDKSink, part map[string]any) {
	b, err := json.Marshal(part)
	if err != nil {
		return
	}
	if sink != nil {
		sink.WritePart(part)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	c.Writer.Flush()
}

func writeDone(c *gin.Context, sink AISDKSink) {
	if sink != nil {
		sink.Done()
	}
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}
