package einoai

import (
	"context"
	"log"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

const (
	// DefaultRedisTTL is the default expiration for Redis-backed run and event keys.
	DefaultRedisTTL = 7 * 24 * time.Hour
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
	SessionID   string
	Messages    []*schema.Message
	Agent       adk.Agent
	Metadata    map[string]any
	OnCompleted OnRunCompleted
}

// RunResult contains the complete result of a successfully completed run.
type RunResult struct {
	Run      *RunInfo
	Input    []*schema.Message
	Output   []*schema.Message
	Messages []*schema.Message
	Usage    *schema.TokenUsage
}

// OnRunCompleted receives a successfully completed run and its complete messages.
type OnRunCompleted func(context.Context, *RunResult) error

// CompletionErrorHandler observes errors returned or panics raised by OnRunCompleted.
type CompletionErrorHandler func(context.Context, string, string, error)

// SubscribeRequest opens a persisted event stream for a run.
type SubscribeRequest struct {
	SessionID string
	RunID     string
}

// RunInfo is stable metadata for a run.
type RunInfo struct {
	SessionID string         `json:"session_id"`
	RunID     string         `json:"run_id"`
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
	DeleteSession(ctx context.Context, sessionID string) error
	CancelRun(ctx context.Context, sessionID string, runID string) error
	SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}

type serviceOptions struct {
	redisTTL            time.Duration
	completionErrorHook CompletionErrorHandler
}

// ServiceOption configures the core einoai service.
type ServiceOption func(*serviceOptions)

// WithRedisTTL configures expiration for Redis-backed run and event keys.
//
// A ttl <= 0 disables expiration for keys written by the service.
func WithRedisTTL(ttl time.Duration) ServiceOption {
	return func(opts *serviceOptions) {
		opts.redisTTL = ttl
	}
}

// WithCompletionErrorHandler configures observation of completion hook failures.
func WithCompletionErrorHandler(handler CompletionErrorHandler) ServiceOption {
	return func(opts *serviceOptions) {
		opts.completionErrorHook = handler
	}
}

func defaultServiceOptions() serviceOptions {
	return serviceOptions{
		redisTTL: DefaultRedisTTL,
		completionErrorHook: func(_ context.Context, sessionID, runID string, err error) {
			log.Printf("einoai completion hook failed session=%s run=%s: %v", sessionID, runID, err)
		},
	}
}

// NewService creates the core einoai service.
func NewService(db *redis.Client, opts ...ServiceOption) Service {
	options := defaultServiceOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return newService(db, options)
}

func isTerminalRunStatus(status RunStatus) bool {
	return status == RunStatusCompleted || status == RunStatusCancelled || status == RunStatusFailed
}
