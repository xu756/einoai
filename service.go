package einoai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

type service struct {
	store      *redisStore
	runMu      sync.Mutex
	runCancels map[string]context.CancelFunc
}

func newService(db *redis.Client, opts serviceOptions) *service {
	return &service{
		store:      newRedisStore(db, opts.redisTTL),
		runCancels: make(map[string]context.CancelFunc),
	}
}

func (s *service) CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error) {
	if req.SessionID == "" {
		return nil, errors.New("sessionID is required")
	}
	if req.Agent == nil {
		return nil, errors.New("agent is required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("messages is required")
	}
	snapshotMessages, err := requestSnapshotMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	currentRun, err := s.store.getCurrentRun(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if currentRun != nil && !isTerminalRunStatus(currentRun.Status) {
		return nil, fmt.Errorf("run %s for session %s is still active", currentRun.RunID, req.SessionID)
	}
	if currentRun != nil {
		_ = s.store.clearCurrentRunIfMatches(ctx, req.SessionID, currentRun.RunID)
	}
	runMessages := append([]*schema.Message{}, snapshotMessages...)

	run := &RunInfo{
		SessionID: req.SessionID,
		RunID:     newRunID(),
		Status:    RunStatusQueued,
		Metadata:  req.Metadata,
	}
	if err := s.store.initRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.store.setActiveMessages(ctx, run.SessionID, run.RunID, snapshotMessages); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(context.Background(), run.SessionID, run.RunID, EventRunCreated, nil); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s.registerRunCancel(run.RunID, cancel)
	go s.executeRun(runCtx, cancel, run.SessionID, run.RunID, runMessages, req.Agent)

	return run, nil
}

func (s *service) GetRun(ctx context.Context, sessionID string) (*RunInfo, error) {
	run, err := s.store.getCurrentRun(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	if isTerminalRunStatus(run.Status) {
		_ = s.store.clearCurrentRunIfMatches(ctx, sessionID, run.RunID)
		return nil, nil
	}
	return run, nil
}

func requestSnapshotMessages(messages []*schema.Message) ([]*schema.Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("messages is required")
	}
	for i, msg := range messages {
		if msg == nil {
			return nil, fmt.Errorf("message %d is required", i)
		}
	}
	return append([]*schema.Message{}, messages...), nil
}

func (s *service) GetMessages(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	messages, err := s.store.getSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	run, err := s.GetRun(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return messages, nil
	}
	active, err := s.store.getActiveMessages(ctx, sessionID, run.RunID)
	if err != nil {
		return nil, err
	}
	return active, nil
}

func (s *service) CancelRun(ctx context.Context, sessionID string, runID string) error {
	if sessionID == "" {
		return errors.New("sessionID is required")
	}
	if runID == "" {
		return errors.New("runID is required")
	}
	run, err := s.store.getRun(ctx, sessionID, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	if isTerminalRunStatus(run.Status) {
		return nil
	}
	if s.cancelActiveRun(runID) {
		return nil
	}
	if err := s.appendFinish(ctx, sessionID, runID, "cancelled", nil); err != nil {
		return err
	}
	if err := s.store.commitRunMessages(ctx, sessionID, runID, nil); err != nil {
		return err
	}
	if err := s.store.setRunStatus(ctx, sessionID, runID, RunStatusCancelled, ""); err != nil {
		return err
	}
	return s.store.clearCurrentRunIfMatches(ctx, sessionID, runID)
}

func (s *service) SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error) {
	if req.SessionID == "" {
		return nil, errors.New("sessionID is required")
	}
	if req.RunID == "" {
		return nil, errors.New("runID is required")
	}
	run, err := s.store.getRun(ctx, req.SessionID, req.RunID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run %s for session %s not found", req.RunID, req.SessionID)
	}
	return &redisEventStream{
		store:     s.store,
		sessionID: req.SessionID,
		runID:     req.RunID,
		lastID:    "0-0",
	}, nil
}

func (s *service) executeRun(ctx context.Context, cancel context.CancelFunc, sessionID, runID string, messages []*schema.Message, agent adk.Agent) {
	defer cancel()
	defer s.unregisterRunCancel(runID)

	if err := s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusRunning, ""); err != nil {
		return
	}
	_, _ = s.appendEvent(context.Background(), sessionID, runID, EventRunStarted, nil)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, messages)
	state := newRunEventBuilder(s, sessionID, runID)

	if err := s.streamAgentEvents(ctx, iter, state); err != nil {
		if errors.Is(err, context.Canceled) {
			_ = state.closeOpenBlocks(context.Background())
			_ = s.appendFinish(context.Background(), sessionID, runID, "cancelled", nil)
			_ = s.store.commitRunMessages(context.Background(), sessionID, runID, nil)
			_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusCancelled, "")
			_ = s.store.clearCurrentRunIfMatches(context.Background(), sessionID, runID)
			return
		}
		_ = state.closeOpenBlocks(context.Background())
		_, _ = s.appendEvent(context.Background(), sessionID, runID, EventError, ErrorData{Message: err.Error()})
		_ = s.appendFinish(context.Background(), sessionID, runID, "error", nil)
		_ = s.store.commitRunMessages(context.Background(), sessionID, runID, nil)
		_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusFailed, err.Error())
		_ = s.store.clearCurrentRunIfMatches(context.Background(), sessionID, runID)
		return
	}

	if !state.finished {
		_ = state.writeFinish(context.Background(), "stop", state.usage)
	}
	_ = s.store.commitRunMessages(context.Background(), sessionID, runID, state.outputMessages)
	_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusCompleted, "")
	_ = s.store.clearCurrentRunIfMatches(context.Background(), sessionID, runID)
}

func (s *service) streamAgentEvents(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], state *runEventBuilder) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		event, ok := iter.Next()
		if !ok {
			return nil
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		mv := event.Output.MessageOutput
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					return err
				}
				if msg != nil {
					if err := state.writeMessage(ctx, msg); err != nil {
						return err
					}
				}
			}
			continue
		}
		if mv.Message != nil {
			if err := state.writeMessage(ctx, mv.Message); err != nil {
				return err
			}
		}
	}
}

func (s *service) appendEvent(ctx context.Context, sessionID, runID string, typ EventType, data any) (*RunEvent, error) {
	return s.store.appendEvent(ctx, RunEvent{
		SessionID: sessionID,
		RunID:     runID,
		Type:      typ,
		Data:      data,
	})
}

func (s *service) appendFinish(ctx context.Context, sessionID, runID string, reason string, usage *schema.TokenUsage) error {
	_, err := s.appendEvent(ctx, sessionID, runID, EventFinish, FinishData{
		FinishReason: reason,
		Usage:        usage,
	})
	return err
}

func (s *service) registerRunCancel(runID string, cancel context.CancelFunc) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.runCancels[runID] = cancel
}

func (s *service) unregisterRunCancel(runID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.runCancels, runID)
}

func (s *service) cancelActiveRun(runID string) bool {
	s.runMu.Lock()
	cancel := s.runCancels[runID]
	s.runMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func newRunID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
