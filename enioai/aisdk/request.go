package aisdk

import (
	"errors"

	"github.com/gin-gonic/gin"
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

// BindCreateRunRequest binds the request body without owning the route.
func BindCreateRunRequest(c *gin.Context) (CreateRunRequest, error) {
	var req CreateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return req, err
	}
	if len(req.Messages) == 0 {
		return req, errors.New("messages is required")
	}
	return req, nil
}

// BindCompletionsRequest is the same AI SDK request shape for direct completions.
func BindCompletionsRequest(c *gin.Context) (CreateRunRequest, error) {
	return BindCreateRunRequest(c)
}

// GetLastEventID resolves the resume cursor from query params or SSE headers.
func GetLastEventID(c *gin.Context) string {
	if v := c.Query("after"); v != "" {
		return v
	}
	if v := c.Query("lastEventId"); v != "" {
		return v
	}
	if v := c.GetHeader("Last-Event-ID"); v != "" {
		return v
	}
	return ""
}
