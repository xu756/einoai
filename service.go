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

var errSessionDeleted = errors.New("session deleted")

const (
	sessionLockShards        = 64
	runLifecyclePollInterval = time.Second
)

type service struct {
	store               *redisStore
	runTimeout          time.Duration
	completionErrorHook CompletionErrorHandler

	runMu       sync.Mutex
	runCancels  map[string]context.CancelFunc
	deletedRuns map[string]struct{}

	sessionLocks [sessionLockShards]sync.Mutex
}

var (
	_ Service          = (*service)(nil)
	_ RunLookupService = (*service)(nil)
)

func newService(db *redis.Client, opts serviceOptions) *service {
	return &service{
		store:               newRedisStore(db, opts.redisTTL),
		runTimeout:          opts.runTimeout,
		completionErrorHook: opts.completionErrorHook,
		runCancels:          make(map[string]context.CancelFunc),
		deletedRuns:         make(map[string]struct{}),
	}
}

func (s *service) CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error) {
	if req.SessionID == "" {
		return nil, errors.New("sessionID is required")
	}
	if req.Agent == nil {
		return nil, errors.New("agent is required")
	}

	snapshotMessages, err := requestSnapshotMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	lock := s.sessionLock(req.SessionID)
	lock.Lock()
	defer lock.Unlock()

	currentRun, err := s.store.getCurrentRun(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if currentRun != nil && !isTerminalRunStatus(currentRun.Status) {
		return nil, fmt.Errorf("%w: run %s for session %s", ErrRunActive, currentRun.RunID, req.SessionID)
	}
	if currentRun != nil {
		if err := s.store.clearCurrentRunIfMatches(ctx, req.SessionID, currentRun.RunID); err != nil {
			return nil, err
		}
	}

	run := &RunInfo{
		SessionID: req.SessionID,
		RunID:     newRunID(),
		Status:    RunStatusQueued,
		Metadata:  cloneAnyMap(req.Metadata),
	}
	assignSessionMessageIDs(snapshotMessages, run.RunID, "input")

	if err := s.store.initRun(ctx, run); err != nil {
		if errors.Is(err, ErrRunActive) {
			if active, getErr := s.store.getCurrentRun(ctx, req.SessionID); getErr == nil && active != nil {
				return nil, fmt.Errorf("%w: run %s for session %s", ErrRunActive, active.RunID, req.SessionID)
			}
		}
		return nil, err
	}
	if _, err := s.appendEvent(detachContext(ctx), run.SessionID, run.RunID, EventRunCreated, nil); err != nil {
		_ = s.store.deleteRun(context.Background(), run.SessionID, run.RunID)
		return nil, err
	}

	runCtx, cancel := s.newRunContext(ctx)
	s.registerRunCancel(run.RunID, cancel)
	go s.executeRun(runCtx, cancel, cloneRunInfo(run), snapshotMessages, req.Agent, req.OnCompleted)

	return cloneRunInfo(run), nil
}

// GetRun returns the current non-terminal run for a session.
func (s *service) GetRun(ctx context.Context, sessionID string) (*RunInfo, error) {
	if sessionID == "" {
		return nil, errors.New("sessionID is required")
	}
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

// GetRunByID returns persisted metadata for a specific run, including terminal runs.
func (s *service) GetRunByID(ctx context.Context, sessionID string, runID string) (*RunInfo, error) {
	if sessionID == "" {
		return nil, errors.New("sessionID is required")
	}
	if runID == "" {
		return nil, errors.New("runID is required")
	}
	run, err := s.store.getRun(ctx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("%w: run %s for session %s", ErrRunNotFound, runID, sessionID)
	}
	return run, nil
}

func requestSnapshotMessages(messages []*schema.Message) ([]*schema.Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("messages is required")
	}
	out := make([]*schema.Message, len(messages))
	for i, msg := range messages {
		if msg == nil {
			return nil, fmt.Errorf("message %d is required", i)
		}
		out[i] = cloneMessage(msg)
	}
	return out, nil
}

func cloneMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	out := *message
	out.Extra = cloneAnyMap(message.Extra)

	if message.ResponseMeta != nil {
		responseMeta := *message.ResponseMeta
		if message.ResponseMeta.Usage != nil {
			usage := *message.ResponseMeta.Usage
			responseMeta.Usage = &usage
		}
		if message.ResponseMeta.LogProbs != nil {
			logProbs := *message.ResponseMeta.LogProbs
			logProbs.Content = append([]schema.LogProb(nil), message.ResponseMeta.LogProbs.Content...)
			for i := range logProbs.Content {
				logProbs.Content[i].Bytes = append([]int64(nil), message.ResponseMeta.LogProbs.Content[i].Bytes...)
				logProbs.Content[i].TopLogProbs = append([]schema.TopLogProb(nil), message.ResponseMeta.LogProbs.Content[i].TopLogProbs...)
				for j := range logProbs.Content[i].TopLogProbs {
					logProbs.Content[i].TopLogProbs[j].Bytes = append([]int64(nil), message.ResponseMeta.LogProbs.Content[i].TopLogProbs[j].Bytes...)
				}
			}
			responseMeta.LogProbs = &logProbs
		}
		out.ResponseMeta = &responseMeta
	}

	if message.ToolCalls != nil {
		out.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		for i := range out.ToolCalls {
			if message.ToolCalls[i].Index != nil {
				index := *message.ToolCalls[i].Index
				out.ToolCalls[i].Index = &index
			}
			out.ToolCalls[i].Extra = cloneAnyMap(message.ToolCalls[i].Extra)
		}
	}
	if message.MultiContent != nil {
		out.MultiContent = cloneLegacyMultiContent(message.MultiContent)
	}
	if message.UserInputMultiContent != nil {
		out.UserInputMultiContent = cloneInputMultiContent(message.UserInputMultiContent)
	}
	if message.AssistantGenMultiContent != nil {
		out.AssistantGenMultiContent = cloneOutputMultiContent(message.AssistantGenMultiContent)
	}
	return &out
}

func cloneLegacyMultiContent(parts []schema.ChatMessagePart) []schema.ChatMessagePart {
	out := append([]schema.ChatMessagePart(nil), parts...)
	for i := range out {
		if parts[i].ImageURL != nil {
			value := *parts[i].ImageURL
			value.Extra = cloneAnyMap(parts[i].ImageURL.Extra)
			out[i].ImageURL = &value
		}
		if parts[i].AudioURL != nil {
			value := *parts[i].AudioURL
			value.Extra = cloneAnyMap(parts[i].AudioURL.Extra)
			out[i].AudioURL = &value
		}
		if parts[i].VideoURL != nil {
			value := *parts[i].VideoURL
			value.Extra = cloneAnyMap(parts[i].VideoURL.Extra)
			out[i].VideoURL = &value
		}
		if parts[i].FileURL != nil {
			value := *parts[i].FileURL
			value.Extra = cloneAnyMap(parts[i].FileURL.Extra)
			out[i].FileURL = &value
		}
	}
	return out
}

func cloneInputMultiContent(parts []schema.MessageInputPart) []schema.MessageInputPart {
	out := append([]schema.MessageInputPart(nil), parts...)
	for i := range out {
		out[i].Extra = cloneAnyMap(parts[i].Extra)
		if parts[i].Image != nil {
			value := *parts[i].Image
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Image.MessagePartCommon)
			out[i].Image = &value
		}
		if parts[i].Audio != nil {
			value := *parts[i].Audio
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Audio.MessagePartCommon)
			out[i].Audio = &value
		}
		if parts[i].Video != nil {
			value := *parts[i].Video
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Video.MessagePartCommon)
			out[i].Video = &value
		}
		if parts[i].File != nil {
			value := *parts[i].File
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].File.MessagePartCommon)
			out[i].File = &value
		}
		if parts[i].ToolSearchResult != nil {
			value := *parts[i].ToolSearchResult
			out[i].ToolSearchResult = &value
		}
	}
	return out
}

func cloneOutputMultiContent(parts []schema.MessageOutputPart) []schema.MessageOutputPart {
	out := append([]schema.MessageOutputPart(nil), parts...)
	for i := range out {
		out[i].Extra = cloneAnyMap(parts[i].Extra)
		if parts[i].Image != nil {
			value := *parts[i].Image
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Image.MessagePartCommon)
			out[i].Image = &value
		}
		if parts[i].Audio != nil {
			value := *parts[i].Audio
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Audio.MessagePartCommon)
			out[i].Audio = &value
		}
		if parts[i].Video != nil {
			value := *parts[i].Video
			value.MessagePartCommon = cloneMessagePartCommon(parts[i].Video.MessagePartCommon)
			out[i].Video = &value
		}
		if parts[i].Reasoning != nil {
			value := *parts[i].Reasoning
			out[i].Reasoning = &value
		}
		if parts[i].StreamingMeta != nil {
			value := *parts[i].StreamingMeta
			out[i].StreamingMeta = &value
		}
	}
	return out
}

func cloneMessagePartCommon(value schema.MessagePartCommon) schema.MessagePartCommon {
	out := value
	out.URL = cloneStringPointer(value.URL)
	out.Base64Data = cloneStringPointer(value.Base64Data)
	out.Extra = cloneAnyMap(value.Extra)
	return out
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneRunInfo(run *RunInfo) *RunInfo {
	if run == nil {
		return nil
	}
	out := *run
	out.Metadata = cloneAnyMap(run.Metadata)
	return &out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneAnyValue(value)
	}
	return out
}

func cloneAnyValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = cloneAnyValue(value[i])
		}
		return out
	default:
		return value
	}
}

func (s *service) DeleteSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("sessionID is required")
	}

	lock := s.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	run, err := s.store.getCurrentRun(ctx, sessionID)
	if err != nil {
		return err
	}
	if run != nil && !isTerminalRunStatus(run.Status) {
		s.markRunDeleted(run.RunID)
		_ = s.cancelActiveRun(run.RunID)
	}
	return s.store.deleteSession(ctx, sessionID)
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
	if run == nil || isTerminalRunStatus(run.Status) {
		return nil
	}
	if s.cancelActiveRun(runID) {
		return nil
	}
	if err := s.appendFinish(ctx, sessionID, runID, "cancelled", nil); err != nil {
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
		return nil, fmt.Errorf("%w: run %s for session %s", ErrRunNotFound, req.RunID, req.SessionID)
	}
	return newRedisEventStream(s.store, req.SessionID, req.RunID, req.AfterEventID), nil
}

func (s *service) executeRun(ctx context.Context, cancel context.CancelFunc, run *RunInfo, messages []*schema.Message, agent adk.Agent, onCompleted OnRunCompleted) {
	defer cancel()
	defer s.unregisterRunCancel(run.RunID)

	sessionID, runID := run.SessionID, run.RunID
	persistCtx := detachContext(ctx)

	if s.isRunDeleted(runID) {
		return
	}
	if err := s.store.setRunStatus(persistCtx, sessionID, runID, RunStatusRunning, ""); err != nil {
		return
	}
	if s.isRunDeleted(runID) {
		return
	}
	if _, err := s.appendEvent(persistCtx, sessionID, runID, EventRunStarted, nil); err != nil {
		return
	}

	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go s.watchRunLifecycle(watchCtx, sessionID, runID, cancel)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, messages)
	state := newRunEventBuilder(s, sessionID, runID)

	if err := s.streamAgentEvents(ctx, iter, state); err != nil {
		if errors.Is(err, errSessionDeleted) || errors.Is(err, errRunTerminal) || s.isRunDeleted(runID) {
			return
		}
		if errors.Is(err, context.Canceled) {
			s.finishCancelled(persistCtx, state, sessionID, runID)
			return
		}
		s.finishFailed(persistCtx, state, sessionID, runID, err)
		return
	}

	if s.isRunDeleted(runID) {
		return
	}
	if !state.finished {
		if err := state.writeFinish(persistCtx, "stop", state.usage); err != nil {
			if !errors.Is(err, errSessionDeleted) && !errors.Is(err, errRunTerminal) {
				s.finishFailed(persistCtx, state, sessionID, runID, err)
			}
			return
		}
	}
	if s.isRunDeleted(runID) {
		return
	}
	if err := s.store.setRunStatus(persistCtx, sessionID, runID, RunStatusCompleted, ""); err != nil {
		return
	}
	_ = s.store.clearCurrentRunIfMatches(persistCtx, sessionID, runID)

	run.Status = RunStatusCompleted
	run.UpdatedAt = time.Now()
	result := &RunResult{
		Run:      cloneRunInfo(run),
		Input:    cloneMessages(messages),
		Output:   cloneMessages(state.outputMessages),
		Messages: cloneMessages(append(append([]*schema.Message{}, messages...), state.outputMessages...)),
		Usage:    cloneTokenUsage(state.usage),
	}
	s.invokeCompletionHook(persistCtx, sessionID, runID, onCompleted, result)
}

func (s *service) watchRunLifecycle(ctx context.Context, sessionID, runID string, cancel context.CancelFunc) {
	ticker := time.NewTicker(runLifecyclePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, err := s.store.getRun(ctx, sessionID, runID)
			if err != nil {
				continue
			}
			if run == nil || isTerminalRunStatus(run.Status) {
				cancel()
				return
			}
		}
	}
}

func (s *service) finishCancelled(ctx context.Context, state *runEventBuilder, sessionID, runID string) {
	_ = state.closeOpenBlocks(ctx)
	_ = s.appendFinish(ctx, sessionID, runID, "cancelled", nil)
	_ = s.store.setRunStatus(ctx, sessionID, runID, RunStatusCancelled, "")
	_ = s.store.clearCurrentRunIfMatches(ctx, sessionID, runID)
}

func (s *service) finishFailed(ctx context.Context, state *runEventBuilder, sessionID, runID string, runErr error) {
	if runErr == nil {
		return
	}
	_ = state.closeOpenBlocks(ctx)
	_, _ = s.appendEvent(ctx, sessionID, runID, EventError, ErrorData{Message: runErr.Error()})
	_ = s.appendFinish(ctx, sessionID, runID, "error", nil)
	_ = s.store.setRunStatus(ctx, sessionID, runID, RunStatusFailed, runErr.Error())
	_ = s.store.clearCurrentRunIfMatches(ctx, sessionID, runID)
}

func (s *service) invokeCompletionHook(ctx context.Context, sessionID, runID string, hook OnRunCompleted, result *RunResult) {
	if hook == nil {
		return
	}
	var hookErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				hookErr = fmt.Errorf("completion hook panic: %v", recovered)
			}
		}()
		hookErr = hook(ctx, result)
	}()
	if hookErr != nil && s.completionErrorHook != nil {
		s.completionErrorHook(ctx, sessionID, runID, hookErr)
	}
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
	if s.isRunDeleted(runID) {
		return nil, errSessionDeleted
	}
	ev, err := s.store.appendEvent(ctx, RunEvent{
		SessionID: sessionID,
		RunID:     runID,
		Type:      typ,
		Data:      data,
	})
	if errors.Is(err, ErrRunNotFound) && s.isRunDeleted(runID) {
		return nil, errSessionDeleted
	}
	return ev, err
}

func (s *service) appendFinish(ctx context.Context, sessionID, runID string, reason string, usage *schema.TokenUsage) error {
	_, err := s.appendEvent(ctx, sessionID, runID, EventFinish, FinishData{
		FinishReason: reason,
		Usage:        usage,
	})
	return err
}

func (s *service) newRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := detachContext(ctx)
	if s.runTimeout <= 0 {
		return context.WithCancel(base)
	}
	return context.WithTimeout(base, s.runTimeout)
}

func detachContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	if messages == nil {
		return nil
	}
	out := make([]*schema.Message, len(messages))
	for i := range messages {
		out[i] = cloneMessage(messages[i])
	}
	return out
}

func cloneTokenUsage(usage *schema.TokenUsage) *schema.TokenUsage {
	if usage == nil {
		return nil
	}
	out := *usage
	return &out
}

func (s *service) sessionLock(sessionID string) *sync.Mutex {
	var hash uint32 = 2166136261
	for i := 0; i < len(sessionID); i++ {
		hash ^= uint32(sessionID[i])
		hash *= 16777619
	}
	return &s.sessionLocks[hash%sessionLockShards]
}

func (s *service) registerRunCancel(runID string, cancel context.CancelFunc) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runCancels == nil {
		s.runCancels = make(map[string]context.CancelFunc)
	}
	if s.deletedRuns == nil {
		s.deletedRuns = make(map[string]struct{})
	}
	delete(s.deletedRuns, runID)
	s.runCancels[runID] = cancel
}

func (s *service) unregisterRunCancel(runID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	delete(s.runCancels, runID)
	delete(s.deletedRuns, runID)
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

func (s *service) markRunDeleted(runID string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.deletedRuns == nil {
		s.deletedRuns = make(map[string]struct{})
	}
	s.deletedRuns[runID] = struct{}{}
}

func (s *service) isRunDeleted(runID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	_, ok := s.deletedRuns[runID]
	return ok
}

func newRunID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
