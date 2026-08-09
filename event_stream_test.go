package einoai

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type xreadCountingHook struct {
	calls int
}

func (h *xreadCountingHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *xreadCountingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if strings.EqualFold(cmd.Name(), "xread") {
			h.calls++
		}
		return next(ctx, cmd)
	}
}

func (h *xreadCountingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRedisEventStreamDoesNotReadAfterContextCancellation(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()
	hook := &xreadCountingHook{}
	store.rdb.AddHook(hook)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := &redisEventStream{store: store, sessionID: "s1", runID: "r1"}
	_, err := stream.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if hook.calls != 0 {
		t.Fatalf("canceled context issued %d XREAD commands", hook.calls)
	}
}

func TestRedisEventStreamResumesAfterEventID(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	first, err := store.appendEvent(ctx, RunEvent{SessionID: "s1", RunID: "r1", Type: EventTextDelta, Data: TextData{Delta: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.appendEvent(ctx, RunEvent{SessionID: "s1", RunID: "r1", Type: EventTextDelta, Data: TextData{Delta: "two"}})
	if err != nil {
		t.Fatal(err)
	}

	stream := newRedisEventStream(store, "s1", "r1", first.ID)
	defer func() { _ = stream.Close() }()
	ev, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != second.ID {
		t.Fatalf("expected resume at %s, got %s", second.ID, ev.ID)
	}
}

func TestRedisEventStreamCloseUnblocksNext(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	stream := newRedisEventStream(store, "s1", "r1", "")
	result := make(chan error, 1)
	go func() {
		_, err := stream.Next(ctx)
		result <- err
	}()

	time.Sleep(20 * time.Millisecond)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF after close, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
}
