package einoai

import (
	"context"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/schema"
)

type runEventBuilder struct {
	service          *service
	sessionID        string
	runID            string
	stepIndex        int
	textID           string
	textStarted      bool
	reasoningID      string
	reasoningStarted bool
	think            thinkSplitter
	finished         bool
	usage            *schema.TokenUsage
	toolCalls        map[int]*toolCallState
	assistantChunks  []*schema.Message
	outputMessages   []*schema.Message
}

type toolCallState struct {
	id               string
	name             string
	pendingArguments string
	started          bool
}

func newRunEventBuilder(service *service, sessionID, runID string) *runEventBuilder {
	b := &runEventBuilder{
		service:   service,
		sessionID: sessionID,
		runID:     runID,
		toolCalls: make(map[int]*toolCallState),
	}
	b.resetIDs()
	return b
}

func (b *runEventBuilder) resetIDs() {
	b.textID = fmt.Sprintf("text_%s_%d", b.runID, b.stepIndex)
	b.reasoningID = fmt.Sprintf("reasoning_%s_%d", b.runID, b.stepIndex)
}

func (b *runEventBuilder) advanceStep() {
	b.stepIndex++
	b.textStarted = false
	b.reasoningStarted = false
	b.think = thinkSplitter{}
	b.toolCalls = make(map[int]*toolCallState)
	b.assistantChunks = nil
	b.resetIDs()
}

func (b *runEventBuilder) writeMessage(ctx context.Context, msg *schema.Message) error {
	if msg == nil {
		return nil
	}
	if shouldCollectAssistantChunk(msg) {
		b.assistantChunks = append(b.assistantChunks, msg)
	}
	if msg.ReasoningContent != "" {
		if err := b.writeReasoning(ctx, msg.ReasoningContent); err != nil {
			return err
		}
	}
	if msg.Content != "" && msg.Role != schema.Tool {
		if err := b.writeContent(ctx, msg.Content); err != nil {
			return err
		}
	}
	if len(msg.ToolCalls) > 0 {
		for i := range msg.ToolCalls {
			if err := b.writeToolCall(ctx, i, msg.ToolCalls[i]); err != nil {
				return err
			}
		}
	}
	if msg.Role == schema.Tool {
		assignSessionMessageID(msg, b.runID, "output", len(b.outputMessages))
		b.outputMessages = append(b.outputMessages, msg)
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventToolResult, ToolResultData{
			ToolCallID: msg.ToolCallID,
			Name:       msg.ToolName,
			Content:    parseMaybeJSON(msg.Content),
		}); err != nil {
			return err
		}
	}
	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.Usage != nil {
			b.usage = msg.ResponseMeta.Usage
		}
		if msg.ResponseMeta.FinishReason != "" {
			return b.writeFinish(ctx, msg.ResponseMeta.FinishReason, msg.ResponseMeta.Usage)
		}
	}
	return nil
}

func shouldCollectAssistantChunk(msg *schema.Message) bool {
	if msg.Role != "" && msg.Role != schema.Assistant {
		return false
	}
	return msg.Content != "" ||
		msg.ReasoningContent != "" ||
		len(msg.ToolCalls) > 0 ||
		len(msg.AssistantGenMultiContent) > 0
}

func (b *runEventBuilder) writeContent(ctx context.Context, content string) error {
	for _, part := range b.think.feed(content) {
		if part.reasoning {
			if err := b.writeReasoning(ctx, part.text); err != nil {
				return err
			}
			continue
		}
		if err := b.writeText(ctx, part.text); err != nil {
			return err
		}
	}
	return nil
}

func (b *runEventBuilder) writeText(ctx context.Context, delta string) error {
	if delta == "" {
		return nil
	}
	if !b.textStarted {
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventTextStart, TextData{ID: b.textID}); err != nil {
			return err
		}
		b.textStarted = true
	}
	_, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventTextDelta, TextData{ID: b.textID, Delta: delta})
	return err
}

func (b *runEventBuilder) writeReasoning(ctx context.Context, delta string) error {
	if delta == "" {
		return nil
	}
	if !b.reasoningStarted {
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventReasoningStart, ReasoningData{ID: b.reasoningID}); err != nil {
			return err
		}
		b.reasoningStarted = true
	}
	_, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventReasoningDelta, ReasoningData{ID: b.reasoningID, Delta: delta})
	return err
}

func (b *runEventBuilder) closeOpenBlocks(ctx context.Context) error {
	if err := b.flushToolCalls(ctx); err != nil {
		return err
	}
	for _, part := range b.think.flush() {
		if part.reasoning {
			if err := b.writeReasoning(ctx, part.text); err != nil {
				return err
			}
		} else if err := b.writeText(ctx, part.text); err != nil {
			return err
		}
	}
	if b.reasoningStarted {
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventReasoningEnd, ReasoningData{ID: b.reasoningID}); err != nil {
			return err
		}
		b.reasoningStarted = false
	}
	if b.textStarted {
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventTextEnd, TextData{ID: b.textID}); err != nil {
			return err
		}
		b.textStarted = false
	}
	return nil
}

func (b *runEventBuilder) writeToolCall(ctx context.Context, position int, tc schema.ToolCall) error {
	index := position
	if tc.Index != nil {
		index = *tc.Index
	}
	st := b.toolCalls[index]
	if st == nil {
		st = &toolCallState{}
		b.toolCalls[index] = st
	}
	if tc.ID != "" {
		st.id = tc.ID
	}
	if tc.Function.Name != "" {
		st.name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		st.pendingArguments += tc.Function.Arguments
	}
	return b.flushToolCall(ctx, index, st)
}

func (b *runEventBuilder) flushToolCalls(ctx context.Context) error {
	indexes := make([]int, 0, len(b.toolCalls))
	for index := range b.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		if err := b.flushToolCall(ctx, index, b.toolCalls[index]); err != nil {
			return err
		}
	}
	return nil
}

func (b *runEventBuilder) flushToolCall(ctx context.Context, index int, st *toolCallState) error {
	if st == nil || (st.started && st.pendingArguments == "") {
		return nil
	}
	if st.id == "" || st.name == "" {
		if st.pendingArguments == "" && !st.started {
			return nil
		}
		return fmt.Errorf("tool call %d missing id or name", index)
	}
	if !st.started {
		if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventToolCall, ToolCallData{
			ID:    st.id,
			Name:  st.name,
			Index: index,
		}); err != nil {
			return err
		}
		st.started = true
	}
	if st.pendingArguments == "" {
		return nil
	}
	arguments := st.pendingArguments
	st.pendingArguments = ""
	_, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventToolCall, ToolCallData{
		ID:        st.id,
		Name:      st.name,
		Arguments: arguments,
		Index:     index,
	})
	return err
}

func (b *runEventBuilder) writeFinish(ctx context.Context, reason string, usage *schema.TokenUsage) error {
	if usage != nil {
		b.usage = usage
	}
	if err := b.closeOpenBlocks(ctx); err != nil {
		return err
	}
	if err := b.commitAssistantMessage(); err != nil {
		return err
	}
	var output []*schema.Message
	if reason != "tool_calls" {
		output = cloneMessages(b.outputMessages)
	}
	if err := b.service.appendFinish(ctx, b.sessionID, b.runID, reason, b.usage, output); err != nil {
		return err
	}
	if reason == "tool_calls" {
		b.advanceStep()
		return nil
	}
	b.finished = true
	return nil
}

func (b *runEventBuilder) commitAssistantMessage() error {
	if len(b.assistantChunks) == 0 {
		return nil
	}
	msg, err := schema.ConcatMessages(b.assistantChunks)
	if err != nil {
		return err
	}
	if msg.Role == "" {
		msg.Role = schema.Assistant
	}
	if b.usage != nil {
		if msg.ResponseMeta == nil {
			msg.ResponseMeta = &schema.ResponseMeta{}
		}
		msg.ResponseMeta.Usage = b.usage
	}
	assignSessionMessageID(msg, b.runID, "output", len(b.outputMessages))
	b.outputMessages = append(b.outputMessages, msg)
	b.assistantChunks = nil
	return nil
}
