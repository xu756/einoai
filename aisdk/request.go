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
	Role     string         `json:"role,omitempty"`
	Parts    []Part         `json:"parts,omitempty"`
	Content  string         `json:"content,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// Part is an AI SDK message part.
type Part struct {
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Filename  string `json:"filename,omitempty"`
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
