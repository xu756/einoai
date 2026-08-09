package einoai

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

const (
	// DefaultRedisTTL is the default expiration for Redis-backed run and event keys.
	DefaultRedisTTL = 7 * 24 * time.Hour
	// DefaultRunTimeout limits one asynchronous agent run.
	DefaultRunTimeout = 10 * time.Minute
)

// RunStatus is the lifecycle state for a persisted run.
type RunStatus string

var (
	// ErrRunActive indicates that the session already has a non-terminal run.
	ErrRunActive = errors.New("einoai: session already has an active run")
	// ErrRunNotFound indicates that a requested run does not exist.
	ErrRunNotFound = errors.New("einoai: run not found")
	errRunTerminal = errors.New("einoai: run is already terminal")
)

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
	SessionID    string
	RunID        string
	AfterEventID string // Redis stream id to resume after; empty replays from the beginning.
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

// RunLookupService extends Service with lookup for persisted terminal runs.
// Keeping this separate preserves compatibility for existing Service mocks.
type RunLookupService interface {
	Service
	GetRunByID(ctx context.Context, sessionID string, runID string) (*RunInfo, error)
}

type serviceOptions struct {
	redisTTL            time.Duration
	runTimeout          time.Duration
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

// WithRunTimeout configures the maximum lifetime of an asynchronous run.
//
// A timeout <= 0 disables the service-level deadline. The request context is
// detached from cancellation, while its values are preserved for the run.
func WithRunTimeout(timeout time.Duration) ServiceOption {
	return func(opts *serviceOptions) {
		opts.runTimeout = timeout
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
		redisTTL:   DefaultRedisTTL,
		runTimeout: DefaultRunTimeout,
		completionErrorHook: func(_ context.Context, sessionID, runID string, err error) {
			log.Printf("einoai completion hook failed session=%s run=%s: %v", sessionID, runID, err)
		},
	}
}

// NewService creates the core einoai service.
func NewService(db *redis.Client, opts ...ServiceOption) RunLookupService {
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
