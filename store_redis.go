package einoai

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

type redisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

type storedEvent struct {
	Type      EventType       `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt int64           `json:"createdAt"`
}

func newRedisStore(rdb *redis.Client, ttl time.Duration) *redisStore {
	return &redisStore{rdb: rdb, ttl: ttl}
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

func sessionMessagesKey(sessionID string) string {
	return fmt.Sprintf("chat:sessions:%s:messages", sessionID)
}

func activeMessagesKey(sessionID, runID string) string {
	return fmt.Sprintf("chat:sessions:%s:runs:%s:active_messages", sessionID, runID)
}

func sessionKeysPattern(sessionID string) string {
	return fmt.Sprintf("chat:sessions:%s:*", sessionID)
}

func (s *redisStore) initRun(ctx context.Context, run *RunInfo) error {
	now := time.Now()
	run.CreatedAt = now
	run.UpdatedAt = now

	metadata, err := json.Marshal(run.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	metaKey := runMetaKey(run.SessionID, run.RunID)
	if err := s.rdb.HSet(ctx, metaKey, map[string]any{
		"session_id": run.SessionID,
		"run_id":     run.RunID,
		"status":     string(run.Status),
		"error":      run.Error,
		"created_at": now.UnixMilli(),
		"updated_at": now.UnixMilli(),
		"metadata":   string(metadata),
	}).Err(); err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, currentRunKey(run.SessionID), run.RunID, s.setExpiration()).Err(); err != nil {
		return err
	}

	s.expire(ctx, metaKey)
	s.expire(ctx, runEventsKey(run.SessionID, run.RunID))
	return nil
}

func (s *redisStore) setRunStatus(ctx context.Context, sessionID, runID string, status RunStatus, errText string) error {
	metaKey := runMetaKey(sessionID, runID)
	now := time.Now().UnixMilli()
	if err := s.rdb.HSet(ctx, metaKey, "status", string(status), "updated_at", now, "error", errText).Err(); err != nil {
		return err
	}
	s.expire(ctx, currentRunKey(sessionID))
	s.expire(ctx, metaKey)
	return nil
}

func (s *redisStore) clearCurrentRunIfMatches(ctx context.Context, sessionID, runID string) error {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	return s.rdb.Eval(ctx, script, []string{currentRunKey(sessionID)}, runID).Err()
}

func (s *redisStore) getRun(ctx context.Context, sessionID, runID string) (*RunInfo, error) {
	values, err := s.rdb.HGetAll(ctx, runMetaKey(sessionID, runID)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	return runInfoFromHash(values), nil
}

func (s *redisStore) getCurrentRun(ctx context.Context, sessionID string) (*RunInfo, error) {
	runID, err := s.rdb.Get(ctx, currentRunKey(sessionID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run, err := s.getRun(ctx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		_ = s.rdb.Del(ctx, currentRunKey(sessionID)).Err()
	}
	return run, nil
}

func (s *redisStore) getSessionMessages(ctx context.Context, sessionID string) ([]*schema.Message, error) {
	values, err := s.rdb.LRange(ctx, sessionMessagesKey(sessionID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	messages := make([]*schema.Message, 0, len(values))
	for _, value := range values {
		var msg schema.Message
		if err := json.Unmarshal([]byte(value), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal session message: %w", err)
		}
		messages = append(messages, &msg)
	}
	return messages, nil
}

func (s *redisStore) setActiveMessages(ctx context.Context, sessionID, runID string, messages []*schema.Message) error {
	data, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("marshal active messages: %w", err)
	}
	key := activeMessagesKey(sessionID, runID)
	if err := s.rdb.Set(ctx, key, string(data), s.setExpiration()).Err(); err != nil {
		return err
	}
	return nil
}

func (s *redisStore) getActiveMessages(ctx context.Context, sessionID, runID string) ([]*schema.Message, error) {
	value, err := s.rdb.Get(ctx, activeMessagesKey(sessionID, runID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var messages []*schema.Message
	if err := json.Unmarshal([]byte(value), &messages); err != nil {
		return nil, fmt.Errorf("unmarshal active messages: %w", err)
	}
	return messages, nil
}

func (s *redisStore) replaceSessionMessages(ctx context.Context, sessionID string, messages []*schema.Message) error {
	values := make([]any, 0, len(messages))
	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("marshal session message: %w", err)
		}
		values = append(values, string(data))
	}
	key := sessionMessagesKey(sessionID)
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, key)
	if len(values) > 0 {
		pipe.RPush(ctx, key, values...)
	}
	if s.ttl > 0 {
		pipe.Expire(ctx, key, s.ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

func (s *redisStore) commitRunMessages(ctx context.Context, sessionID, runID string, outputMessages []*schema.Message) error {
	active, err := s.getActiveMessages(ctx, sessionID, runID)
	if err != nil {
		return err
	}
	messages := append(active, outputMessages...)
	if err := s.replaceSessionMessages(ctx, sessionID, messages); err != nil {
		return err
	}
	_ = s.rdb.Del(ctx, activeMessagesKey(sessionID, runID)).Err()
	return nil
}

func (s *redisStore) deleteSession(ctx context.Context, sessionID string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, sessionKeysPattern(sessionID), 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func (s *redisStore) getRunForEvents(ctx context.Context, sessionID, runID string) (*RunInfo, error) {
	current, err := s.getCurrentRun(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if current != nil {
		if current.RunID != runID {
			return nil, nil
		}
		return current, nil
	}
	return s.getRun(ctx, sessionID, runID)
}

func runInfoFromHash(values map[string]string) *RunInfo {
	createdAt, _ := strconv.ParseInt(values["created_at"], 10, 64)
	updatedAt, _ := strconv.ParseInt(values["updated_at"], 10, 64)

	var metadata map[string]any
	if values["metadata"] != "" {
		_ = json.Unmarshal([]byte(values["metadata"]), &metadata)
	}

	return &RunInfo{
		SessionID: values["session_id"],
		RunID:     values["run_id"],
		Status:    RunStatus(values["status"]),
		CreatedAt: time.UnixMilli(createdAt),
		UpdatedAt: time.UnixMilli(updatedAt),
		Error:     values["error"],
		Metadata:  metadata,
	}
}

func (s *redisStore) appendEvent(ctx context.Context, ev RunEvent) (*RunEvent, error) {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}

	data, err := json.Marshal(ev.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal event data: %w", err)
	}
	body, err := json.Marshal(storedEvent{
		Type:      ev.Type,
		Data:      data,
		CreatedAt: ev.CreatedAt.UnixMilli(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}

	id, err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: runEventsKey(ev.SessionID, ev.RunID),
		ID:     "*",
		Values: map[string]any{
			"data": string(body),
			"ts":   ev.CreatedAt.UnixMilli(),
		},
	}).Result()
	if err != nil {
		return nil, err
	}

	ev.ID = id
	s.expire(ctx, runEventsKey(ev.SessionID, ev.RunID))
	s.expire(ctx, runMetaKey(ev.SessionID, ev.RunID))
	s.expire(ctx, currentRunKey(ev.SessionID))
	return &ev, nil
}

func (s *redisStore) setExpiration() time.Duration {
	if s.ttl <= 0 {
		return 0
	}
	return s.ttl
}

func (s *redisStore) expire(ctx context.Context, key string) {
	if s.ttl <= 0 {
		return
	}
	_ = s.rdb.Expire(ctx, key, s.ttl).Err()
}

func (s *redisStore) readAfter(ctx context.Context, sessionID, runID, lastID string, block time.Duration, count int64) ([]*RunEvent, error) {
	if lastID == "" {
		lastID = "0-0"
	}
	streams, err := s.rdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{runEventsKey(sessionID, runID), lastID},
		Count:   count,
		Block:   block,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []*RunEvent
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			data, _ := msg.Values["data"].(string)
			ev, err := decodeStoredEvent(sessionID, runID, msg.ID, data)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
	}
	return out, nil
}

func decodeStoredEvent(sessionID, runID, id, data string) (*RunEvent, error) {
	var stored storedEvent
	if err := json.Unmarshal([]byte(data), &stored); err != nil {
		return nil, err
	}
	var payload any
	if len(stored.Data) > 0 && string(stored.Data) != "null" {
		var m map[string]any
		if err := json.Unmarshal(stored.Data, &m); err != nil {
			return nil, err
		}
		payload = m
	}
	return &RunEvent{
		ID:        id,
		SessionID: sessionID,
		RunID:     runID,
		Type:      stored.Type,
		Data:      payload,
		CreatedAt: time.UnixMilli(stored.CreatedAt),
	}, nil
}
