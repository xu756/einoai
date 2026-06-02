package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const runTTL = 2 * time.Hour

type RunEvent struct {
	ID   string
	Data string
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

func (s *RunStore) InitRun(ctx context.Context, sessionID, runID, message string) error {
	metaKey := runMetaKey(sessionID, runID)

	if err := s.rdb.HSet(ctx, metaKey, map[string]any{
		"session_id": sessionID,
		"run_id":     runID,
		"message":    message,
		"status":     "running",
		"created_at": time.Now().Unix(),
	}).Err(); err != nil {
		return err
	}

	_ = s.rdb.Expire(ctx, metaKey, runTTL).Err()
	_ = s.rdb.Expire(ctx, runEventsKey(sessionID, runID), runTTL).Err()
	return nil
}

func (s *RunStore) SetRunStatus(ctx context.Context, sessionID, runID, status string) error {
	metaKey := runMetaKey(sessionID, runID)
	return s.rdb.HSet(ctx, metaKey, "status", status, "updated_at", time.Now().Unix()).Err()
}

func (s *RunStore) RunExists(ctx context.Context, sessionID, runID string) (bool, error) {
	n, err := s.rdb.Exists(ctx, runMetaKey(sessionID, runID)).Result()
	return n > 0, err
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
