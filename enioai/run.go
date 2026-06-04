package enioai

import (
	"context"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

// RunStatus is the lifecycle state for a persisted run.
type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusCancelled RunStatus = "cancelled"
	RunStatusFailed    RunStatus = "failed"
)

// CreateRunRequest starts an agent run for a session.
type CreateRunRequest struct {
	SessionID string
	Messages  []*schema.Message
	Agent     adk.Agent
	Metadata  map[string]any
}

// SubscribeRequest opens a persisted event stream for a run.
type SubscribeRequest struct {
	SessionID    string
	AfterEventID string
}

// RunInfo is stable metadata for a run.
type RunInfo struct {
	SessionID string         `json:"session_id"`
	RunID     string         `json:"runId"`
	Status    RunStatus      `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Error     string         `json:"error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Service is the core run orchestration API. It has no HTTP or Gin dependency.
type Service interface {
	CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
	GetRun(ctx context.Context, sessionID string) (*RunInfo, error)
	CancelRun(ctx context.Context, sessionID string) error
	SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}

// NewService creates the core enio-ai service.
func NewService(chatModel model.ToolCallingChatModel, db *redis.Client) Service {
	return newService(chatModel, db)
}

func isTerminalRunStatus(status RunStatus) bool {
	return status == RunStatusCompleted || status == RunStatusCancelled || status == RunStatusFailed
}
