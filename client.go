package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
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
	rdb      *redis.Client
	runStore *RunStore
	model    model.ToolCallingChatModel
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
		model:    cm,
		rdb:      rdb,
		runStore: NewRunStore(rdb),
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

	go m.executeRun(sessionID, runID, message, kind)

	return runID, nil
}

func (m *AgentManager) executeRun(
	sessionID string,
	runID string,
	message string,
	kind AgentKind,
) {
	// 重点：这里不能用 HTTP request context。
	// 这个 ctx 只代表后台 run 自己的生命周期。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	_ = m.runStore.SetRunStatus(ctx, sessionID, runID, "running")

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
		m.writeRunError(ctx, sessionID, runID, err)
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
		_ = m.runStore.SetRunStatus(context.Background(), sessionID, runID, "error")
		return
	}

	_ = m.runStore.SetRunStatus(context.Background(), sessionID, runID, "done")
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
	_ = m.runStore.SetRunStatus(ctx, sessionID, runID, "error")
}
