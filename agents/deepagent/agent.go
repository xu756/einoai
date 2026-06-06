package deepagent

import (
	"context"
	"os"

	localbk "github.com/cloudwego/eino-ext/adk/backend/local"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/skill"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Agent struct {
}

// 工作workspace
const Workspace = "./deepagent"

func GetDeepAgent(ctx context.Context, model model.ToolCallingChatModel) (adk.ResumableAgent, error) {

	backend, _ := localbk.NewBackend(ctx, &localbk.Config{})

	skiiDir := os.Getenv("SKILLS_DIR")
	if skiiDir == "" {
		skiiDir = "./skills"
	}
	skillBackend, _ := skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
		Backend: backend,
		BaseDir: skiiDir,
	})
	skillMiddleware, _ := skill.NewTyped(ctx, &skill.TypedConfig[*schema.Message]{
		Backend: skillBackend,
	})
	return deep.New(ctx, &deep.Config{
		ChatModel: model,
		SubAgents: []adk.Agent{},
		Handlers: []adk.TypedChatModelAgentMiddleware[*schema.Message]{
			skillMiddleware,
			// ... 其他中间件，比如 approval/safeTool/retry 等
		},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{},
			},
		},
		Instruction: "所有文件操作在./workspace 目录操作 没有workspace目录就创建",

		Backend:        backend, // 提供文件系统操作能力
		StreamingShell: backend, // 提供命令执行能力
		MaxIteration:   50,
	})
}
