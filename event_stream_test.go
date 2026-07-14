package einoai

import (
	"context"
	"errors"
	"strings"
	"testing"

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
