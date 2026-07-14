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

// WriteEventStreamTo writes einoai events as AI SDK SSE to any writer.
func WriteEventStreamTo(ctx context.Context, writer io.Writer, flush FlushFunc, stream einoai.EventStream) error {
	out := eventStreamWriter{writer: writer, flush: flush}
	state := newStreamState()

	for {
		ev, err := stream.Next(ctx)
		if err == io.EOF {
			return out.writeDone()
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if err != nil {
			_ = out.writeStreamError(err)
			return err
		}
		if ev == nil {
			continue
		}
		if !state.started {
			if err := out.writePart(ev.ID, map[string]any{"type": "start", "messageId": "msg_" + ev.RunID}); err != nil {
				return err
			}
			if err := out.writePart(ev.ID, map[string]any{"type": "start-step"}); err != nil {
				return err
			}
			state.started = true
		}
		done, err := writeEvent(out, state, ev)
		if err != nil {
			return err
		}
		if done {
			return out.writeDone()
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
		return false, writeToolResult(w, state, ev.ID, data)
	case einoai.EventError:
		data, _ := einoai.DecodeEventData[einoai.ErrorData](ev)
		return false, w.writePart(ev.ID, map[string]any{"type": "error", "errorText": data.Message})
	case einoai.EventFinish:
		data, _ := einoai.DecodeEventData[einoai.FinishData](ev)
		if data.FinishReason == "tool_calls" {
			if err := writePendingToolsAvailable(w, state, ev.ID); err != nil {
				return false, err
			}
			if err := w.writePart(ev.ID, createFinishStepEvent()); err != nil {
				return false, err
			}
			if err := w.writePart(ev.ID, map[string]any{"type": "start-step"}); err != nil {
				return false, err
			}
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
	}
	if !st.started {
		if err := w.writePart(id, map[string]any{"type": "tool-input-start", "toolCallId": st.id, "toolName": st.name}); err != nil {
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
	}
	if !st.available {
		if err := writeToolAvailable(w, id, st); err != nil {
			return err
		}
	}
	return w.writePart(id, map[string]any{"type": "tool-output-available", "toolCallId": st.id, "output": data.Content})
}

func writePendingToolsAvailable(w eventStreamWriter, state *streamState, id string) error {
	for _, st := range state.toolCalls {
		if st.started && !st.available {
			if err := writeToolAvailable(w, id, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func newStreamState() *streamState {
	return &streamState{
		toolCalls: make(map[string]*toolState),
	}
}

func writeToolAvailable(w eventStreamWriter, id string, st *toolState) error {
	if err := w.writePart(id, map[string]any{
		"type":       "tool-input-available",
		"toolCallId": st.id,
		"toolName":   st.name,
		"input":      parseMaybeJSON(st.inputText),
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
	case "tool_calls":
		return "tool-calls"
	case "content_filter":
		return "content-filter"
	default:
		return reason
	}
}

func (w eventStreamWriter) writePart(id string, part map[string]any) error {
	b, err := json.Marshal(part)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(w.writer, "id: %s\n", id); err != nil {
			return err
		}
	}
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
