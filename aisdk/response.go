package aisdk

import (
	"github.com/xu756/einoai"
)

// CreateRunResponse is the default AI SDK create-run response shape.
type CreateRunResponse struct {
	SessionID string           `json:"sessionId"`
	RunID     string           `json:"run_id"`
	Status    einoai.RunStatus `json:"status"`
	Metadata  map[string]any   `json:"metadata,omitempty"`
}

// RunResponse is the run status response. Session history is application-owned.
type RunResponse struct {
	Run *einoai.RunInfo `json:"run"`
}

// CancelResponse is the default AI SDK cancel response shape.
type CancelResponse struct {
	OK bool `json:"ok"`
}

// DeleteSessionResponse is the default AI SDK delete-session response shape.
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
		Metadata:  run.Metadata,
	}
}

// NewRunResponse converts a run into a response struct.
func NewRunResponse(run *einoai.RunInfo) RunResponse {
	return RunResponse{Run: run}
}

// NewCancelResponse returns a successful cancel response struct.
func NewCancelResponse() CancelResponse {
	return CancelResponse{OK: true}
}

// NewDeleteSessionResponse returns a successful delete-session response struct.
func NewDeleteSessionResponse() DeleteSessionResponse {
	return DeleteSessionResponse{OK: true}
}
