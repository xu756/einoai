package enioai

import (
	"context"
	"fmt"

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
}

func newRunEventBuilder(service *service, sessionID, runID string) *runEventBuilder {
	b := &runEventBuilder{
		service:   service,
		sessionID: sessionID,
		runID:     runID,
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
	b.resetIDs()
}

func (b *runEventBuilder) writeMessage(ctx context.Context, msg *schema.Message) error {
	if msg == nil {
		return nil
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
		for i, tc := range msg.ToolCalls {
			index := i
			if tc.Index != nil {
				index = *tc.Index
			}
			if _, err := b.service.appendEvent(ctx, b.sessionID, b.runID, EventToolCall, ToolCallData{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
				Index:     index,
			}); err != nil {
				return err
			}
		}
	}
	if msg.Role == schema.Tool {
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

func (b *runEventBuilder) writeFinish(ctx context.Context, reason string, usage *schema.TokenUsage) error {
	if usage != nil {
		b.usage = usage
	}
	if err := b.closeOpenBlocks(ctx); err != nil {
		return err
	}
	if err := b.service.appendFinish(ctx, b.sessionID, b.runID, reason, b.usage); err != nil {
		return err
	}
	if reason == "tool_calls" {
		b.advanceStep()
		return nil
	}
	b.finished = true
	return nil
}
