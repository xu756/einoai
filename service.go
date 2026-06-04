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
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

type service struct {
	model      model.ToolCallingChatModel
	store      *redisStore
	runMu      sync.Mutex
	runCancels map[string]context.CancelFunc
}

func newService(chatModel model.ToolCallingChatModel, db *redis.Client) *service {
	return &service{
		model:      chatModel,
		store:      newRedisStore(db),
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

	run := &RunInfo{
		SessionID: req.SessionID,
		RunID:     newRunID(),
		Status:    RunStatusQueued,
		Metadata:  req.Metadata,
	}
	if err := s.store.initRun(ctx, run); err != nil {
		return nil, err
	}
	if _, err := s.appendEvent(context.Background(), run.SessionID, run.RunID, EventRunCreated, nil); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s.registerRunCancel(run.RunID, cancel)
	go s.executeRun(runCtx, cancel, run.SessionID, run.RunID, req.Messages, req.Agent)

	return run, nil
}

func (s *service) GetRun(ctx context.Context, sessionID string) (*RunInfo, error) {
	return s.store.getCurrentRun(ctx, sessionID)
}

func (s *service) CancelRun(ctx context.Context, sessionID string) error {
	run, err := s.store.getCurrentRun(ctx, sessionID)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	if isTerminalRunStatus(run.Status) {
		return nil
	}
	runID := run.RunID
	if s.cancelActiveRun(runID) {
		return nil
	}
	if err := s.appendFinish(ctx, sessionID, runID, "cancelled", nil); err != nil {
		return err
	}
	return s.store.setRunStatus(ctx, sessionID, runID, RunStatusCancelled, "")
}

func (s *service) SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error) {
	if req.SessionID == "" {
		return nil, errors.New("sessionID is required")
	}
	run, err := s.store.getCurrentRun(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run for session %s not found", req.SessionID)
	}
	lastID := req.AfterEventID
	if lastID == "" {
		lastID = "0-0"
	}
	return &redisEventStream{
		store:     s.store,
		sessionID: req.SessionID,
		runID:     run.RunID,
		lastID:    lastID,
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
			_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusCancelled, "")
			return
		}
		_ = state.closeOpenBlocks(context.Background())
		_, _ = s.appendEvent(context.Background(), sessionID, runID, EventError, ErrorData{Message: err.Error()})
		_ = s.appendFinish(context.Background(), sessionID, runID, "error", nil)
		_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusFailed, err.Error())
		return
	}

	if !state.finished {
		_ = state.closeOpenBlocks(context.Background())
		_ = s.appendFinish(context.Background(), sessionID, runID, "stop", state.usage)
	}
	_ = s.store.setRunStatus(context.Background(), sessionID, runID, RunStatusCompleted, "")
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
