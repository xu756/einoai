package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ChatCompletionsRequest is an OpenAI-compatible chat completions request.
type ChatCompletionsRequest struct {
	Model               string            `json:"model"`
	Messages            []ChatMessage     `json:"messages"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *StreamOptions    `json:"stream_options,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	MaxTokens           *int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
	N                   *int              `json:"n,omitempty"`
	Stop                json.RawMessage   `json:"stop,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]int    `json:"logit_bias,omitempty"`
	User                string            `json:"user,omitempty"`
	ResponseFormat      json.RawMessage   `json:"response_format,omitempty"`
	Tools               []Tool            `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

// StreamOptions is an OpenAI-compatible stream_options object.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatMessage is an OpenAI-compatible chat message.
type ChatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	Name             string          `json:"name,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
}

// Tool is an OpenAI-compatible tool definition.
type Tool struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function,omitempty"`
}

// ToolCall is an OpenAI-compatible tool call.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Index    *int         `json:"index,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is an OpenAI-compatible function call.
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// DecodeChatCompletionsRequest decodes an OpenAI chat completions body.
func DecodeChatCompletionsRequest(body io.Reader) (ChatCompletionsRequest, error) {
	var req ChatCompletionsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return req, err
	}
	if len(req.Messages) == 0 {
		return req, errors.New("messages is required")
	}
	return req, nil
}

// ResolveSessionID returns the first explicit session id, or a unique temporary
// id for stateless Chat Completions requests. The model name is intentionally
// not used as a shared fallback because unrelated concurrent requests must not
// contend for the same run slot.
func ResolveSessionID(_ ChatCompletionsRequest, candidates ...string) string {
	for _, v := range candidates {
		if v != "" {
			return v
		}
	}
	return newTemporarySessionID()
}

func newTemporarySessionID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return "openai-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("openai-%d", time.Now().UnixNano())
}
