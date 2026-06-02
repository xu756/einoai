package main

import (
	"context"
	"os"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/redis/go-redis/v9"
)

type AgentManager struct {
	rdb   *redis.Client
	model model.ToolCallingChatModel
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
		model: cm,
		rdb:   rdb,
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
