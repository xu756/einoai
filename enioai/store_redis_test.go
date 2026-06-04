package enioai

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisStore(t *testing.T) (*redisStore, func()) {
	t.Helper()
	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	return newRedisStore(rdb), func() {
		_ = rdb.Close()
		srv.Close()
	}
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
