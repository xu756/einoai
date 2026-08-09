package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	ccb "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/coze-dev/cozeloop-go"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/xu756/einoai"
)

type app struct {
	model model.ToolCallingChatModel
	svc   einoai.RunLookupService
}

// onRunCompleted is the application-owned history persistence seam.
// Replace this sample log with the application's repository write.
func (a *app) onRunCompleted(_ context.Context, result *einoai.RunResult) error {
	log.Printf("run completed session=%s run=%s messages=%d", result.Run.SessionID, result.Run.RunID, len(result.Messages))
	return nil
}

func main() {
	_ = godotenv.Load()

	ctx := context.Background()
	cozeClient, err := cozeloop.NewClient()
	if err != nil {
		panic(err)
	}
	defer cozeClient.Close(ctx)

	handler := ccb.NewLoopHandler(cozeClient)
	callbacks.AppendGlobalHandlers(handler)

	chatModel, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   os.Getenv("MODEL_NAME"),
	})
	if err != nil {
		log.Fatalf("create chat model: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     envOr("REDIS_ADDR", "127.0.0.1:6379"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       envInt("REDIS_DB", 1),
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	defer func() {
		_ = rdb.Close()
	}()

	a := &app{
		model: chatModel,
		svc: einoai.NewService(
			rdb,
			einoai.WithRedisTTL(envDuration("REDIS_TTL", einoai.DefaultRedisTTL)),
			einoai.WithRunTimeout(envDuration("RUN_TIMEOUT", einoai.DefaultRunTimeout)),
		),
	}

	engine := gin.Default()
	engine.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	api := engine.Group("/api")
	a.registerAISDK(api.Group("/usechat"))
	a.registerOpenAI(api.Group("/v1"))

	// 兼容你之前本地调试常用的路径。
	api.POST("/chat/completions", a.openAICompletions)

	addr := envOr("HTTP_ADDR", ":8080")
	log.Printf("listening on %s", addr)
	if err := engine.Run(addr); err != nil {
		log.Fatalf("run server: %v", err)
	}
}

func (a *app) resolveAgent(ctx context.Context) (adk.Agent, error) {
	weatherTool, err := NewWeatherTool(ctx)
	if err != nil {
		return nil, err
	}
	calculatorTool, err := NewCalculatorTool(ctx)
	if err != nil {
		return nil, err
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Model: a.model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool, calculatorTool},
			},
		},
	})
}

func envOr(key string, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}
