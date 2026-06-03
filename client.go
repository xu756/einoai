package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/redis/go-redis/v9"
)

type AgentManager struct {
	runStore   *RunStore
	model      model.ToolCallingChatModel
	runMu      sync.Mutex
	runCancels map[string]context.CancelFunc
	runIters   map[string]*adk.AsyncIterator[*adk.AgentEvent]
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
		runIters:   make(map[string]*adk.AsyncIterator[*adk.AgentEvent]),
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

func (m *AgentManager) StartRun(
	ctx context.Context,
	sessionID string,
	message string,
	kind AgentKind,
) (string, error) {
	runID := newRunID()

	if err := m.runStore.InitRun(ctx, sessionID, runID, message); err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.registerRunCancel(runID, cancel)

	go m.executeRun(runCtx, cancel, sessionID, runID, message, kind)

	return runID, nil
}

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

func (m *AgentManager) StartAIRun(
	ctx context.Context,
	sessionID string,
	message string,
	kind AgentKind,
) (string, error) {
	runID := newRunID()

	if err := m.runStore.InitRun(ctx, sessionID, runID, message); err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.registerRunCancel(runID, cancel)

	go m.executeAIRun(runCtx, cancel, sessionID, runID, message, kind)

	return runID, nil
}

func (m *AgentManager) GetAIRunIterator(ctx context.Context, runID string) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	m.runMu.Lock()
	iter := m.runIters[runID]
	m.runMu.Unlock()

	if iter != nil {
		return iter, nil
	}

	run, err := m.runStore.GetRun(ctx, "", runID)
	if err != nil {
		return nil, err
	}
	if run == nil || isTerminalRunStatus(run.Status) {
		return nil, nil
	}
	return nil, nil
}

func (m *AgentManager) registerRunIter(runID string, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.runIters == nil {
		m.runIters = make(map[string]*adk.AsyncIterator[*adk.AgentEvent])
	}
	m.runIters[runID] = iter
}

func (m *AgentManager) unregisterRunIter(runID string) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	delete(m.runIters, runID)
}

func (m *AgentManager) executeAIRun(
	ctx context.Context,
	cancel context.CancelFunc,
	sessionID string,
	runID string,
	message string,
	kind AgentKind,
) {
	defer cancel()
	defer m.unregisterRunCancel(runID)
	defer m.unregisterRunIter(runID)

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
	iter := runner.Query(ctx, message)

	m.registerRunIter(runID, iter)

	// 流式输出复用 Redis sink 保证事件不丢，同时 iterator 供 SSE 读取
	sink := &RedisOpenAISink{
		store:     m.runStore,
		sessionID: sessionID,
		runID:     runID,
	}

	modelName := os.Getenv("MODEL_NAME")
	if modelName == "" {
		modelName = "GPT-4"
	}

	if err := streamOpenAICompatibleToSink(ctx, sink, iter, modelName); err != nil {
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

func (m *AgentManager) executeRun(
	ctx context.Context,
	cancel context.CancelFunc,
	sessionID string,
	runID string,
	message string,
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
	iter := runner.Query(ctx, message)

	sink := &RedisOpenAISink{
		store:     m.runStore,
		sessionID: sessionID,
		runID:     runID,
	}

	modelName := os.Getenv("MODEL_NAME")
	if modelName == "" {
		modelName = "GPT-4"
	}

	if err := streamOpenAICompatibleToSink(ctx, sink, iter, modelName); err != nil {
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
