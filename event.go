package einoai

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/schema"
)

// EventType is the internal protocol-independent run event type.
type EventType string

const (
	EventRunCreated     EventType = "run_created"
	EventRunStarted     EventType = "run_started"
	EventTextStart      EventType = "text_start"
	EventTextDelta      EventType = "text_delta"
	EventTextEnd        EventType = "text_end"
	EventReasoningStart EventType = "reasoning_start"
	EventReasoningDelta EventType = "reasoning_delta"
	EventReasoningEnd   EventType = "reasoning_end"
	EventToolCall       EventType = "tool_call"
	EventToolResult     EventType = "tool_result"
	EventError          EventType = "error"
	EventFinish         EventType = "finish"
)

// RunEvent is the internal event persisted in Redis and consumed by protocols.
type RunEvent struct {
	ID        string
	SessionID string
	RunID     string
	Type      EventType
	Data      any
	CreatedAt time.Time
}

// EventStream is a pull-based stream over run events.
type EventStream interface {
	Next(ctx context.Context) (*RunEvent, error)
	Close() error
}

// TextData carries text block ids and deltas.
type TextData struct {
	ID    string `json:"id,omitempty"`
	Delta string `json:"delta,omitempty"`
}

// ReasoningData carries reasoning block ids and deltas.
type ReasoningData struct {
	ID    string `json:"id,omitempty"`
	Delta string `json:"delta,omitempty"`
}

// ToolCallData carries streamed tool-call input.
type ToolCallData struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Index     int    `json:"index,omitempty"`
}

// ToolResultData carries a completed tool output.
type ToolResultData struct {
	ToolCallID string `json:"toolCallId,omitempty"`
	Name       string `json:"name,omitempty"`
	Content    any    `json:"content,omitempty"`
}

// ErrorData carries a run or stream error.
type ErrorData struct {
	Message string `json:"message,omitempty"`
}

// FinishData carries terminal step state plus the exact Eino output on run termination.
type FinishData struct {
	FinishReason string             `json:"finishReason,omitempty"`
	Usage        *schema.TokenUsage `json:"usage,omitempty"`
	Output       []*schema.Message  `json:"output,omitempty"`
}

// DecodeEventData converts RunEvent.Data into a typed payload.
func DecodeEventData[T any](ev *RunEvent) (T, bool) {
	var out T
	if ev == nil || ev.Data == nil {
		return out, false
	}
	if v, ok := ev.Data.(T); ok {
		return v, true
	}
	b, err := json.Marshal(ev.Data)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, false
	}
	return out, true
}
