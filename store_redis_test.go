package einoai

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

func newTestRedisStore(t *testing.T) (*redisStore, func()) {
	t.Helper()
	store, cleanup, _ := newTestRedisStoreWithServer(t, DefaultRedisTTL)
	return store, cleanup
}

func newTestRedisStoreWithServer(t *testing.T, ttl time.Duration) (*redisStore, func(), *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	return newRedisStore(rdb, ttl), func() {
		_ = rdb.Close()
		srv.Close()
	}, srv
}

func TestRedisStoreReadsTerminalRunEventsByRunID(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusQueued}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendEvent(ctx, RunEvent{
		SessionID: "s1",
		RunID:     "r1",
		Type:      EventTextDelta,
		Data:      TextData{ID: "text_r1_0", Delta: "hello"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}

	found, err := store.getRunForEvents(ctx, "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.RunID != "r1" || found.Status != RunStatusCompleted {
		t.Fatalf("unexpected run: %#v", found)
	}

	events, err := store.readAfter(ctx, "s1", "r1", "0-0", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != EventTextDelta {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestRedisStoreUsesConfiguredTTL(t *testing.T) {
	store, cleanup, srv := newTestRedisStoreWithServer(t, 3*time.Hour)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusQueued}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceSessionMessages(ctx, "s1", []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}); err != nil {
		t.Fatal(err)
	}

	if ttl := srv.TTL(runMetaKey("s1", "r1")); ttl != 3*time.Hour {
		t.Fatalf("expected custom run meta ttl, got %s", ttl)
	}
	if ttl := srv.TTL(currentRunKey("s1")); ttl != 3*time.Hour {
		t.Fatalf("expected custom current run ttl, got %s", ttl)
	}
	if ttl := srv.TTL(sessionMessagesKey("s1")); ttl != 3*time.Hour {
		t.Fatalf("expected custom messages ttl, got %s", ttl)
	}
}

func TestRedisStoreCanDisableTTL(t *testing.T) {
	store, cleanup, srv := newTestRedisStoreWithServer(t, 0)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusQueued}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceSessionMessages(ctx, "s1", []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}); err != nil {
		t.Fatal(err)
	}

	if ttl := srv.TTL(runMetaKey("s1", "r1")); ttl != 0 {
		t.Fatalf("expected run meta ttl disabled, got %s", ttl)
	}
	if ttl := srv.TTL(currentRunKey("s1")); ttl != 0 {
		t.Fatalf("expected current run ttl disabled, got %s", ttl)
	}
	if ttl := srv.TTL(sessionMessagesKey("s1")); ttl != 0 {
		t.Fatalf("expected messages ttl disabled, got %s", ttl)
	}
}

func TestRedisStoreDeleteSessionRemovesSessionKeys(t *testing.T) {
	store, cleanup, srv := newTestRedisStoreWithServer(t, DefaultRedisTTL)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setActiveMessages(ctx, "s1", "r1", []*schema.Message{
		{Role: schema.User, Content: "active"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceSessionMessages(ctx, "s1", []*schema.Message{
		{Role: schema.User, Content: "history"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendEvent(ctx, RunEvent{
		SessionID: "s1",
		RunID:     "r1",
		Type:      EventTextDelta,
		Data:      TextData{ID: "text_1", Delta: "hello"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceSessionMessages(ctx, "s2", []*schema.Message{
		{Role: schema.User, Content: "other"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.deleteSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{
		currentRunKey("s1"),
		runMetaKey("s1", "r1"),
		runEventsKey("s1", "r1"),
		activeMessagesKey("s1", "r1"),
		sessionMessagesKey("s1"),
	} {
		if srv.Exists(key) {
			t.Fatalf("expected key %s to be deleted", key)
		}
	}
	if !srv.Exists(sessionMessagesKey("s2")) {
		t.Fatal("expected other session history to remain")
	}
}
