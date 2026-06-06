package einoai

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
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

func TestRequestSnapshotMessagesUsesFullRequestBody(t *testing.T) {
	messages, err := requestSnapshotMessages([]*schema.Message{
		{Role: schema.User, Content: "old"},
		{Role: schema.Assistant, Content: "older answer"},
		{Role: schema.User, Content: "current"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected full request snapshot, got %#v", messages)
	}
	if messages[2].Content != "current" {
		t.Fatalf("expected request order to be preserved, got %#v", messages)
	}
}

func TestServiceGetMessagesReturnsActiveSnapshotWhenRunIsActive(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.replaceSessionMessages(ctx, "s1", []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}); err != nil {
		t.Fatal(err)
	}
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setActiveMessages(ctx, "s1", "r1", []*schema.Message{
		{Role: schema.User, Content: "edited"},
		{Role: schema.User, Content: "current"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &service{store: store}
	messages, err := svc.GetMessages(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected active request snapshot, got %#v", messages)
	}
	if messages[0].Content != "edited" || messages[1].Content != "current" {
		t.Fatalf("expected active snapshot to replace committed history, got %#v", messages)
	}
}

func TestCommitRunMessagesReplacesHistoryWithActiveSnapshotAndOutput(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	if err := store.replaceSessionMessages(ctx, "s1", []*schema.Message{
		{Role: schema.User, Content: "old branch"},
	}); err != nil {
		t.Fatal(err)
	}
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.setActiveMessages(ctx, "s1", "r1", []*schema.Message{
		{Role: schema.User, Content: "edited branch"},
		{Role: schema.User, Content: "current"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.commitRunMessages(ctx, "s1", "r1", []*schema.Message{
		{Role: schema.Assistant, Content: "answer"},
	}); err != nil {
		t.Fatal(err)
	}

	messages, err := store.getSessionMessages(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected active snapshot plus output, got %#v", messages)
	}
	if messages[0].Content != "edited branch" || messages[2].Content != "answer" {
		t.Fatalf("expected committed history to be replaced, got %#v", messages)
	}
}
