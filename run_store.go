package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

const runTTL = 2 * time.Hour

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusDone      RunStatus = "done"
	RunStatusError     RunStatus = "error"
	RunStatusCanceling RunStatus = "canceling"
	RunStatusCanceled  RunStatus = "canceled"
)

type RunEvent struct {
	ID   string
	Data string
}

type RunMeta struct {
	SessionID string    `json:"sessionId"`
	RunID     string    `json:"runId"`
	Message   string    `json:"message"`
	Status    RunStatus `json:"status"`
	CreatedAt int64     `json:"createdAt"`
	UpdatedAt int64     `json:"updatedAt,omitempty"`
}

type StreamEventType string

const (
	StreamEventMessage StreamEventType = "message"
	StreamEventError   StreamEventType = "error"
	StreamEventDone    StreamEventType = "done"
)

type StreamEvent struct {
	Type    StreamEventType `json:"type"`
	Message *schema.Message `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type RunStore struct {
	rdb *redis.Client
}

func NewRunStore(rdb *redis.Client) *RunStore {
	return &RunStore{rdb: rdb}
}

func runEventsKey(sessionID, runID string) string {
	return fmt.Sprintf("chat:sessions:%s:runs:%s:events", sessionID, runID)
}

func runMetaKey(sessionID, runID string) string {
	return fmt.Sprintf("chat:sessions:%s:runs:%s:meta", sessionID, runID)
}

func currentRunKey(sessionID string) string {
	return fmt.Sprintf("chat:sessions:%s:current_run", sessionID)
}

func (s *RunStore) InitRun(ctx context.Context, sessionID, runID, message string) error {
	metaKey := runMetaKey(sessionID, runID)
	now := time.Now().UnixMilli()

	if err := s.rdb.HSet(ctx, metaKey, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"message":    message,
		"status":     string(RunStatusRunning),
		"created_at": now,
	}).Err(); err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, currentRunKey(sessionID), runID, runTTL).Err(); err != nil {
		return err
	}

	_ = s.rdb.Expire(ctx, metaKey, runTTL).Err()
	_ = s.rdb.Expire(ctx, runEventsKey(sessionID, runID), runTTL).Err()
	return nil
}

func (s *RunStore) SetRunStatus(ctx context.Context, sessionID, runID string, status RunStatus) error {
	metaKey := runMetaKey(sessionID, runID)
	if err := s.rdb.HSet(ctx, metaKey, "status", string(status), "updated_at", time.Now().UnixMilli()).Err(); err != nil {
		return err
	}
	if isTerminalRunStatus(status) {
		return s.clearCurrentRunIfMatches(ctx, sessionID, runID)
	}
	_ = s.rdb.Expire(ctx, currentRunKey(sessionID), runTTL).Err()
	_ = s.rdb.Expire(ctx, metaKey, runTTL).Err()
	return nil
}

func (s *RunStore) clearCurrentRunIfMatches(ctx context.Context, sessionID, runID string) error {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	return s.rdb.Eval(ctx, script, []string{currentRunKey(sessionID)}, runID).Err()
}

func (s *RunStore) GetRun(ctx context.Context, sessionID, runID string) (*RunMeta, error) {
	values, err := s.rdb.HGetAll(ctx, runMetaKey(sessionID, runID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}

	return runMetaFromHash(values), nil
}

func (s *RunStore) GetCurrentRun(ctx context.Context, sessionID string) (*RunMeta, error) {
	runID, err := s.rdb.Get(ctx, currentRunKey(sessionID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	run, err := s.GetRun(ctx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		_ = s.rdb.Del(ctx, currentRunKey(sessionID)).Err()
	}

	return run, nil
}

func runMetaFromHash(values map[string]string) *RunMeta {
	createdAt, _ := strconv.ParseInt(values["created_at"], 10, 64)
	updatedAt, _ := strconv.ParseInt(values["updated_at"], 10, 64)

	return &RunMeta{
		SessionID: values["session_id"],
		RunID:     values["run_id"],
		Message:   values["message"],
		Status:    RunStatus(values["status"]),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

func (s *RunStore) Append(ctx context.Context, sessionID, runID, data string) (string, error) {
	key := runEventsKey(sessionID, runID)

	id, err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		ID:     "*",
		Values: map[string]any{
			"data": data,
			"ts":   time.Now().UnixMilli(),
		},
	}).Result()
	if err != nil {
		return "", err
	}

	_ = s.rdb.Expire(ctx, key, runTTL).Err()
	_ = s.rdb.Expire(ctx, runMetaKey(sessionID, runID), runTTL).Err()
	_ = s.rdb.Expire(ctx, currentRunKey(sessionID), runTTL).Err()

	return id, nil
}

func (s *RunStore) ReadAfter(
	ctx context.Context,
	sessionID string,
	runID string,
	lastID string,
	block time.Duration,
	count int64,
) ([]RunEvent, error) {
	if lastID == "" {
		lastID = "0-0"
	}

	key := runEventsKey(sessionID, runID)

	streams, err := s.rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{key, lastID},
		Count:   count,
		Block:   block,
	}).Result()

	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []RunEvent
	for _, st := range streams {
		for _, msg := range st.Messages {
			data, _ := msg.Values["data"].(string)
			out = append(out, RunEvent{
				ID:   msg.ID,
				Data: data,
			})
		}
	}

	return out, nil
}

// ReadAIStream 从 Redis Stream 读取 AISDK 格式事件，供 SSE 使用。
// 每次调用阻塞 block 时长，无新事件则超时返回。
func (s *RunStore) ReadAIStream(
	ctx context.Context,
	sessionID string,
	runID string,
	lastID string,
	block time.Duration,
) ([]RunEvent, error) {
	if lastID == "" {
		lastID = "0-0"
	}

	key := runEventsKey(sessionID, runID)

	streams, err := s.rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{key, lastID},
		Count:   2000,
		Block:   block,
	}).Result()

	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []RunEvent
	for _, st := range streams {
		for _, msg := range st.Messages {
			data, _ := msg.Values["data"].(string)
			out = append(out, RunEvent{
				ID:   msg.ID,
				Data: data,
			})
		}
	}

	return out, nil
}
