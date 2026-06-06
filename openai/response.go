package openai

import (
	"github.com/cloudwego/eino/schema"
	"github.com/xu756/einoai"
)

// CreateRunResponse is the default OpenAI-compatible create-run response shape.
type CreateRunResponse struct {
	SessionID string           `json:"sessionId"`
	RunID     string           `json:"run_id"`
	Status    einoai.RunStatus `json:"status"`
}

// RunResponse is the default OpenAI-compatible get-run response shape.
type RunResponse struct {
	Run      *einoai.RunInfo `json:"run"`
	Messages []ChatMessage   `json:"messages"`
}

// CancelResponse is the default OpenAI-compatible cancel response shape.
type CancelResponse struct {
	OK bool `json:"ok"`
}

// NewCreateRunResponse converts a run into a response struct.
func NewCreateRunResponse(run *einoai.RunInfo) CreateRunResponse {
	if run == nil {
		return CreateRunResponse{}
	}
	return CreateRunResponse{
		SessionID: run.SessionID,
		RunID:     run.RunID,
		Status:    run.Status,
	}
}

// NewRunResponse converts stored schema messages into OpenAI chat messages.
func NewRunResponse(run *einoai.RunInfo, messages []*schema.Message) RunResponse {
	return RunResponse{
		Run:      run,
		Messages: FromSchemaMessages(messages),
	}
}

// NewCancelResponse returns a successful cancel response struct.
func NewCancelResponse() CancelResponse {
	return CancelResponse{OK: true}
}
