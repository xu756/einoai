package einoai

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRunEventBuilderConvertsToolCallChunksToCompleteEvents(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	index := 0
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index: &index,
			ID:    "call_00_P6ma2c1021vGwXNjT4gp7549",
			Type:  "function",
			Function: schema.FunctionCall{
				Name: "get_weather",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index: &index,
			Type:  "function",
			Function: schema.FunctionCall{
				Arguments: `{"location":"北京"}`,
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "tool_calls",
		},
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.readAfter(ctx, "s1", "r1", "0-0", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var toolEvents []ToolCallData
	for _, ev := range events {
		if ev.Type != EventToolCall {
			continue
		}
		data, ok := DecodeEventData[ToolCallData](ev)
		if !ok {
			t.Fatalf("decode tool call event: %#v", ev)
		}
		toolEvents = append(toolEvents, data)
	}
	if len(toolEvents) != 2 {
		t.Fatalf("expected start and delta tool events, got %#v", toolEvents)
	}
	for _, data := range toolEvents {
		if data.ID != "call_00_P6ma2c1021vGwXNjT4gp7549" {
			t.Fatalf("unexpected tool call id: %#v", data)
		}
		if data.Name != "get_weather" {
			t.Fatalf("unexpected tool name: %#v", data)
		}
	}
	if toolEvents[1].Arguments != `{"location":"北京"}` {
		t.Fatalf("unexpected tool arguments: %#v", toolEvents[1])
	}
}

func TestRunEventBuilderPreservesUsageOnCommittedAssistantMessage(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	usage := &schema.TokenUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	if err := builder.writeMessage(ctx, &schema.Message{
		Role:    schema.Assistant,
		Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeMessage(ctx, &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage:        usage,
		},
	}); err != nil {
		t.Fatal(err)
	}
	messages := builder.outputMessages
	if len(messages) != 1 {
		t.Fatalf("expected committed assistant message, got %#v", messages)
	}
	if messages[0].ResponseMeta == nil || messages[0].ResponseMeta.Usage == nil {
		t.Fatalf("expected committed assistant message to preserve usage, got %#v", messages[0].ResponseMeta)
	}
	if messages[0].ResponseMeta.Usage.TotalTokens != usage.TotalTokens {
		t.Fatalf("expected total tokens %d, got %#v", usage.TotalTokens, messages[0].ResponseMeta.Usage)
	}
	if messages[0].Extra[sessionMessageIDExtraKey] != "msg_r1_output_0" {
		t.Fatalf("expected generated assistant message ID, got %#v", messages[0].Extra)
	}
}

func TestRunEventBuilderAccumulatesUsageAcrossModelSteps(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	steps := []struct {
		reason string
		usage  *schema.TokenUsage
	}{
		{reason: "tool_calls", usage: &schema.TokenUsage{
			PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 1},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 1},
		}},
		{reason: "stop", usage: &schema.TokenUsage{
			PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8,
			PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 2},
			CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2},
		}},
	}
	for _, step := range steps {
		if err := builder.writeMessage(ctx, &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{
			FinishReason: step.reason,
			Usage:        step.usage,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if builder.usage == nil || builder.usage.PromptTokens != 8 || builder.usage.CompletionTokens != 5 || builder.usage.TotalTokens != 13 {
		t.Fatalf("usage was not accumulated: %#v", builder.usage)
	}
	if builder.usage.PromptTokenDetails.CachedTokens != 3 || builder.usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage details were not accumulated: %#v", builder.usage)
	}
}

func TestRunEventBuilderFallbackFinishDoesNotDoubleCompletedStepUsage(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := store.initRun(ctx, &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	if err := builder.writeMessage(ctx, &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{
		FinishReason: "tool_calls",
		Usage:        &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := builder.writeFinish(ctx, "stop", builder.usage); err != nil {
		t.Fatal(err)
	}
	if builder.usage == nil || builder.usage.TotalTokens != 5 {
		t.Fatalf("fallback finish double-counted usage: %#v", builder.usage)
	}
}

func TestRunEventBuilderPreservesTerminalFinishReason(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := store.initRun(ctx, &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	builder := newRunEventBuilder(&service{store: store}, "s1", "r1")
	if err := builder.writeFinish(ctx, "length", nil); err != nil {
		t.Fatal(err)
	}
	result := newRunResult(&RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusCompleted}, nil, builder)
	if result.FinishReason != "length" {
		t.Fatalf("terminal finish reason was lost: %#v", result)
	}
}

func TestAssignSessionMessageIDUsesOutputNamespace(t *testing.T) {
	message := &schema.Message{Role: schema.Tool}
	assignSessionMessageID(message, "run_1", "output", 0)
	if message.Extra[sessionMessageIDExtraKey] != "msg_run_1_output_0" {
		t.Fatalf("generated output ID missing: %#v", message)
	}
}

func TestFinishFailedCommitsPartialAssistantIntoFinishOutput(t *testing.T) {
	store, cleanup := newTestRedisStore(t)
	defer cleanup()

	ctx := context.Background()
	run := &RunInfo{SessionID: "s1", RunID: "r1", Status: RunStatusRunning}
	if err := store.initRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	svc := &service{store: store}
	builder := newRunEventBuilder(svc, "s1", "r1")
	if err := builder.writeMessage(ctx, &schema.Message{Role: schema.Assistant, Content: "partial"}); err != nil {
		t.Fatal(err)
	}

	svc.finishFailed(ctx, builder, "s1", "r1", context.DeadlineExceeded)

	events, err := store.readAfter(ctx, "s1", "r1", "0-0", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != EventFinish {
			continue
		}
		data, ok := DecodeEventData[FinishData](ev)
		if !ok {
			t.Fatalf("failed to decode finish event: %#v", ev)
		}
		if data.FinishReason != "error" || len(data.Output) != 1 || data.Output[0].Content != "partial" {
			t.Fatalf("partial assistant message missing from failed run output: %#v", data)
		}
		return
	}
	t.Fatal("failed run did not persist a finish event")
}
