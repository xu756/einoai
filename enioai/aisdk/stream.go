package aisdk

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	enioai "enio-ai/enioai"

	"github.com/gin-gonic/gin"
)

type toolState struct {
	id        string
	name      string
	inputText string
	available bool
	started   bool
}

type streamState struct {
	started   bool
	toolCalls map[string]*toolState
}

// WriteEventStream writes enioai events as AI SDK Data Stream Protocol SSE.
func WriteEventStream(c *gin.Context, stream enioai.EventStream) {
	setStreamHeaders(c)
	state := &streamState{toolCalls: make(map[string]*toolState)}
	after := GetLastEventID(c)

	for {
		ev, err := stream.Next(c.Request.Context())
		if err == io.EOF {
			writeDone(c)
			return
		}
		if err != nil {
			WriteError(c, err)
			return
		}
		if ev == nil {
			continue
		}
		if !state.started && after == "" {
			writePart(c, ev.ID, map[string]any{"type": "start", "messageId": "msg_" + ev.RunID})
			writePart(c, ev.ID, map[string]any{"type": "start-step"})
			state.started = true
		}
		if writeEvent(c, state, ev) {
			writeDone(c)
			return
		}
	}
}

func writeEvent(c *gin.Context, state *streamState, ev *enioai.RunEvent) bool {
	switch ev.Type {
	case enioai.EventTextStart:
		data, _ := enioai.DecodeEventData[enioai.TextData](ev)
		writePart(c, ev.ID, map[string]any{"type": "text-start", "id": data.ID})
	case enioai.EventTextDelta:
		data, _ := enioai.DecodeEventData[enioai.TextData](ev)
		writePart(c, ev.ID, map[string]any{"type": "text-delta", "id": data.ID, "delta": data.Delta})
	case enioai.EventTextEnd:
		data, _ := enioai.DecodeEventData[enioai.TextData](ev)
		writePart(c, ev.ID, map[string]any{"type": "text-end", "id": data.ID})
	case enioai.EventReasoningStart:
		data, _ := enioai.DecodeEventData[enioai.ReasoningData](ev)
		writePart(c, ev.ID, map[string]any{"type": "reasoning-start", "id": data.ID})
	case enioai.EventReasoningDelta:
		data, _ := enioai.DecodeEventData[enioai.ReasoningData](ev)
		writePart(c, ev.ID, map[string]any{"type": "reasoning-delta", "id": data.ID, "delta": data.Delta})
	case enioai.EventReasoningEnd:
		data, _ := enioai.DecodeEventData[enioai.ReasoningData](ev)
		writePart(c, ev.ID, map[string]any{"type": "reasoning-end", "id": data.ID})
	case enioai.EventToolCall:
		data, _ := enioai.DecodeEventData[enioai.ToolCallData](ev)
		writeToolCall(c, state, ev.ID, data)
	case enioai.EventToolResult:
		data, _ := enioai.DecodeEventData[enioai.ToolResultData](ev)
		writeToolResult(c, state, ev.ID, data)
	case enioai.EventError:
		data, _ := enioai.DecodeEventData[enioai.ErrorData](ev)
		writePart(c, ev.ID, map[string]any{"type": "error", "errorText": data.Message})
	case enioai.EventFinish:
		data, _ := enioai.DecodeEventData[enioai.FinishData](ev)
		if data.FinishReason == "tool_calls" {
			writePendingToolsAvailable(c, state, ev.ID)
			writePart(c, ev.ID, createFinishStepEvent())
			writePart(c, ev.ID, map[string]any{"type": "start-step"})
			return false
		}
		writePart(c, ev.ID, createFinishStepEvent())
		writePart(c, ev.ID, createMessageMetadataEvent())
		writePart(c, ev.ID, createFinishEvent(data))
		return true
	}
	return false
}

func writeToolCall(c *gin.Context, state *streamState, id string, data enioai.ToolCallData) {
	callID := data.ID
	if callID == "" {
		callID = fmt.Sprintf("tool_call_%d", data.Index)
	}
	st := state.toolCalls[callID]
	if st == nil {
		st = &toolState{id: callID}
		state.toolCalls[callID] = st
	}
	if data.Name != "" {
		st.name = data.Name
	}
	if st.name == "" {
		st.name = "tool"
	}
	if !st.started {
		writePart(c, id, map[string]any{"type": "tool-input-start", "toolCallId": st.id, "toolName": st.name})
		st.started = true
	}
	if data.Arguments != "" {
		st.inputText += data.Arguments
		writePart(c, id, map[string]any{"type": "tool-input-delta", "toolCallId": st.id, "inputTextDelta": data.Arguments})
	}
}

func writeToolResult(c *gin.Context, state *streamState, id string, data enioai.ToolResultData) {
	st := state.toolCalls[data.ToolCallID]
	if st == nil {
		st = &toolState{id: data.ToolCallID, name: data.Name}
		state.toolCalls[data.ToolCallID] = st
	}
	if st.name == "" {
		st.name = "tool"
	}
	if !st.available {
		writeToolAvailable(c, id, st)
	}
	writePart(c, id, map[string]any{"type": "tool-output-available", "toolCallId": st.id, "output": data.Content})
}

func writePendingToolsAvailable(c *gin.Context, state *streamState, id string) {
	for _, st := range state.toolCalls {
		if st.started && !st.available {
			writeToolAvailable(c, id, st)
		}
	}
}

func writeToolAvailable(c *gin.Context, id string, st *toolState) {
	writePart(c, id, map[string]any{
		"type":       "tool-input-available",
		"toolCallId": st.id,
		"toolName":   st.name,
		"input":      parseMaybeJSON(st.inputText),
	})
	st.available = true
}

func createFinishEvent(data enioai.FinishData) map[string]any {
	reason := normalizeFinishReason(data.FinishReason)
	if reason == "" {
		reason = "stop"
	}
	res := map[string]any{
		"type":         "finish",
		"finishReason": reason,
	}
	if data.Usage != nil {
		u := createUsage(data)
		res["messageMetadata"] = map[string]any{"custom": map[string]any{"usage": u}}
	}
	return res
}

func createFinishStepEvent() map[string]any {
	return map[string]any{"type": "finish-step"}
}

func createMessageMetadataEvent() map[string]any {
	metadata := map[string]any{}
	if modelID := os.Getenv("MODEL_NAME"); modelID != "" {
		metadata["modelId"] = modelID
	}
	return map[string]any{
		"type":            "message-metadata",
		"messageMetadata": metadata,
	}
}

func createUsage(data enioai.FinishData) map[string]any {
	return map[string]any{
		"inputTokens":       data.Usage.PromptTokens,
		"outputTokens":      data.Usage.CompletionTokens,
		"totalTokens":       data.Usage.TotalTokens,
		"cachedInputTokens": data.Usage.PromptTokenDetails.CachedTokens,
		"inputTokenDetails": map[string]any{
			"cacheReadTokens": data.Usage.PromptTokenDetails.CachedTokens,
			"noCacheTokens":   data.Usage.PromptTokens - data.Usage.PromptTokenDetails.CachedTokens,
		},
		"outputTokenDetails": map[string]any{
			"textTokens":      data.Usage.CompletionTokens - data.Usage.CompletionTokensDetails.ReasoningTokens,
			"reasoningTokens": data.Usage.CompletionTokensDetails.ReasoningTokens,
		},
		"reasoningTokens": data.Usage.CompletionTokensDetails.ReasoningTokens,
	}
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool-calls"
	case "content_filter":
		return "content-filter"
	default:
		return reason
	}
}

func setStreamHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("x-vercel-ai-ui-message-stream", "v1")
}

func writePart(c *gin.Context, id string, part map[string]any) {
	b, err := json.Marshal(part)
	if err != nil {
		return
	}
	if id != "" {
		_, _ = fmt.Fprintf(c.Writer, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
	c.Writer.Flush()
}

func writeDone(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	c.Writer.Flush()
}

func parseMaybeJSON(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	if s == "" {
		return map[string]any{}
	}
	return s
}
