package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRunStore(t *testing.T) (*RunStore, func()) {
	t.Helper()

	srv := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: srv.Addr()})

	return NewRunStore(rdb), func() {
		_ = rdb.Close()
		srv.Close()
	}
}

func TestRunStoreKeepsOnlyCurrentRunForSession(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()

	if err := store.InitRun(ctx, "session-1", "run-old", "old message"); err != nil {
		t.Fatal(err)
	}
	if err := store.InitRun(ctx, "session-1", "run-new", "new message"); err != nil {
		t.Fatal(err)
	}

	run, err := store.GetCurrentRun(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != "run-new" || run.SessionID != "session-1" || run.Message != "new message" || run.Status != "running" {
		t.Fatalf("unexpected run metadata: %#v", run)
	}

	if err := store.rdb.Del(ctx, currentRunKey("session-1")).Err(); err != nil {
		t.Fatal(err)
	}

	run, err = store.GetCurrentRun(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if run != nil {
		t.Fatalf("expected no running run, got %#v", run)
	}
}

func TestRunStoreReturnsNilWhenCurrentRunMetaExpired(t *testing.T) {
	store, cleanup := newTestRunStore(t)
	defer cleanup()

	ctx := context.Background()

	if err := store.InitRun(ctx, "session-1", "run-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := store.rdb.Del(ctx, runMetaKey("session-1", "run-1")).Err(); err != nil {
		t.Fatal(err)
	}

	run, err := store.GetCurrentRun(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if run != nil {
		t.Fatalf("expected nil run, got %#v", run)
	}
}
