package aisdk

import (
	"encoding/json"
	"errors"
	"io"
)

// CreateRunRequest is the AI SDK / assistant-ui request body.
type CreateRunRequest struct {
	Messages []Message      `json:"messages"`
	Model    string         `json:"model,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
}

// Message is an AI SDK UI message.
type Message struct {
	ID       string         `json:"id,omitempty"`
	Role     string         `json:"role"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Parts    []Part         `json:"parts"`
}

// Part is an AI SDK UI message part.
type Part struct {
	ID               string         `json:"id,omitempty"`
	Type             string         `json:"type"`
	Text             string         `json:"text,omitempty"`
	State            string         `json:"state,omitempty"`
	Data             any            `json:"data,omitempty"`
	ToolCallID       string         `json:"toolCallId,omitempty"`
	Input            any            `json:"input,omitempty"`
	Output           any            `json:"output,omitempty"`
	ErrorText        string         `json:"errorText,omitempty"`
	ProviderExecuted *bool          `json:"providerExecuted,omitempty"`
	URL              string         `json:"url,omitempty"`
	MediaType        string         `json:"mediaType,omitempty"`
	Filename         string         `json:"filename,omitempty"`
	SourceID         string         `json:"sourceId,omitempty"`
	Title            string         `json:"title,omitempty"`
	ProviderMetadata map[string]any `json:"providerMetadata,omitempty"`
}

// DecodeCreateRunRequest decodes a create-run request body.
func DecodeCreateRunRequest(body io.Reader) (CreateRunRequest, error) {
	var req CreateRunRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return req, err
	}
	if len(req.Messages) == 0 {
		return req, errors.New("messages is required")
	}
	return req, nil
}

// DecodeCompletionsRequest is the same AI SDK request shape for direct completions.
func DecodeCompletionsRequest(body io.Reader) (CreateRunRequest, error) {
	return DecodeCreateRunRequest(body)
}
