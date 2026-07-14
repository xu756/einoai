# einoai

`einoai` 是一个基于 CloudWeGo Eino ADK 封装的 Go 大模型运行包。核心包只负责 run 生命周期、Redis 持久化、事件订阅、中断、reasoning / `<think>` 拆分；协议层分别提供 AI SDK / assistant-ui 和 OpenAI-compatible 的消息转换与流式输出。

核心包不依赖 Gin，也不强制注册路由。业务方可以在自己的 handler 中先做鉴权、限流、计费、日志、agent 选择、参数校验、业务埋点或多租户隔离，再组合调用本包。

## 特性

- 基于 Eino ADK `adk.Agent` 执行 run，agent 自己携带模型、工具和编排逻辑。
- Redis 保存 run metadata、status、events、current run、active messages、committed messages、error、usage。
- session 历史以 `[]*schema.Message` 存储，协议格式只在 HTTP 边界转换。
- `GetMessages` 返回当前 session 的 schema 历史；有运行中 run 时返回 active snapshot，不包含正在流式生成的 assistant 内容。
- `DeleteSession` 删除 session history、active snapshot、run meta、events 和 current run；如果有运行中 run 会先中断。
- `SubscribeEvents` 按 `sessionID + runID` 订阅当前 run，不使用 `Last-Event-ID` 断点恢复。
- `CancelRun` 使用 `sessionID + runID` 精确中断 run。
- 支持 AI SDK / assistant-ui UI Message Stream。
- 支持 OpenAI-compatible chat completions 流式与非流式输出。
- 支持 Eino `ReasoningContent` 与模型内容中的 `<think>...</think>` 拆分。

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
├── cmd/server/   # 示例 Gin 服务
├── docs/         # HTTP API 文档
├── service.go    # 核心 Service 实现
├── run.go        # Run 类型、状态、核心接口
├── event.go      # 内部统一事件类型
├── runner.go     # Eino ADK 执行与事件转换
├── reasoning.go  # <think> 拆分
└── store_redis.go
```

| 包 | 职责 |
| --- | --- |
| `github.com/xu756/einoai` | 核心 run 编排、Redis 持久化、订阅、中断、history 管理，不依赖 Gin |
| `github.com/xu756/einoai/aisdk` | AI SDK UIMessage 请求转换、schema history 转 UIMessage、AI SDK SSE 流输出 |
| `github.com/xu756/einoai/openai` | OpenAI chat completions 请求转换、schema history 转 OpenAI messages、OpenAI SSE / 非流式输出 |
| `cmd/server` | 示例服务，不是核心包 |

## 创建 Service

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/xu756/einoai"
)

redisClient := redis.NewClient(&redis.Options{
    Addr: "127.0.0.1:6379",
})

svc := einoai.NewService(redisClient)
```

`NewService` 不接收 chat model。模型、工具和编排逻辑由每次 `CreateRun` 传入的 `adk.Agent` 提供。

默认 Redis key TTL 是 7 天。可以通过 option 调整：

```go
svc := einoai.NewService(
    redisClient,
    einoai.WithRedisTTL(7*24*time.Hour),
)
```

`WithRedisTTL(0)` 或负数表示不设置过期时间。

核心接口：

```go
type Service interface {
    CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
    GetRun(ctx context.Context, sessionID string) (*RunInfo, error)
    GetMessages(ctx context.Context, sessionID string) ([]*schema.Message, error)
    DeleteSession(ctx context.Context, sessionID string) error
    CancelRun(ctx context.Context, sessionID string, runID string) error
    SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}

type CreateRunRequest struct {
    SessionID string
    Messages  []*schema.Message
    Agent     adk.Agent
    Metadata  map[string]any
}

type SubscribeRequest struct {
    SessionID string
    RunID     string
}
```

## History 规则

Redis 内部只保存 `[]*schema.Message`：

- 创建 run 时，业务传入的 `req.Messages` 是本次 session 的完整历史快照。
- 前端如果从中间某条消息重新生成，后端以本次请求体为准，替换 active snapshot。
- run 完成时，committed history = active snapshot + 本次 assistant / tool 输出。
- run 取消或失败时，committed history = active snapshot，不保存未完成的 assistant 流式输出。
- `GetMessages(ctx, sessionID)` 在 run 运行中返回 active snapshot；没有运行中 run 时返回 committed history。

协议包只做边界转换：

```go
uiMessages := aisdk.FromSchemaMessages(schemaMessages)
openaiMessages := openai.FromSchemaMessages(schemaMessages)
```

## AI SDK / assistant-ui 接入

AI SDK 请求体使用官方 UIMessage 结构：

```json
{
  "messages": [
    {
      "id": "user_1",
      "role": "user",
      "metadata": {"custom": {}},
      "parts": [
        {"type": "text", "text": "查询郑州天气"}
      ]
    }
  ],
  "model": "deepseek-v4-flash"
}
```

创建 run：

```go
func (h *Handler) CreateAIRun(c *gin.Context) {
    sessionID := c.Param("sessionId")

    req, err := aisdk.DecodeCreateRunRequest(c.Request.Body)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    messages, err := aisdk.ToSchemaMessages(req)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    agent, err := h.ResolveAgent(c.Request.Context())
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    run, err := h.AIService.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
        SessionID: sessionID,
        Messages:  messages,
        Agent:     agent,
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusAccepted, aisdk.NewCreateRunResponse(run))
}
```

查询 run 和历史消息：

```go
func (h *Handler) GetAIRun(c *gin.Context) {
    sessionID := c.Param("sessionId")

    run, err := h.AIService.GetRun(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }
    messages, err := h.AIService.GetMessages(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    response, err := aisdk.NewRunResponse(run, messages)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, response)
}
```

订阅 run 事件：

```go
func (h *Handler) RunAIEvents(c *gin.Context) {
    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID: c.Param("sessionId"),
        RunID:     c.Param("run_id"),
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }
    defer stream.Close()

    aisdk.SetEventStreamHeaders(c.Writer.Header())
    _ = aisdk.WriteEventStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, stream)
}
```

## OpenAI-compatible 接入

创建 run 或直接 chat completions 时，OpenAI 请求先转换成 schema messages：

```go
func (h *Handler) ChatCompletions(c *gin.Context) {
    req, err := openai.DecodeChatCompletionsRequest(c.Request.Body)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    messages, err := openai.ToSchemaMessages(req)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    agent, err := h.ResolveAgent(c.Request.Context())
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    run, err := h.AIService.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
        SessionID: openai.ResolveSessionID(req, c.GetHeader("X-Session-ID"), c.Query("sessionId")),
        Messages:  messages,
        Agent:     agent,
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID: run.SessionID,
        RunID:     run.RunID,
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    defer stream.Close()

    if req.Stream {
        openai.SetChatCompletionStreamHeaders(c.Writer.Header())
        _ = openai.WriteChatCompletionStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, req, stream)
        return
    }

    body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    c.JSON(http.StatusOK, body)
}
```

示例服务不会把请求中的 `model`、协议名称或 AI SDK `params` 复制到 run metadata。Eino 模型只由已配置的 agent 决定；OpenAI `model` 仍作为响应 chunk 和默认 session ID 的协议字段使用。

查询统一 session 历史：

```go
func (h *Handler) GetOpenAIRun(c *gin.Context) {
    sessionID := c.Param("sessionId")

    run, err := h.AIService.GetRun(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    messages, err := h.AIService.GetMessages(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    response, err := openai.NewRunResponse(run, messages)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    c.JSON(http.StatusOK, response)
}
```

两个 session GET 端点现在返回完全相同的协议无关消息结构。每条 Eino 消息独立保留，`parts` 可以包含 `text`、`reasoning`、`image`、`audio`、`video`、`file`、`tool-call`、`tool-result` 和未知扩展 `data`：

```json
{
  "run": {"session_id":"session_001","run_id":"run_001","status":"completed"},
  "messages": [
    {
      "id": "msg_run_001_input_0",
      "role": "user",
      "parts": [
        {"type":"text","text":"分析图片"},
        {"type":"image","url":"https://example.com/a.png","media_type":"image/png"}
      ]
    },
    {
      "id": "msg_run_001_output_0",
      "role": "assistant",
      "parts": [
        {"type":"reasoning","text":"先识别图片"},
        {"type":"tool-call","tool_call_id":"call_1","tool_name":"vision","input":{"url":"https://example.com/a.png"}}
      ]
    },
    {
      "id": "msg_run_001_output_1",
      "role": "tool",
      "parts": [
        {"type":"tool-result","tool_call_id":"call_1","tool_name":"vision","output":{"objects":["cat"]}}
      ]
    },
    {
      "id": "msg_run_001_output_2",
      "role": "assistant",
      "parts": [{"type":"text","text":"图片中有一只猫"}],
      "finish_reason": "stop",
      "usage": {
        "input_tokens": 100,
        "output_tokens": 30,
        "total_tokens": 130,
        "input_token_details": {"cached_tokens":20,"uncached_tokens":80},
        "output_token_details": {"reasoning_tokens":10,"text_tokens":20}
      }
    }
  ]
}
```

这是 breaking change：session 客户端应改为渲染 `messages[].parts`。实时 AI SDK 和 OpenAI 流仍保持各自协议格式。

## 协议层边界

普通 JSON 响应函数返回结构体，不依赖 Gin：

- `aisdk.NewCreateRunResponse`
- `aisdk.NewRunResponse`（返回 `(RunResponse, error)`）
- `aisdk.NewCancelResponse`
- `aisdk.NewDeleteSessionResponse`
- `openai.NewCreateRunResponse`
- `openai.NewRunResponse`（返回 `(RunResponse, error)`）
- `openai.NewCancelResponse`
- `openai.NewDeleteSessionResponse`

流式输出也不依赖 Gin，直接接通用 writer：

- `aisdk.SetEventStreamHeaders(header)`
- `aisdk.WriteEventStreamTo(ctx, writer, flush, stream)`
- `openai.SetChatCompletionStreamHeaders(header)`
- `openai.WriteChatCompletionStreamTo(ctx, writer, flush, req, stream)`

`writer` 只需要实现 `io.Writer`，`flush` 是可选的 `func()`。Hertz、net/http、fasthttp 或自定义网关都可以用这组函数自己集成 SSE。

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
| `REDIS_TTL` | Redis key 过期时间，Go duration 格式；`0` 表示不过期 | `168h` |

生产环境建议使用系统环境变量或配置中心，不要依赖本地 `.env`。

## Redis 存储说明

Redis 保存：

- run meta：`session_id`、`run_id`、`status`、`error`、`created_at`、`updated_at`、`metadata`。
- current run 指针。
- active messages：运行中的请求快照。
- committed session messages：已完成或已取消后的 session 历史。
- Redis Stream 事件。
- usage，通过最终 `finish` 事件持久化；成功完成的 assistant history message 也会保留 `ResponseMeta.Usage`。

默认 Redis key TTL 为 7 天，也就是 `einoai.DefaultRedisTTL`。示例服务可通过 `REDIS_TTL` 覆盖，代码接入可通过 `einoai.WithRedisTTL(ttl)` 覆盖；传入 `0` 或负数表示不设置过期时间。

## Reasoning / `<think>` 处理

模型输出会被转换为内部统一事件：

- 如果 Eino 返回 `ReasoningContent`，转换为 `reasoning_start`、`reasoning_delta`、`reasoning_end`。
- 如果模型把 `<think>...</think>` 混在 `Content`，输出转换层会拆成 reasoning 事件和 text 事件。
- 流式分片中 `<think>` 标签被拆开时，也会通过拆分器正确处理。

## 中断与订阅

- `DeleteSession(ctx, sessionID)` 会删除该 session 的 history、active snapshot、run meta、events 和 current run；如果有运行中 run 会先中断。
- `CancelRun(ctx, sessionID, runID)` 会取消指定 run。
- 取消后写入 `finish` 事件，finish reason 为 `cancelled`，状态更新为 `cancelled`。
- `SubscribeEvents(ctx, SubscribeRequest{SessionID, RunID})` 订阅指定 run。
- 当前订阅不读取 `Last-Event-ID`，重新连接时直接按 run 订阅。

## 测试

```bash
go test ./...
go vet ./...
golangci-lint run
go test -race ./...
```
