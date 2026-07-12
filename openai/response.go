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

// RunResponse is the protocol-neutral get-run response shape.
type RunResponse = einoai.SessionRunResponse

// CancelResponse is the default OpenAI-compatible cancel response shape.
type CancelResponse struct {
	OK bool `json:"ok"`
}

// DeleteSessionResponse is the default OpenAI-compatible delete-session response shape.
type DeleteSessionResponse struct {
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
func NewRunResponse(run *einoai.RunInfo, messages []*schema.Message) (RunResponse, error) {
	return einoai.NewSessionRunResponse(run, messages)
}

// NewCancelResponse returns a successful cancel response struct.
func NewCancelResponse() CancelResponse {
	return CancelResponse{OK: true}
}

// NewDeleteSessionResponse returns a successful delete-session response struct.
func NewDeleteSessionResponse() DeleteSessionResponse {
	return DeleteSessionResponse{OK: true}
}
