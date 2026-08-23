package einoai

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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

func sessionKeysPattern(sessionID string) string {
	return fmt.Sprintf("chat:sessions:%s:*", escapeRedisGlob(sessionID))
}

func escapeRedisGlob(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(value)
}

func (s *redisStore) ttlMilliseconds() int64 {
	if s.ttl <= 0 {
		return 0
	}
	ms := s.ttl.Milliseconds()
	if ms == 0 {
		return 1
	}
	return ms
}

func (s *redisStore) initRun(ctx context.Context, run *RunInfo) error {
	now := time.Now()
	run.CreatedAt = now
	run.UpdatedAt = now

	metadata, err := json.Marshal(run.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	const script = `
if redis.call("EXISTS", KEYS[1]) ~= 0 then
	return 0
end
redis.call("HSET", KEYS[2],
	"session_id", ARGV[1],
	"run_id", ARGV[2],
	"status", ARGV[3],
	"error", ARGV[4],
	"created_at", ARGV[5],
	"updated_at", ARGV[5],
	"metadata", ARGV[6])
redis.call("SET", KEYS[1], ARGV[2])
local ttl = tonumber(ARGV[7])
if ttl and ttl > 0 then
	redis.call("PEXPIRE", KEYS[1], ttl)
	redis.call("PEXPIRE", KEYS[2], ttl)
end
return 1
`
	created, err := s.rdb.Eval(ctx, script, []string{
		currentRunKey(run.SessionID),
		runMetaKey(run.SessionID, run.RunID),
	},
		run.SessionID,
		run.RunID,
		string(run.Status),
		run.Error,
		now.UnixMilli(),
		string(metadata),
		s.ttlMilliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if created == 0 {
		return ErrRunActive
	}
	return nil
}

func (s *redisStore) setRunStatus(ctx context.Context, sessionID, runID string, status RunStatus, errText string) error {
	const script = `
if redis.call("EXISTS", KEYS[1]) == 0 then
	return 0
end
local current = redis.call("HGET", KEYS[1], "status")
if current == "completed" or current == "cancelled" or current == "failed" then
	if current == ARGV[1] then
		return 1
	end
	return -1
end
redis.call("HSET", KEYS[1], "status", ARGV[1], "updated_at", ARGV[2], "error", ARGV[3])
local ttl = tonumber(ARGV[4])
if ttl and ttl > 0 then
	redis.call("PEXPIRE", KEYS[1], ttl)
	if redis.call("GET", KEYS[2]) == ARGV[5] then
		redis.call("PEXPIRE", KEYS[2], ttl)
	end
end
return 1
`
	updated, err := s.rdb.Eval(ctx, script, []string{
		runMetaKey(sessionID, runID),
		currentRunKey(sessionID),
	},
		string(status),
		time.Now().UnixMilli(),
		errText,
		s.ttlMilliseconds(),
		runID,
	).Int()
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrRunNotFound
	}
	if updated < 0 {
		return errRunTerminal
	}
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

func (s *redisStore) deleteRun(ctx context.Context, sessionID, runID string) error {
	if err := s.clearCurrentRunIfMatches(ctx, sessionID, runID); err != nil {
		return err
	}
	return s.rdb.Del(ctx, runMetaKey(sessionID, runID), runEventsKey(sessionID, runID)).Err()
}

func (s *redisStore) deleteSession(ctx context.Context, sessionID string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, sessionKeysPattern(sessionID), 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			filtered := keys[:0]
			for _, key := range keys {
				if belongsToSessionKey(sessionID, key) {
					filtered = append(filtered, key)
				}
			}
			if len(filtered) > 0 {
				if err := s.rdb.Del(ctx, filtered...).Err(); err != nil {
					return err
				}
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

func belongsToSessionKey(sessionID, key string) bool {
	prefix := fmt.Sprintf("chat:sessions:%s:", sessionID)
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(key, prefix)
	if suffix == "current_run" {
		return true
	}
	parts := strings.Split(suffix, ":")
	return len(parts) == 3 && parts[0] == "runs" && parts[1] != "" && (parts[2] == "meta" || parts[2] == "events")
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

	const script = `
if redis.call("EXISTS", KEYS[1]) == 0 then
	return "__missing__"
end
local status = redis.call("HGET", KEYS[1], "status")
if status == "completed" or status == "cancelled" or status == "failed" then
	return "__terminal__"
end
local id = redis.call("XADD", KEYS[2], "*", "data", ARGV[1], "ts", ARGV[2])
local ttl = tonumber(ARGV[3])
if ttl and ttl > 0 then
	redis.call("PEXPIRE", KEYS[1], ttl)
	redis.call("PEXPIRE", KEYS[2], ttl)
	if redis.call("GET", KEYS[3]) == ARGV[4] then
		redis.call("PEXPIRE", KEYS[3], ttl)
	end
end
return id
`
	result, err := s.rdb.Eval(ctx, script, []string{
		runMetaKey(ev.SessionID, ev.RunID),
		runEventsKey(ev.SessionID, ev.RunID),
		currentRunKey(ev.SessionID),
	}, string(body), ev.CreatedAt.UnixMilli(), s.ttlMilliseconds(), ev.RunID).Result()
	if err != nil {
		return nil, err
	}
	id, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("append event returned unexpected result %T", result)
	}
	switch id {
	case "__missing__":
		return nil, ErrRunNotFound
	case "__terminal__":
		return nil, errRunTerminal
	}

	ev.ID = id
	return &ev, nil
}

func (s *redisStore) finishRun(ctx context.Context, ev RunEvent, status RunStatus, errText string) (*RunEvent, error) {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal terminal event data: %w", err)
	}
	body, err := json.Marshal(storedEvent{
		Type:      ev.Type,
		Data:      data,
		CreatedAt: ev.CreatedAt.UnixMilli(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal terminal event: %w", err)
	}

	const script = `
if redis.call("EXISTS", KEYS[1]) == 0 then
	return "__missing__"
end
local current_status = redis.call("HGET", KEYS[1], "status")
if current_status == "completed" or current_status == "cancelled" or current_status == "failed" then
	return "__terminal__"
end
local id = redis.call("XADD", KEYS[2], "*", "data", ARGV[1], "ts", ARGV[2])
redis.call("HSET", KEYS[1], "status", ARGV[3], "updated_at", ARGV[2], "error", ARGV[4])
if redis.call("GET", KEYS[3]) == ARGV[5] then
	redis.call("DEL", KEYS[3])
end
local ttl = tonumber(ARGV[6])
if ttl and ttl > 0 then
	redis.call("PEXPIRE", KEYS[1], ttl)
	redis.call("PEXPIRE", KEYS[2], ttl)
end
return id
`
	result, err := s.rdb.Eval(ctx, script, []string{
		runMetaKey(ev.SessionID, ev.RunID),
		runEventsKey(ev.SessionID, ev.RunID),
		currentRunKey(ev.SessionID),
	}, string(body), ev.CreatedAt.UnixMilli(), string(status), errText, ev.RunID, s.ttlMilliseconds()).Result()
	if err != nil {
		return nil, err
	}
	id, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("finish run returned unexpected result %T", result)
	}
	switch id {
	case "__missing__":
		return nil, ErrRunNotFound
	case "__terminal__":
		return nil, errRunTerminal
	}
	ev.ID = id
	return &ev, nil
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
