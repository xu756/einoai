package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
)

type AgentManager struct {
	runStore   *RunStore
	model      model.ToolCallingChatModel
	runMu      sync.Mutex
	runCancels map[string]context.CancelFunc
}

func NewAgentManager(ctx context.Context, rdb *redis.Client) (*AgentManager, error) {
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   os.Getenv("MODEL_NAME"),
	})

	if err != nil {
		return nil, err
	}

	return &AgentManager{
		model:      cm,
		runStore:   NewRunStore(rdb),
		runCancels: make(map[string]context.CancelFunc),
	}, nil
}

func (m *AgentManager) NewChatModelAgent(ctx context.Context) (adk.Agent, error) {
	weatherTool, err := NewWeatherTool(ctx)
	if err != nil {
		return nil, err
	}
	calculatorTool, err := NewCalculatorTool(ctx)
	if err != nil {
		return nil, err
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Model: m.model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool, calculatorTool},
			},
		},
	})
}

func (m *AgentManager) NewDeepAgent(ctx context.Context) (adk.Agent, error) {
	weatherTool, err := NewWeatherTool(ctx)
	if err != nil {
		return nil, err
	}
	calculatorTool, err := NewCalculatorTool(ctx)
	if err != nil {
		return nil, err
	}
	researchAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Model:       m.model,
		Instruction: "You are a research expert. Provide detailed information on requested topics.",
	})
	if err != nil {
		return nil, err
	}

	codeAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Model:       m.model,
		Instruction: "You are a coding expert. Write and review code based on requirements.",
	})
	if err != nil {
		return nil, err
	}

	return deep.New(ctx, &deep.Config{
		ChatModel: m.model,
		SubAgents: []adk.Agent{researchAgent, codeAgent},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool, calculatorTool},
			},
		},
	})
}

func (m *AgentManager) NewRunner(ctx context.Context, agent adk.Agent) *adk.Runner {
	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})
}

type AgentKind string

const (
	AgentKindChat AgentKind = "chat"
	AgentKindDeep AgentKind = "deep"
)

func newRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StartRun creates a run for OpenAI API protocol.
func (m *AgentManager) StartRun(ctx context.Context, sessionID string, messages []*schema.Message, kind AgentKind) (string, error) {
	runID := newRunID()

	lastMessageText := ""
	if len(messages) > 0 {
		lastMessageText = messages[len(messages)-1].Content
	}

	if err := m.runStore.InitRun(ctx, sessionID, runID, lastMessageText); err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.registerRunCancel(runID, cancel)

	go m.executeRun(runCtx, cancel, sessionID, runID, messages, kind)

	return runID, nil
}

// CancelSessionRun cancels the current run for a session by runId.
func (m *AgentManager) CancelSessionRun(ctx context.Context, sessionID string, runID string) (*RunMeta, bool, error) {
	run, err := m.runStore.GetCurrentRun(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	if run == nil {
		return nil, false, nil
	}
	if run.RunID != runID {
		return nil, false, nil
	}
	if isTerminalRunStatus(run.Status) {
		return nil, false, nil
	}

	if err := m.runStore.SetRunStatus(ctx, run.SessionID, run.RunID, RunStatusCanceling); err != nil {
		return nil, false, err
	}

	if m.cancelActiveRun(run.RunID) {
		run.Status = RunStatusCanceling
		return run, true, nil
	}

	_, _ = m.runStore.Append(ctx, run.SessionID, run.RunID, "[DONE]")
	if err := m.runStore.SetRunStatus(ctx, run.SessionID, run.RunID, RunStatusCanceled); err != nil {
		return nil, false, err
	}
	run.Status = RunStatusCanceled
	return run, true, nil
}

func (m *AgentManager) registerRunCancel(runID string, cancel context.CancelFunc) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.runCancels == nil {
		m.runCancels = make(map[string]context.CancelFunc)
	}
	m.runCancels[runID] = cancel
}

func (m *AgentManager) unregisterRunCancel(runID string) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	delete(m.runCancels, runID)
}

func (m *AgentManager) cancelActiveRun(runID string) bool {
	m.runMu.Lock()
	cancel := m.runCancels[runID]
	m.runMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func isTerminalRunStatus(status RunStatus) bool {
	return status == RunStatusDone || status == RunStatusError || status == RunStatusCanceled
}

// StartAIRun creates a run for AI SDK protocol.
func (m *AgentManager) StartAIRun(ctx context.Context, sessionID string, messages []*schema.Message, kind AgentKind) (string, error) {
	runID := newRunID()

	lastMessageText := ""
	if len(messages) > 0 {
		lastMessageText = messages[len(messages)-1].Content
	}

	if err := m.runStore.InitRun(ctx, sessionID, runID, lastMessageText); err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.registerRunCancel(runID, cancel)

	go m.executeAIRun(runCtx, cancel, sessionID, runID, messages, kind)

	return runID, nil
}

// executeRun runs the agent for OpenAI API protocol (writes schema.Message to Redis).
func (m *AgentManager) executeRun(
	ctx context.Context,
	cancel context.CancelFunc,
	sessionID string,
	runID string,
	messages []*schema.Message,
	kind AgentKind,
) {
	defer cancel()
	defer m.unregisterRunCancel(runID)

	_ = m.runStore.SetRunStatus(ctx, sessionID, runID, RunStatusRunning)

	var (
		ag  adk.Agent
		err error
	)

	switch kind {
	case AgentKindDeep:
		ag, err = m.NewDeepAgent(ctx)
	default:
		ag, err = m.NewChatModelAgent(ctx)
	}

	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			m.finishRunCanceled(sessionID, runID)
			return
		}
		m.writeRunError(context.Background(), sessionID, runID, err)
		return
	}

	runner := m.NewRunner(ctx, ag)
	iter := runner.Run(ctx, messages)

	if err := m.streamToRedis(ctx, iter, sessionID, runID); err != nil {
		if errors.Is(err, context.Canceled) {
			m.finishRunCanceled(sessionID, runID)
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			m.writeRunError(context.Background(), sessionID, runID, err)
			return
		}
		_ = m.runStore.SetRunStatus(context.Background(), sessionID, runID, RunStatusError)
		return
	}

	_ = m.runStore.SetRunStatus(context.Background(), sessionID, runID, RunStatusDone)
}

// executeAIRun runs the agent for AI SDK protocol (writes schema.Message to Redis).
func (m *AgentManager) executeAIRun(
	ctx context.Context,
	cancel context.CancelFunc,
	sessionID string,
	runID string,
	messages []*schema.Message,
	kind AgentKind,
) {
	// Re-uses the exact same logic as executeRun because both now just stream raw schema.Message to Redis
	m.executeRun(ctx, cancel, sessionID, runID, messages, kind)
}

func (m *AgentManager) streamToRedis(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], sessionID, runID string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		event, ok := iter.Next()
		if !ok {
			b, _ := json.Marshal(StreamEvent{Type: StreamEventDone})
			_, _ = m.runStore.Append(context.Background(), sessionID, runID, string(b))
			return nil
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			b, _ := json.Marshal(StreamEvent{Type: StreamEventError, Error: event.Err.Error()})
			_, _ = m.runStore.Append(context.Background(), sessionID, runID, string(b))
			return event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					b, _ := json.Marshal(StreamEvent{Type: StreamEventError, Error: err.Error()})
					_, _ = m.runStore.Append(context.Background(), sessionID, runID, string(b))
					return err
				}
				if msg != nil {
					b, _ := json.Marshal(StreamEvent{Type: StreamEventMessage, Message: msg})
					_, _ = m.runStore.Append(context.Background(), sessionID, runID, string(b))
				}
			}
			continue
		}
		if mv.Message != nil {
			b, _ := json.Marshal(StreamEvent{Type: StreamEventMessage, Message: mv.Message})
			_, _ = m.runStore.Append(context.Background(), sessionID, runID, string(b))
		}
	}
}

// streamAISDKToSink writes AISDK format events from the iterator to the sink.
func (m *AgentManager) streamAISDKToSink(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], sink AISDKSink) error {
	state := &useChatStreamState{
		messageID:         fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		textID:            fmt.Sprintf("text_%d", time.Now().UnixNano()),
		reasoningID:       fmt.Sprintf("reasoning_%d", time.Now().UnixNano()),
		toolCalls:         make(map[string]*toolCallState),
		toolCallIndexToID: make(map[int]string),
	}

	sink.WritePart(map[string]any{"type": "start", "messageId": state.messageID})
	sink.WritePart(map[string]any{"type": "start-step"})
	state.stepStarted = true

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		event, ok := iter.Next()
		if !ok {
			finishOpenBlocksSink(sink, state)
			sink.WritePart(map[string]any{"type": "finish-step"})
			sink.WritePart(map[string]any{"type": "finish"})
			sink.Done()
			return nil
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			sink.WritePart(map[string]any{"type": "error", "errorText": event.Err.Error()})
			sink.WritePart(map[string]any{"type": "finish"})
			sink.Done()
			return event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		mv := event.Output.MessageOutput
		if mv.IsStreaming && mv.MessageStream != nil {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				msg, err := mv.MessageStream.Recv()
				if err != nil {
					if err == io.EOF {
						break
					}
					sink.WritePart(map[string]any{"type": "error", "errorText": err.Error()})
					sink.Done()
					return err
				}
				if msg == nil {
					continue
				}
				writeEinoMsgAsAISDKPartsSink(sink, state, msg)
			}
			continue
		}
		if mv.Message != nil {
			writeEinoMsgAsAISDKPartsSink(sink, state, mv.Message)
		}
	}
}

func finishOpenBlocksSink(sink AISDKSink, state *useChatStreamState) {
	if state.reasoningStarted {
		sink.WritePart(map[string]any{"type": "reasoning-end", "id": state.reasoningID})
		state.reasoningStarted = false
	}
	if state.textStarted {
		sink.WritePart(map[string]any{"type": "text-end", "id": state.textID})
		state.textStarted = false
	}
}

func writeEinoMsgAsAISDKPartsSink(sink AISDKSink, state *useChatStreamState, msg *schema.Message) {
	if msg == nil {
		return
	}
	if msg.ReasoningContent != "" {
		if !state.reasoningStarted {
			sink.WritePart(map[string]any{"type": "reasoning-start", "id": state.reasoningID})
			state.reasoningStarted = true
		}
		sink.WritePart(map[string]any{"type": "reasoning-delta", "id": state.reasoningID, "delta": msg.ReasoningContent})
	}
	if msg.Content != "" && msg.Role != schema.Tool {
		if !state.textStarted {
			sink.WritePart(map[string]any{"type": "text-start", "id": state.textID})
			state.textStarted = true
		}
		sink.WritePart(map[string]any{"type": "text-delta", "id": state.textID, "delta": msg.Content})
	}
	if len(msg.ToolCalls) > 0 {
		for i, tc := range msg.ToolCalls {
			index := i
			if tc.Index != nil {
				index = *tc.Index
			}
			callID := tc.ID
			if callID != "" {
				state.toolCallIndexToID[index] = callID
			}
			if callID == "" {
				if savedID, ok := state.toolCallIndexToID[index]; ok && savedID != "" {
					callID = savedID
				}
			}
			if callID == "" {
				callID = fmt.Sprintf("tool_call_%d", index)
				state.toolCallIndexToID[index] = callID
			}
			st := state.toolCalls[callID]
			if st == nil {
				st = &toolCallState{ID: callID}
				state.toolCalls[callID] = st
			}
			if tc.Function.Name != "" {
				st.Name = tc.Function.Name
			}
			if st.Name == "" {
				st.Name = "tool"
			}
			if !st.Started {
				sink.WritePart(map[string]any{"type": "tool-input-start", "toolCallId": st.ID, "toolName": st.Name})
				st.Started = true
			}
			if tc.Function.Arguments != "" {
				st.InputText.WriteString(tc.Function.Arguments)
				sink.WritePart(map[string]any{"type": "tool-input-delta", "toolCallId": st.ID, "inputTextDelta": tc.Function.Arguments})
			}
		}
	}
	if msg.Role == schema.Tool {
		callID := msg.ToolCallID
		if callID == "" {
			callID = fmt.Sprintf("tool_call_%d", len(state.toolCalls))
		}
		st := state.toolCalls[callID]
		if st == nil {
			st = &toolCallState{ID: callID, Name: msg.ToolName}
			state.toolCalls[callID] = st
		}
		if msg.ToolName != "" {
			st.Name = msg.ToolName
		}
		if st.Name == "" {
			st.Name = "tool"
		}
		if !st.Available {
			writeToolAvailableSink(sink, st)
		}
		sink.WritePart(map[string]any{"type": "tool-output-available", "toolCallId": st.ID, "output": parseMaybeJSON(msg.Content)})
	}
	if msg.ResponseMeta != nil {
		if msg.ResponseMeta.FinishReason == "tool_calls" {
			finishOpenBlocksSink(sink, state)
			for _, st := range state.toolCalls {
				if st.Started && !st.Available {
					writeToolAvailableSink(sink, st)
				}
			}
			sink.WritePart(map[string]any{"type": "finish-step"})
			sink.WritePart(map[string]any{"type": "start-step"})
			state.stepStarted = true
			state.textID = fmt.Sprintf("text_%d", time.Now().UnixNano())
			state.reasoningID = fmt.Sprintf("reasoning_%d", time.Now().UnixNano())
			state.textStarted = false
			state.reasoningStarted = false
		}
		if msg.ResponseMeta.Usage != nil || msg.ResponseMeta.FinishReason != "" {
			sink.WritePart(map[string]any{
				"type": "data-usage",
				"data": map[string]any{
					"finishReason": msg.ResponseMeta.FinishReason,
					"usage":        convertEinoUsageToAISDKUsage(msg.ResponseMeta.Usage),
				},
			})
		}
	}
}

func writeToolAvailableSink(sink AISDKSink, st *toolCallState) {
	inputText := st.InputText.String()
	sink.WritePart(map[string]any{
		"type":       "tool-input-available",
		"toolCallId": st.ID,
		"toolName":   st.Name,
		"input":      parseMaybeJSON(inputText),
	})
	st.Available = true
}

func (m *AgentManager) writeRunError(ctx context.Context, sessionID, runID string, err error) {
	errObj := map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "server_error",
		},
	}

	b, _ := json.Marshal(errObj)
	_, _ = m.runStore.Append(ctx, sessionID, runID, string(b))
	_, _ = m.runStore.Append(ctx, sessionID, runID, "[DONE]")
	_ = m.runStore.SetRunStatus(ctx, sessionID, runID, RunStatusError)
}

func (m *AgentManager) finishRunCanceled(sessionID, runID string) {
	ctx := context.Background()
	_, _ = m.runStore.Append(ctx, sessionID, runID, "[DONE]")
	_ = m.runStore.SetRunStatus(ctx, sessionID, runID, RunStatusCanceled)
}
