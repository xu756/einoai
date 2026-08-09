package einoai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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
	if ttl := srv.TTL(runMetaKey("s1", "r1")); ttl != 3*time.Hour {
		t.Fatalf("expected custom run meta ttl, got %s", ttl)
	}
	if ttl := srv.TTL(currentRunKey("s1")); ttl != 3*time.Hour {
		t.Fatalf("expected custom current run ttl, got %s", ttl)
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
	if ttl := srv.TTL(runMetaKey("s1", "r1")); ttl != 0 {
		t.Fatalf("expected run meta ttl disabled, got %s", ttl)
	}
	if ttl := srv.TTL(currentRunKey("s1")); ttl != 0 {
		t.Fatalf("expected current run ttl disabled, got %s", ttl)
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
	if _, err := store.appendEvent(ctx, RunEvent{
		SessionID: "s1",
		RunID:     "r1",
		Type:      EventTextDelta,
		Data:      TextData{ID: "text_1", Delta: "hello"},
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
	} {
		if srv.Exists(key) {
			t.Fatalf("expected key %s to be deleted", key)
		}
	}
}

func TestRedisStoreInitRunReservesSessionAtomically(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, runID := range []string{"r1", "r2"} {
		runID := runID
		go func() {
			<-start
			results <- store.initRun(ctx, &RunInfo{SessionID: "s1", RunID: runID, Status: RunStatusQueued})
		}()
	}
	close(start)

	var successes, conflicts int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRunActive):
			conflicts++
		default:
			t.Fatalf("unexpected init error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected one reservation and one conflict, got success=%d conflict=%d", successes, conflicts)
	}
}

func TestRedisStoreDeleteSessionEscapesGlobCharacters(t *testing.T) {
	store, cleanup, srv := newTestRedisStoreWithServer(t, DefaultRedisTTL)
	defer cleanup()

	ctx := context.Background()
	literal := &RunInfo{SessionID: "tenant*", RunID: "r1", Status: RunStatusRunning}
	other := &RunInfo{SessionID: "tenant123", RunID: "r2", Status: RunStatusRunning}
	if err := store.initRun(ctx, literal); err != nil {
		t.Fatal(err)
	}
	if err := store.initRun(ctx, other); err != nil {
		t.Fatal(err)
	}

	if err := store.deleteSession(ctx, "tenant*"); err != nil {
		t.Fatal(err)
	}
	if srv.Exists(runMetaKey("tenant*", "r1")) || srv.Exists(currentRunKey("tenant*")) {
		t.Fatal("literal wildcard session keys were not deleted")
	}
	if !srv.Exists(runMetaKey("tenant123", "r2")) || !srv.Exists(currentRunKey("tenant123")) {
		t.Fatal("deleting wildcard-like session id removed another session")
	}
}

func TestRedisStoreDeleteSessionDoesNotDeleteColonPrefixedSession(t *testing.T) {
	store, cleanup, srv := newTestRedisStoreWithServer(t, DefaultRedisTTL)
	defer cleanup()

	ctx := context.Background()
	parent := &RunInfo{SessionID: "tenant", RunID: "r1", Status: RunStatusRunning}
	child := &RunInfo{SessionID: "tenant:child", RunID: "r2", Status: RunStatusRunning}
	if err := store.initRun(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := store.initRun(ctx, child); err != nil {
		t.Fatal(err)
	}

	if err := store.deleteSession(ctx, "tenant"); err != nil {
		t.Fatal(err)
	}
	if srv.Exists(runMetaKey("tenant", "r1")) || srv.Exists(currentRunKey("tenant")) {
		t.Fatal("parent session keys were not deleted")
	}
	if !srv.Exists(runMetaKey("tenant:child", "r2")) || !srv.Exists(currentRunKey("tenant:child")) {
		t.Fatal("deleting parent-like session id removed colon-prefixed session")
	}
}

func TestSetRunStatusDoesNotRecreateDeletedRun(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.deleteSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCompleted, ""); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected missing run error, got %v", err)
	}
	found, err := store.getRun(ctx, "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatalf("deleted run was recreated: %#v", found)
	}
}

func TestRedisStoreTerminalStatusIsIrreversible(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCompleted, ""); !errors.Is(err, errRunTerminal) {
		t.Fatalf("expected terminal transition rejection, got %v", err)
	}
	found, err := store.getRun(ctx, "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Status != RunStatusCancelled {
		t.Fatalf("terminal state was overwritten: %#v", found)
	}
}

func TestRedisStoreRejectsEventsAfterTerminalStatus(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	_, err := store.appendEvent(ctx, RunEvent{SessionID: "s1", RunID: "r1", Type: EventTextDelta, Data: TextData{Delta: "late"}})
	if !errors.Is(err, errRunTerminal) {
		t.Fatalf("expected terminal event rejection, got %v", err)
	}
}
