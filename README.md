# einoai

`einoai` 是一个基于 CloudWeGo Eino ADK 封装的 Go 大模型运行包。它把模型执行、Redis 事件持久化、断点恢复、中断、reasoning / `<think>` 拆分抽成核心能力，并提供 AI SDK / assistant-ui 与 OpenAI-compatible 两套协议适配。

核心包不依赖 Gin，也不强制注册路由。业务方可以在自己的 handler 中先做鉴权、限流、计费、日志、用户态 agent 选择、参数校验、业务埋点或多租户隔离，再组合调用本包提供的函数。

## 特性

- 基于 `model.ToolCallingChatModel` 与 Eino ADK `adk.Agent` 执行 run。
- Redis 持久化 run metadata、status、events、current run、error、usage。
- SSE 事件支持 `AfterEventID` 断点恢复。
- 支持通过 `CancelRun` 中断后台 run。
- 支持 AI SDK / assistant-ui UI Message Stream 输出。
- 支持 OpenAI-compatible chat completions 流式与非流式输出。
- 支持 Eino `ReasoningContent` 与模型内容中的 `<think>...</think>` 拆分。
- 协议包提供可拆分函数，方便接入业务自己的 Gin handler。

## 安装

```bash
go get github.com/xu756/einoai
```

当前模块路径以 `go.mod` 为准：

```go
module github.com/xu756/einoai
```

## 包结构

```text
.
├── aisdk/        # AI SDK / assistant-ui 协议适配
├── openai/       # OpenAI-compatible 协议适配
├── cmd/server/   # 示例 Gin 服务，仅用于本地测试和参考
├── docs/         # HTTP API 文档
├── service.go    # 核心 Service 实现
├── run.go        # Run 类型、状态、核心接口
├── event.go      # 内部统一事件类型
├── runner.go     # Eino ADK 执行与事件转换
├── reasoning.go  # <think> 拆分
└── store_redis.go
```

包职责：

| 包 | 职责 |
| --- | --- |
| `github.com/xu756/einoai` | 核心 run 编排、Redis 持久化、订阅、中断、reasoning 拆分，不依赖 Gin |
| `github.com/xu756/einoai/aisdk` | AI SDK / assistant-ui 请求绑定、消息转换、UI Message Stream 输出、Gin 辅助函数 |
| `github.com/xu756/einoai/openai` | OpenAI-compatible chat completions 请求绑定、消息转换、stream chunk 输出、Gin 辅助函数 |
| `cmd/server` | 示例服务，不是核心包 |

## 快速开始

### 创建 Service

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/xu756/einoai"
)

redisClient := redis.NewClient(&redis.Options{
    Addr: "127.0.0.1:6379",
})

svc := einoai.NewService(model, redisClient)
```

`NewService` 参数：

```go
func NewService(chatModel model.ToolCallingChatModel, db *redis.Client) Service
```

核心接口：

```go
type Service interface {
    CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
    GetRun(ctx context.Context, sessionID string) (*RunInfo, error)
    CancelRun(ctx context.Context, sessionID string) error
    SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}
```

### 在业务 Gin Handler 中使用 AI SDK 协议

```go
import (
    "github.com/xu756/einoai"
    "github.com/xu756/einoai/aisdk"
)

func (h *Handler) CreateAIRun(c *gin.Context) {
    sessionID := c.Param("sessionId")

    req, err := aisdk.BindCreateRunRequest(c)
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }

    messages, err := aisdk.ToSchemaMessages(req)
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }

    agent, err := h.ResolveAgent(c.Request.Context(), sessionID, messages)
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }

    run, err := h.AIService.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
        SessionID: sessionID,
        Messages:  messages,
        Agent:     agent,
        Metadata: map[string]any{
            "protocol": "aisdk",
            "model":    req.Model,
            "params":   req.Params,
        },
    })
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }

    aisdk.WriteCreateRunResponse(c, run)
}
```

订阅事件：

```go
func (h *Handler) RunAIEvents(c *gin.Context) {
    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID:    c.Param("sessionId"),
        AfterEventID: aisdk.GetLastEventID(c),
    })
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }
    defer stream.Close()

    aisdk.WriteEventStream(c, stream)
}
```

### 在业务 Gin Handler 中使用 OpenAI 协议

```go
import (
    "github.com/xu756/einoai"
    "github.com/xu756/einoai/openai"
)

func (h *Handler) ChatCompletions(c *gin.Context) {
    req, err := openai.BindChatCompletionsRequest(c)
    if err != nil {
        openai.WriteError(c, err)
        return
    }

    messages, err := openai.ToSchemaMessages(req)
    if err != nil {
        openai.WriteError(c, err)
        return
    }

    agent, err := h.ResolveOpenAIAgent(c.Request.Context(), req, messages)
    if err != nil {
        openai.WriteError(c, err)
        return
    }

    run, err := h.AIService.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
        SessionID: openai.ResolveSessionID(c, req),
        Messages:  messages,
        Agent:     agent,
        Metadata: map[string]any{
            "protocol": "openai",
            "model":    req.Model,
        },
    })
    if err != nil {
        openai.WriteError(c, err)
        return
    }

    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID: run.SessionID,
    })
    if err != nil {
        openai.WriteError(c, err)
        return
    }
    defer stream.Close()

    if req.Stream {
        openai.WriteChatCompletionStream(c, req, stream)
        return
    }

    body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
    if err != nil {
        openai.WriteError(c, err)
        return
    }
    c.JSON(http.StatusOK, body)
}
```

## 核心设计

### 核心包 `einoai`

核心包只处理协议无关的 run 生命周期：

- 创建 run。
- 查询 session 当前 run。
- 取消 session 当前 run。
- 订阅 run 事件。
- Redis Stream 持久化事件并支持断点恢复。
- 执行 Eino ADK agent。
- 将模型输出转换为内部统一事件。

`CreateRunRequest` 使用完整 `[]*schema.Message`，不会兼容旧的单 `message string` 字段，不会只取最后一条 user message，也不会生成默认兜底消息。

### `aisdk` 协议包

`aisdk` 负责：

- 绑定 AI SDK / assistant-ui 请求体。
- 将 AI SDK messages 转成 `[]*schema.Message`。
- 将核心 `RunEvent` 写成 AI SDK UI Message Stream SSE。
- 提供 `WriteError`、`WriteCreateRunResponse`、`WriteEventStream` 等可组合函数。
- 提供 `HandleCreateRun`、`HandleCompletions` 等便捷函数，但它们不是唯一入口。

### `openai` 协议包

`openai` 负责：

- 绑定 OpenAI-compatible chat completions 请求。
- 将 OpenAI messages 转成 `[]*schema.Message`。
- 输出 OpenAI-compatible streaming chunk。
- 聚合非流式 chat completions 响应。
- 提供 `ResolveSessionID`、`WriteError`、`WriteChatCompletionStream` 等可组合函数。

### 为什么不强制 `RegisterRoutes`

不推荐把路由注册和 handler 完全封死，例如：

```go
handler.RegisterAISDKRoutes(router.Group("/usechat"))
handler.RegisterOpenAIRoutes(router.Group("/v1"))
```

实际业务通常需要在 handler 中插入：

- 鉴权
- 限流
- 计费
- 日志
- 用户态 agent 选择
- 参数校验
- 业务埋点
- 多租户隔离

因此本包更推荐业务方自行注册路由，并在 handler 中组合协议包函数与核心 Service。

## 运行示例服务

示例服务位于 `cmd/server`：

```bash
go run ./cmd/server
```

如果本地安装了 Air，也可以使用：

```bash
air
```

`.air.toml` 只是本地热重载开发配置，不是核心包运行要求。

## 环境变量

示例服务会读取以下环境变量：

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `HTTP_ADDR` | Gin 服务监听地址 | `:8080` |
| `OPENAI_API_KEY` | OpenAI-compatible 模型 API Key | 无 |
| `OPENAI_BASE_URL` | OpenAI-compatible 模型 Base URL | 无 |
| `MODEL_NAME` | 模型名称，也会用于 AI SDK `message-metadata.modelId` | 无 |
| `REDIS_ADDR` | Redis 地址 | `127.0.0.1:6379` |
| `REDIS_PASSWORD` | Redis 密码 | 空 |
| `REDIS_DB` | Redis DB | `1` |

本地示例服务可以通过 `.env` 配置模型和 Redis，但生产环境建议使用系统环境变量或配置中心。不要把 `.env` 作为必须提交的配置文件。

## Redis 存储说明

Redis 用于保存：

- run meta：`session_id`、`run_id`、`status`、`error`、`created_at`、`updated_at`、`metadata`。
- current run 指针。
- Redis Stream 事件。
- usage 通过最终 `finish` 事件持久化。

当前代码中的 Redis key TTL 为 2 小时。

## Reasoning / `<think>` 处理

模型输出会被转换为内部统一事件：

- 如果 Eino 返回 `ReasoningContent`，转换为 `reasoning_start`、`reasoning_delta`、`reasoning_end`。
- 如果模型把 `<think>...</think>` 混在 `Content`，输出转换层会拆成 reasoning 事件和 text 事件。
- 流式分片中 `<think>` 标签被拆开时，也会通过拆分器正确处理。

## 中断与恢复

- `CancelRun(ctx, sessionID)` 会取消该 session 当前 run。
- 取消后会写入 `finish` 事件，finish reason 为 `cancelled`，状态更新为 `cancelled`。
- 订阅时可以通过 `AfterEventID` 恢复未消费事件。
- HTTP SSE 层支持 `?after=`、`?lastEventId=` 和 `Last-Event-ID`。

## 测试

```bash
go test ./...
```

当前测试覆盖了 Redis 存储读取与 `<think>` reasoning 拆分等核心行为。
