package aisdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/xu756/einoai"

	"github.com/cloudwego/eino/schema"
)

type toolState struct {
	id        string
	name      string
	inputText string
	available bool
	started   bool
	completed bool
}

type streamState struct {
	started           bool
	pendingStepFinish bool
	toolCalls         map[string]*toolState
	toolOrder         []string
	runErr            error
}

// FlushFunc flushes buffered stream data to the client.
type FlushFunc func()

type eventStreamWriter struct {
	writer io.Writer
	flush  FlushFunc
}

// SetEventStreamHeaders sets AI SDK UI Message Stream headers on an HTTP header map.
func SetEventStreamHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	header.Set("x-vercel-ai-ui-message-stream", "v1")
}

// WriteEventStreamTo writes einoai events as AI SDK UI Message Stream SSE.
//
// The returned messages are the complete Eino output messages for the run. They
// are kept out of the wire protocol so the caller can persist them directly.
func WriteEventStreamTo(ctx context.Context, writer io.Writer, flush FlushFunc, stream einoai.EventStream) ([]*schema.Message, error) {
	out := eventStreamWriter{writer: writer, flush: flush}
	state := newStreamState()
	var output []*schema.Message

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
		if !state.started {
			if err := out.writePart(ev.ID, map[string]any{"type": "start", "messageId": "msg_" + ev.RunID}); err != nil {
				return output, err
			}
			if err := out.writePart(ev.ID, map[string]any{"type": "start-step"}); err != nil {
				return output, err
			}
			state.started = true
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

func writeEvent(w eventStreamWriter, state *streamState, ev *einoai.RunEvent) (bool, error) {
	switch ev.Type {
	case einoai.EventTextStart:
		data, _ := einoai.DecodeEventData[einoai.TextData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "text-start", "id": data.ID})
	case einoai.EventTextDelta:
		data, _ := einoai.DecodeEventData[einoai.TextData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "text-delta", "id": data.ID, "delta": data.Delta})
	case einoai.EventTextEnd:
		data, _ := einoai.DecodeEventData[einoai.TextData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "text-end", "id": data.ID})
	case einoai.EventReasoningStart:
		data, _ := einoai.DecodeEventData[einoai.ReasoningData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "reasoning-start", "id": data.ID})
	case einoai.EventReasoningDelta:
		data, _ := einoai.DecodeEventData[einoai.ReasoningData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "reasoning-delta", "id": data.ID, "delta": data.Delta})
	case einoai.EventReasoningEnd:
		data, _ := einoai.DecodeEventData[einoai.ReasoningData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "reasoning-end", "id": data.ID})
	case einoai.EventToolCall:
		data, _ := einoai.DecodeEventData[einoai.ToolCallData](ev)
		return false, writeToolCall(w, state, ev.ID, data)
	case einoai.EventToolResult:
		data, _ := einoai.DecodeEventData[einoai.ToolResultData](ev)
		if err := writeToolResult(w, state, ev.ID, data); err != nil {
			return false, err
		}
		if state.pendingStepFinish && allToolOutputsCompleted(state) {
			if err := w.writePart(ev.ID, createFinishStepEvent()); err != nil {
				return false, err
			}
			if err := w.writePart(ev.ID, map[string]any{"type": "start-step"}); err != nil {
				return false, err
			}
			beginNextStep(state)
		}
		return false, nil
	case einoai.EventError:
		data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
		state.runErr = errors.New(data.Message)
		return false, w.writePart(ev.ID, map[string]any{"type": "error", "errorText": data.Message})
	case einoai.EventFinish:
		data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
		if data.FinishReason == "cancelled" || data.FinishReason == "canceled" {
			return true, w.writePart(ev.ID, map[string]any{"type": "abort", "reason": "cancelled"})
		}
		if data.FinishReason == "tool_calls" || data.FinishReason == "tool-calls" {
			if err := writePendingToolsAvailable(w, state, ev.ID); err != nil {
				return false, err
			}
			// The Eino agent executes these tools on the server. Keep the current
			// step open until all tool outputs have been streamed.
			state.pendingStepFinish = true
			return false, nil
		}
		if err := w.writePart(ev.ID, createFinishStepEvent()); err != nil {
			return false, err
		}
		if err := w.writePart(ev.ID, createMessageMetadataEvent()); err != nil {
			return false, err
		}
		if err := w.writePart(ev.ID, createFinishEvent(data)); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func writeToolCall(w eventStreamWriter, state *streamState, id string, data einoai.ToolCallData) error {
	if data.ID == "" || data.Name == "" {
		return nil
	}
	st := state.toolCalls[data.ID]
	if st == nil {
		st = &toolState{id: data.ID, name: data.Name}
		state.toolCalls[data.ID] = st
		state.toolOrder = append(state.toolOrder, data.ID)
	}
	if !st.started {
		if err := w.writePart(id, map[string]any{"type": "tool-input-start", "toolCallId": st.id, "toolName": st.name, "providerExecuted": true}); err != nil {
			return err
		}
		st.started = true
	}
	if data.Arguments != "" {
		st.inputText += data.Arguments
		if err := w.writePart(id, map[string]any{"type": "tool-input-delta", "toolCallId": st.id, "inputTextDelta": data.Arguments}); err != nil {
			return err
		}
	}
	return nil
}

func writeToolResult(w eventStreamWriter, state *streamState, id string, data einoai.ToolResultData) error {
	st := state.toolCalls[data.ToolCallID]
	if st == nil {
		st = &toolState{id: data.ToolCallID, name: data.Name}
		state.toolCalls[data.ToolCallID] = st
		state.toolOrder = append(state.toolOrder, data.ToolCallID)
	}
	if !st.available {
		if err := writeToolAvailable(w, id, st); err != nil {
			return err
		}
	}
	if err := w.writePart(id, map[string]any{"type": "tool-output-available", "toolCallId": st.id, "output": data.Content, "providerExecuted": true}); err != nil {
		return err
	}
	st.completed = true
	return nil
}

func writePendingToolsAvailable(w eventStreamWriter, state *streamState, id string) error {
	for _, toolCallID := range state.toolOrder {
		st := state.toolCalls[toolCallID]
		if st != nil && st.started && !st.available {
			if err := writeToolAvailable(w, id, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func allToolOutputsCompleted(state *streamState) bool {
	if state == nil || len(state.toolOrder) == 0 {
		return false
	}
	for _, toolCallID := range state.toolOrder {
		st := state.toolCalls[toolCallID]
		if st != nil && st.started && !st.completed {
			return false
		}
	}
	return true
}

func beginNextStep(state *streamState) {
	state.pendingStepFinish = false
	state.toolCalls = make(map[string]*toolState)
	state.toolOrder = nil
}

func newStreamState() *streamState {
	return &streamState{
		toolCalls: make(map[string]*toolState),
	}
}

func writeToolAvailable(w eventStreamWriter, id string, st *toolState) error {
	if err := w.writePart(id, map[string]any{
		"type":             "tool-input-available",
		"toolCallId":       st.id,
		"toolName":         st.name,
		"input":            parseMaybeJSON(st.inputText),
		"providerExecuted": true,
	}); err != nil {
		return err
	}
	st.available = true
	return nil
}

func (w eventStreamWriter) writeStreamError(err error) error {
	if err == nil {
		return nil
	}
	if writeErr := w.writePart("", map[string]any{"type": "error", "errorText": err.Error()}); writeErr != nil {
		return writeErr
	}
	return w.writeDone()
}

func createFinishEvent(data einoai.FinishData) map[string]any {
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

func createUsage(data einoai.FinishData) map[string]any {
	return usageMetadata(data.Usage)
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "", "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "tool-calls":
		return "tool-calls"
	case "content_filter", "content-filter":
		return "content-filter"
	case "error":
		return "error"
	default:
		return "other"
	}
}

func (w eventStreamWriter) writePart(id string, part map[string]any) error {
	b, err := json.Marshal(part)
	if err != nil {
		return err
	}
	_ = id // Internal Redis event IDs are not part of the AI SDK UI Message Stream wire format.
	if _, err := fmt.Fprintf(w.writer, "data: %s\n\n", b); err != nil {
		return err
	}
	w.flushNow()
	return nil
}

func (w eventStreamWriter) writeDone() error {
	if _, err := fmt.Fprint(w.writer, "data: [DONE]\n\n"); err != nil {
		return err
	}
	w.flushNow()
	return nil
}

func (w eventStreamWriter) flushNow() {
	if w.flush != nil {
		w.flush()
	}
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
