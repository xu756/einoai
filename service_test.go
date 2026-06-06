package einoai

import (
	"context"
	"testing"
)

func TestServiceGetRunHidesTerminalCurrentRun(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusQueued}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setRunStatus(ctx, "s1", "r1", RunStatusCompleted, ""); err != nil {
		t.Fatal(err)
	}

	svc := &service{store: store}
	found, err := svc.GetRun(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatalf("expected terminal current run to be hidden, got %#v", found)
	}

	current, err := store.getCurrentRun(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if current != nil {
		t.Fatalf("expected terminal current run pointer to be cleared, got %#v", current)
	}
}

func TestServiceSubscribeEventsUsesRunID(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
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
		Data:      TextData{ID: "text_r1_0", Delta: "hello"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &service{store: store}
	if _, err := svc.SubscribeEvents(ctx, SubscribeRequest{SessionID: "s1"}); err == nil {
		t.Fatal("expected missing runID error")
	}

	stream, err := svc.SubscribeEvents(ctx, SubscribeRequest{SessionID: "s1", RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	ev, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != EventTextDelta {
		t.Fatalf("expected text delta event, got %#v", ev)
	}
}
