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
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Filename  string `json:"filename,omitempty"`
}

type useChatStreamState struct {
	messageID         string
	baseID            string
	stepIndex         int
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

func newUseChatStreamState(baseID string) *useChatStreamState {
	if baseID == "" {
		baseID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	state := &useChatStreamState{
		messageID:         "msg_" + baseID,
		baseID:            baseID,
		toolCalls:         make(map[string]*toolCallState),
		toolCallIndexToID: make(map[int]string),
	}
	resetUseChatStepIDs(state)
	return state
}

func advanceUseChatStep(state *useChatStreamState) {
	state.stepIndex++
	resetUseChatStepIDs(state)
	state.textStarted = false
	state.reasoningStarted = false
}

func resetUseChatStepIDs(state *useChatStreamState) {
	state.textID = fmt.Sprintf("text_%s_%d", state.baseID, state.stepIndex)
	state.reasoningID = fmt.Sprintf("reasoning_%s_%d", state.baseID, state.stepIndex)
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

	messages := extractUseChatMessages(req)

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Run(ctx, messages, adk.AgentRunOption{})

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

	messages := extractUseChatMessages(req)

	runner := h.AgentManager.NewRunner(ctx, ag)
	iter := runner.Run(ctx, messages)

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

func extractUseChatMessages(req UseChatRequest) []*schema.Message {
	var msgs []*schema.Message
	for _, m := range req.Messages {
		var role schema.RoleType
		switch m.Role {
		case "assistant":
			role = schema.Assistant
		case "system":
			role = schema.System
		case "tool":
			role = schema.Tool
		default:
			role = schema.User
		}

		var multiParts []schema.MessageInputPart
		hasTextPart := false

		for _, part := range m.Parts {
			switch part.Type {
			case "text":
				hasTextPart = true
				multiParts = append(multiParts, schema.MessageInputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: part.Text,
				})
			case "file", "image":
				var partType schema.ChatMessagePartType
				if strings.HasPrefix(part.MediaType, "image/") {
					partType = schema.ChatMessagePartType("image_url")
				} else if strings.HasPrefix(part.MediaType, "audio/") {
					partType = schema.ChatMessagePartType("audio_url")
				} else if strings.HasPrefix(part.MediaType, "video/") {
					partType = schema.ChatMessagePartType("video_url")
				} else {
					partType = schema.ChatMessagePartType("file_url")
				}

				urlStr := part.URL
				var base64Data *string
				mimeType := part.MediaType

				if strings.HasPrefix(urlStr, "data:") {
					commaIdx := strings.Index(urlStr, ",")
					if commaIdx != -1 {
						b64 := urlStr[commaIdx+1:]
						base64Data = &b64
						meta := urlStr[5:commaIdx]
						semiIdx := strings.Index(meta, ";")
						if semiIdx != -1 {
							mimeType = meta[:semiIdx]
						} else {
							mimeType = meta
						}
						urlStr = ""
					}
				}

				common := schema.MessagePartCommon{
					MIMEType: mimeType,
				}
				if urlStr != "" {
					common.URL = &urlStr
				}
				if base64Data != nil {
					common.Base64Data = base64Data
				}

				var inputPart schema.MessageInputPart
				inputPart.Type = partType
				if partType == schema.ChatMessagePartType("image_url") {
					inputPart.Image = &schema.MessageInputImage{
						MessagePartCommon: common,
					}
				} else if partType == schema.ChatMessagePartType("audio_url") {
					inputPart.Audio = &schema.MessageInputAudio{
						MessagePartCommon: common,
					}
				} else if partType == schema.ChatMessagePartType("video_url") {
					inputPart.Video = &schema.MessageInputVideo{
						MessagePartCommon: common,
					}
				} else {
					inputPart.File = &schema.MessageInputFile{
						MessagePartCommon: common,
						Name:              part.Filename,
					}
				}
				multiParts = append(multiParts, inputPart)
			}
		}

		msg := &schema.Message{
			Role: role,
		}

		if len(multiParts) > 0 && (role == schema.User || role == schema.Tool) {
			if m.Content != "" && !hasTextPart {
				multiParts = append([]schema.MessageInputPart{{
					Type: schema.ChatMessagePartTypeText,
					Text: m.Content,
				}}, multiParts...)
			}
			msg.UserInputMultiContent = multiParts
			msg.Content = m.Content
		} else if len(multiParts) > 0 && role == schema.Assistant {
			var outParts []schema.MessageOutputPart
			if m.Content != "" && !hasTextPart {
				outParts = append(outParts, schema.MessageOutputPart{
					Type: schema.ChatMessagePartTypeText,
					Text: m.Content,
				})
			}
			for _, p := range multiParts {
				outPart := schema.MessageOutputPart{
					Type: p.Type,
					Text: p.Text,
				}
				if p.Image != nil {
					outPart.Image = &schema.MessageOutputImage{
						MessagePartCommon: p.Image.MessagePartCommon,
					}
				} else if p.Audio != nil {
					outPart.Audio = &schema.MessageOutputAudio{
						MessagePartCommon: p.Audio.MessagePartCommon,
					}
				} else if p.Video != nil {
					outPart.Video = &schema.MessageOutputVideo{
						MessagePartCommon: p.Video.MessagePartCommon,
					}
				}
				outParts = append(outParts, outPart)
			}
			msg.AssistantGenMultiContent = outParts
			msg.Content = m.Content
		} else {
			if m.Content == "" && hasTextPart {
				var sb strings.Builder
				for _, p := range m.Parts {
					if p.Type == "text" {
						sb.WriteString(p.Text)
					}
				}
				msg.Content = sb.String()
			} else {
				msg.Content = m.Content
			}
		}

		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 && req.Message != "" {
		msgs = append(msgs, &schema.Message{
			Role:    schema.User,
			Content: req.Message,
		})
	}

	if len(msgs) == 0 {
		msgs = append(msgs, &schema.Message{
			Role:    schema.User,
			Content: defaultUseChatMessage,
		})
	}
	return msgs
}

// streamAISDKDataProtocol writes AISDK events directly to HTTP SSE response (no Redis).
func streamAISDKDataProtocol(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	streamAISDKDataProtocolWithSink(c, iter, nil)
}

func createAISDKFinishEvent(finishReason string, usage *schema.TokenUsage) map[string]any {
	if finishReason == "" {
		finishReason = "stop"
	}

	res := map[string]any{
		"type":         "finish",
		"finishReason": finishReason,
	}

	if usage != nil {
		// Construct the specific structure requested by user
		u := map[string]any{
			"inputTokens":       usage.PromptTokens,
			"outputTokens":      usage.CompletionTokens,
			"totalTokens":       usage.TotalTokens,
			"cachedInputTokens": usage.PromptTokenDetails.CachedTokens,
		}

		// Add details if available
		inputDetails := map[string]any{
			"cacheReadTokens": usage.PromptTokenDetails.CachedTokens,
			"noCacheTokens":   usage.PromptTokens - usage.PromptTokenDetails.CachedTokens,
		}
		u["inputTokenDetails"] = inputDetails

		outputDetails := map[string]any{
			"textTokens":      usage.CompletionTokens - usage.CompletionTokensDetails.ReasoningTokens,
			"reasoningTokens": usage.CompletionTokensDetails.ReasoningTokens,
		}
		u["outputTokenDetails"] = outputDetails
		u["reasoningTokens"] = usage.CompletionTokensDetails.ReasoningTokens

		res["messageMetadata"] = map[string]any{
			"custom": map[string]any{
				"usage": u,
			},
		}
	}

	return res
}

// streamAISDKDataProtocolWithSink streams AISDK Data Stream Protocol events.
// If sink is non-nil, also writes each event to sink (for Redis persistence).
func streamAISDKDataProtocolWithSink(c *gin.Context, iter *adk.AsyncIterator[*adk.AgentEvent], sink AISDKSink) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("x-vercel-ai-ui-message-stream", "v1")

	state := newUseChatStreamState("")

	writePart(c, sink, map[string]any{"type": "start", "messageId": state.messageID})
	writePart(c, sink, map[string]any{"type": "start-step"})
	state.stepStarted = true

	var lastUsage *schema.TokenUsage
	var lastFinishReason string

	for {
		event, ok := iter.Next()
		if !ok {
			finishOpenBlocks(c, sink, state)
			writePart(c, sink, map[string]any{"type": "finish-step"})
			writePart(c, sink, createAISDKFinishEvent(lastFinishReason, lastUsage))
			writeDone(c, sink)
			return
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			writePart(c, sink, map[string]any{"type": "error", "errorText": event.Err.Error()})
			writePart(c, sink, createAISDKFinishEvent("error", lastUsage))
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
				if msg.ResponseMeta != nil {
					if msg.ResponseMeta.Usage != nil {
						lastUsage = msg.ResponseMeta.Usage
					}
					if msg.ResponseMeta.FinishReason != "" {
						lastFinishReason = msg.ResponseMeta.FinishReason
					}
				}
				writeEinoMsgAsAISDKParts(c, sink, state, msg)
			}
			continue
		}
		if mv.Message != nil {
			if mv.Message.ResponseMeta != nil {
				if mv.Message.ResponseMeta.Usage != nil {
					lastUsage = mv.Message.ResponseMeta.Usage
				}
				if mv.Message.ResponseMeta.FinishReason != "" {
					lastFinishReason = mv.Message.ResponseMeta.FinishReason
				}
			}
			writeEinoMsgAsAISDKParts(c, sink, state, mv.Message)
		}
	}
}

func writeEinoMsgAsAISDKParts(c *gin.Context, sink AISDKSink, state *useChatStreamState, msg *schema.Message) {
	writeEinoMsgAsAISDKPartsWithID(c, sink, state, "", msg)
}

func writeEinoMsgAsAISDKPartsWithID(c *gin.Context, sink AISDKSink, state *useChatStreamState, id string, msg *schema.Message) {
	if msg == nil {
		return
	}
	if msg.ReasoningContent != "" {
		if !state.reasoningStarted {
			writePartWithID(c, sink, id, map[string]any{"type": "reasoning-start", "id": state.reasoningID})
			state.reasoningStarted = true
		}
		writePartWithID(c, sink, id, map[string]any{"type": "reasoning-delta", "id": state.reasoningID, "delta": msg.ReasoningContent})
	}
	if msg.Content != "" && msg.Role != schema.Tool {
		if !state.textStarted {
			writePartWithID(c, sink, id, map[string]any{"type": "text-start", "id": state.textID})
			state.textStarted = true
		}
		writePartWithID(c, sink, id, map[string]any{"type": "text-delta", "id": state.textID, "delta": msg.Content})
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
				writePartWithID(c, sink, id, map[string]any{"type": "tool-input-start", "toolCallId": st.ID, "toolName": st.Name})
				st.Started = true
			}
			if tc.Function.Arguments != "" {
				st.InputText.WriteString(tc.Function.Arguments)
				writePartWithID(c, sink, id, map[string]any{"type": "tool-input-delta", "toolCallId": st.ID, "inputTextDelta": tc.Function.Arguments})
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
			writeToolAvailableWithID(c, sink, id, st)
		}
		writePartWithID(c, sink, id, map[string]any{"type": "tool-output-available", "toolCallId": st.ID, "output": parseMaybeJSON(msg.Content)})
	}
	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.FinishReason == "tool_calls" {
			finishOpenBlocksWithID(c, sink, id, state)
			for _, st := range state.toolCalls {
				if st.Started && !st.Available {
					writeToolAvailableWithID(c, sink, id, st)
				}
			}
			writePartWithID(c, sink, id, map[string]any{"type": "finish-step"})
			writePartWithID(c, sink, id, map[string]any{"type": "start-step"})
			state.stepStarted = true
			advanceUseChatStep(state)
		}
		if msg.ResponseMeta.Usage != nil || msg.ResponseMeta.FinishReason != "" {
			writePartWithID(c, sink, id, map[string]any{
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
	finishOpenBlocksWithID(c, sink, "", state)
}

func finishOpenBlocksWithID(c *gin.Context, sink AISDKSink, id string, state *useChatStreamState) {
	if state.reasoningStarted {
		writePartWithID(c, sink, id, map[string]any{"type": "reasoning-end", "id": state.reasoningID})
		state.reasoningStarted = false
	}
	if state.textStarted {
		writePartWithID(c, sink, id, map[string]any{"type": "text-end", "id": state.textID})
		state.textStarted = false
	}
}

func writeToolAvailable(c *gin.Context, sink AISDKSink, st *toolCallState) {
	writeToolAvailableWithID(c, sink, "", st)
}

func writeToolAvailableWithID(c *gin.Context, sink AISDKSink, id string, st *toolCallState) {
	inputText := st.InputText.String()
	writePartWithID(c, sink, id, map[string]any{
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
	writePartWithID(c, sink, "", part)
}

func writePartWithID(c *gin.Context, sink AISDKSink, id string, part map[string]any) {
	b, err := json.Marshal(part)
	if err != nil {
		return
	}
	if sink != nil {
		sink.WritePart(part)
	}
	if c == nil {
		return
	}
	if id != "" {
		_, _ = fmt.Fprintf(c.Writer, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	c.Writer.Flush()
}

func writeDone(c *gin.Context, sink AISDKSink) {
	if sink != nil {
		sink.Done()
	}
	if c == nil {
		return
	}
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}
