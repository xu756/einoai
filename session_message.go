package einoai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const sessionMessageIDExtraKey = "_einoai_message_id"

// SessionRunResponse is the protocol-neutral session history response.
type SessionRunResponse struct {
	Run      *RunInfo         `json:"run"`
	Messages []SessionMessage `json:"messages"`
}

// SessionMessage is one stored message in execution order.
type SessionMessage struct {
	ID           string         `json:"id"`
	Role         string         `json:"role"`
	Name         string         `json:"name,omitempty"`
	Parts        []SessionPart  `json:"parts"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Usage        *SessionUsage  `json:"usage,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// SessionPart is a tagged content, media, reasoning, or tool part.
type SessionPart struct {
	Type       string         `json:"type"`
	Text       string         `json:"text,omitempty"`
	Signature  string         `json:"signature,omitempty"`
	URL        string         `json:"url,omitempty"`
	Base64Data string         `json:"base64_data,omitempty"`
	MediaType  string         `json:"media_type,omitempty"`
	Name       string         `json:"name,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Input      any            `json:"input,omitempty"`
	Output     any            `json:"output,omitempty"`
	DataType   string         `json:"data_type,omitempty"`
	Data       any            `json:"data,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// SessionUsage is a protocol-neutral token usage breakdown.
type SessionUsage struct {
	InputTokens        int                       `json:"input_tokens"`
	OutputTokens       int                       `json:"output_tokens"`
	TotalTokens        int                       `json:"total_tokens"`
	InputTokenDetails  SessionInputTokenDetails  `json:"input_token_details"`
	OutputTokenDetails SessionOutputTokenDetails `json:"output_token_details"`
}

// SessionInputTokenDetails describes cached and uncached input tokens.
type SessionInputTokenDetails struct {
	CachedTokens   int `json:"cached_tokens"`
	UncachedTokens int `json:"uncached_tokens"`
}

// SessionOutputTokenDetails describes reasoning and text output tokens.
type SessionOutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	TextTokens      int `json:"text_tokens"`
}

// NewSessionRunResponse converts stored Eino messages into the unified session format.
func NewSessionRunResponse(run *RunInfo, messages []*schema.Message) (SessionRunResponse, error) {
	converted := make([]SessionMessage, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			continue
		}
		item, err := newSessionMessage(message, index)
		if err != nil {
			return SessionRunResponse{}, fmt.Errorf("convert message %d: %w", index, err)
		}
		converted = append(converted, item)
	}
	return SessionRunResponse{Run: run, Messages: converted}, nil
}

func newSessionMessage(message *schema.Message, index int) (SessionMessage, error) {
	out := SessionMessage{
		ID:       sessionMessageID(message, index),
		Role:     string(message.Role),
		Name:     message.Name,
		Parts:    []SessionPart{},
		Metadata: publicMetadata(message.Extra),
	}
	if message.ReasoningContent != "" {
		out.Parts = append(out.Parts, SessionPart{Type: "reasoning", Text: message.ReasoningContent})
	}
	if message.Role == schema.Tool {
		out.Parts = append(out.Parts, SessionPart{
			Type:       "tool-result",
			ToolCallID: message.ToolCallID,
			ToolName:   message.ToolName,
			Output:     parseSessionValue(message.Content),
		})
	} else if message.Content != "" {
		out.Parts = append(out.Parts, SessionPart{Type: "text", Text: message.Content})
	}
	for _, call := range message.ToolCalls {
		out.Parts = append(out.Parts, SessionPart{
			Type:       "tool-call",
			ToolCallID: call.ID,
			ToolName:   call.Function.Name,
			Input:      parseSessionValue(call.Function.Arguments),
			Metadata:   publicMetadata(call.Extra),
		})
	}
	if message.ResponseMeta != nil {
		out.FinishReason = message.ResponseMeta.FinishReason
		out.Usage = newSessionUsage(message.ResponseMeta.Usage)
	}
	if _, err := json.Marshal(out); err != nil {
		return SessionMessage{}, fmt.Errorf("message JSON: %w", err)
	}
	return out, nil
}

func sessionMessageID(message *schema.Message, index int) string {
	if message.Extra != nil {
		if id, _ := message.Extra[sessionMessageIDExtraKey].(string); id != "" {
			return id
		}
		if id, _ := message.Extra["_einoai_ui_id"].(string); id != "" {
			return id
		}
	}
	return fmt.Sprintf("msg_%d", index)
}

func publicMetadata(extra map[string]any) map[string]any {
	var out map[string]any
	for key, value := range extra {
		if strings.HasPrefix(key, "_einoai_") {
			continue
		}
		if out == nil {
			out = make(map[string]any)
		}
		out[key] = value
	}
	return out
}

func parseSessionValue(value string) any {
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		return decoded
	}
	return value
}

func newSessionUsage(usage *schema.TokenUsage) *SessionUsage {
	normalized := NormalizeTokenUsage(usage)
	if normalized == nil {
		return nil
	}
	return &SessionUsage{
		InputTokens:  normalized.InputTokens,
		OutputTokens: normalized.OutputTokens,
		TotalTokens:  normalized.TotalTokens,
		InputTokenDetails: SessionInputTokenDetails{
			CachedTokens:   normalized.CachedInputTokens,
			UncachedTokens: normalized.UncachedInputTokens,
		},
		OutputTokenDetails: SessionOutputTokenDetails{
			ReasoningTokens: normalized.ReasoningTokens,
			TextTokens:      normalized.TextOutputTokens,
		},
	}
}
