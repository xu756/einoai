package einoai

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestNewServiceUsesDefaultRedisTTL(t *testing.T) {
	svc := NewService(nil).(*service)
	if svc.store.ttl != DefaultRedisTTL {
		t.Fatalf("expected default redis ttl %s, got %s", DefaultRedisTTL, svc.store.ttl)
	}
}

func TestNewServiceAcceptsRedisTTLOption(t *testing.T) {
	svc := NewService(nil, WithRedisTTL(12*time.Hour)).(*service)
	if svc.store.ttl != 12*time.Hour {
		t.Fatalf("expected custom redis ttl, got %s", svc.store.ttl)
	}
}

func TestNewServiceAcceptsCompletionErrorHandler(t *testing.T) {
	called := false
	svc := NewService(nil, WithCompletionErrorHandler(func(_ context.Context, sessionID, runID string, err error) {
		called = sessionID == "s1" && runID == "r1" && err != nil
	})).(*service)
	svc.invokeCompletionHook(context.Background(), "s1", "r1", func(context.Context, *RunResult) error {
		return context.Canceled
	}, &RunResult{})
	if !called {
		t.Fatal("completion error handler was not called")
	}
}

func TestCompletionHookPanicIsReported(t *testing.T) {
	var got error
	svc := &service{completionErrorHook: func(_ context.Context, _, _ string, err error) { got = err }}
	svc.invokeCompletionHook(context.Background(), "s1", "r1", func(context.Context, *RunResult) error {
		panic("boom")
	}, &RunResult{})
	if got == nil {
		t.Fatal("expected panic to be reported as hook error")
	}
}

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
	defer func() {
		_ = stream.Close()
	}()

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

func TestAssignSessionMessageIDsPreservesExistingIDs(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Extra: map[string]any{sessionMessageIDExtraKey: "client_1"}},
		{Role: schema.User},
		{Role: schema.User, Extra: map[string]any{"_einoai_ui_id": "ui_1"}},
	}
	assignSessionMessageIDs(messages, "run_1", "input")
	if messages[0].Extra[sessionMessageIDExtraKey] != "client_1" {
		t.Fatalf("existing ID changed: %#v", messages[0].Extra)
	}
	if messages[1].Extra[sessionMessageIDExtraKey] != "msg_run_1_input_1" {
		t.Fatalf("generated ID missing: %#v", messages[1].Extra)
	}
	if messages[2].Extra[sessionMessageIDExtraKey] != "ui_1" {
		t.Fatalf("AI SDK ID was not preserved: %#v", messages[2].Extra)
	}
}

func TestServiceDeleteSessionRemovesRunArtifacts(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	svc := &service{
		store:       store,
		runCancels:  make(map[string]context.CancelFunc),
		deletedRuns: make(map[string]struct{}),
	}
	if err := svc.DeleteSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}

	deletedRun, err := svc.GetRun(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if deletedRun != nil {
		t.Fatalf("expected deleted session run to be empty, got %#v", deletedRun)
	}
}

func TestNewServiceAcceptsRunTimeoutOption(t *testing.T) {
	svc := NewService(nil, WithRunTimeout(90*time.Second)).(*service)
	if svc.runTimeout != 90*time.Second {
		t.Fatalf("expected custom run timeout, got %s", svc.runTimeout)
	}
}

func TestRequestSnapshotMessagesDoesNotMutateSource(t *testing.T) {
	source := &schema.Message{
		Role:    schema.User,
		Content: "hello",
		Extra: map[string]any{
			"custom": map[string]any{"trace": "source"},
		},
	}

	snapshot, err := requestSnapshotMessages([]*schema.Message{source})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot[0] == source {
		t.Fatal("snapshot must not reuse caller message pointers")
	}

	assignSessionMessageIDs(snapshot, "run_1", "input")
	if _, ok := source.Extra[sessionMessageIDExtraKey]; ok {
		t.Fatalf("assigning internal ids mutated caller message: %#v", source.Extra)
	}

	snapshot[0].Content = "changed"
	snapshot[0].Extra["custom"].(map[string]any)["trace"] = "snapshot"
	if source.Content != "hello" {
		t.Fatalf("snapshot content mutation leaked to source: %q", source.Content)
	}
	if source.Extra["custom"].(map[string]any)["trace"] != "source" {
		t.Fatalf("snapshot metadata mutation leaked to source: %#v", source.Extra)
	}
}

func TestRequestSnapshotMessagesClonesMultimodalParts(t *testing.T) {
	url := "https://example.com/source.png"
	source := &schema.Message{
		Role: schema.User,
		MultiContent: []schema.ChatMessagePart{{
			Type: schema.ChatMessagePartTypeImageURL,
			ImageURL: &schema.ChatMessageImageURL{
				URL:   url,
				Extra: map[string]any{"owner": "source"},
			},
		}},
		UserInputMultiContent: []schema.MessageInputPart{{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
				URL:   &url,
				Extra: map[string]any{"owner": "source"},
			}},
		}},
	}

	snapshot, err := requestSnapshotMessages([]*schema.Message{source})
	if err != nil {
		t.Fatal(err)
	}
	legacy := snapshot[0].MultiContent[0].ImageURL
	legacy.URL = "https://example.com/snapshot.png"
	legacy.Extra["owner"] = "snapshot"
	modern := snapshot[0].UserInputMultiContent[0].Image
	*modern.URL = "https://example.com/snapshot-modern.png"
	modern.Extra["owner"] = "snapshot"

	if source.MultiContent[0].ImageURL.URL != url || source.MultiContent[0].ImageURL.Extra["owner"] != "source" {
		t.Fatalf("legacy multimodal mutation leaked to source: %#v", source.MultiContent[0])
	}
	if *source.UserInputMultiContent[0].Image.URL != url || source.UserInputMultiContent[0].Image.Extra["owner"] != "source" {
		t.Fatalf("multimodal mutation leaked to source: %#v", source.UserInputMultiContent[0])
	}
}

func TestServiceGetRunByIDReturnsTerminalRun(t *testing.T) {
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
	found, err := svc.GetRunByID(ctx, "s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Status != RunStatusCompleted {
		t.Fatalf("expected completed run metadata, got %#v", found)
	}
}
