# einoai

`einoai` 是基于 CloudWeGo Eino ADK 的 Go run 编排包。它负责 agent 执行、Redis-backed run 状态、事件流、取消和协议适配；session history 由业务应用自己保存。

核心包不依赖 Gin。业务方可以在 handler 中完成鉴权、限流、计费、agent 选择和自己的消息存储，然后调用本包。

## 设计边界

- Redis 只保存 run metadata、current run 指针和 per-run event stream。
- einoai 不保存、不查询 session history，也不提供 `GetMessages`。
- 正常完成时，`OnCompleted` 收到完整的输入、输出和合并消息。
- 取消或异常不会触发 `OnCompleted`。
- hook 错误只通过 `CompletionErrorHandler` 观察，不会把已完成的 run 改成失败。
- 流式请求的最终消息仍由 AI SDK / OpenAI stream 返回；异步请求的完整消息由 hook 交给业务存储。

## 安装

```bash
go get github.com/xu756/einoai
```

## 创建 Service

```go
redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
svc := einoai.NewService(
    redisClient,
    einoai.WithRedisTTL(7*24*time.Hour),
    einoai.WithCompletionErrorHandler(func(ctx context.Context, sessionID, runID string, err error) {
        logger.Printf("history hook failed session=%s run=%s: %v", sessionID, runID, err)
    }),
)
```

Redis TTL 默认 7 天；传入 `0` 或负数表示不过期。

## 核心 API

```go
type Service interface {
    CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
    GetRun(ctx context.Context, sessionID string) (*RunInfo, error)
    DeleteSession(ctx context.Context, sessionID string) error // 只删除 einoai run artifacts
    CancelRun(ctx context.Context, sessionID, runID string) error
    SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}

type CreateRunRequest struct {
    SessionID   string
    Messages    []*schema.Message // 本次请求携带的完整输入快照
    Agent       adk.Agent
    Metadata    map[string]any
    OnCompleted einoai.OnRunCompleted
}

type RunResult struct {
    Run      *RunInfo
    Input    []*schema.Message
    Output   []*schema.Message
    Messages []*schema.Message // Input + Output
    Usage    *schema.TokenUsage
}
```

## 业务保存历史

业务方在 hook 中写入自己的数据库、对象存储或消息队列。hook 只在正常完成时调用，建议由业务方保证幂等和超时：

```go
run, err := svc.CreateRun(ctx, einoai.CreateRunRequest{
    SessionID: sessionID,
    Messages:  messages,
    Agent:     agent,
    OnCompleted: func(ctx context.Context, result *einoai.RunResult) error {
        return historyRepo.Replace(ctx, result.Run.SessionID, result.Messages)
    },
})
```

`Messages` 是输入快照后接完整 assistant/tool 输出。取消、agent 错误、流错误和 session 删除都不会保存未完成 assistant 输出，也不会调用 hook。hook 在 run 状态已经为 `completed` 后执行；hook 返回错误不改变 run 状态。

## 协议适配

| 包 | 职责 |
| --- | --- |
| `github.com/xu756/einoai` | run 编排、Redis 状态、事件、取消和完成 hook |
| `github.com/xu756/einoai/aisdk` | AI SDK UIMessage 请求转换和 UI Message Stream 输出 |
| `github.com/xu756/einoai/openai` | OpenAI Chat Completions 请求转换和 SSE / 非流式输出 |
| `cmd/server` | 示例 Gin 服务，不是核心包 |

请求转换仍然使用：

```go
schemaMessages, err := aisdk.ToSchemaMessages(req)
schemaMessages, err := openai.ToSchemaMessages(req)
```

历史读取应直接访问业务自己的 `historyRepo`，不要从 einoai 查询。run 状态接口只返回 run metadata：

```json
{"run":{"session_id":"session_1","run_id":"run_1","status":"completed"}}
```

实时事件仍可通过 `SubscribeEvents` 消费；支持 text、reasoning、tool call、tool result、finish 和 usage。

## Redis 存储

核心包只创建以下类型的 key：

- run metadata：状态、错误、时间、metadata；
- current run 指针；
- per-run Redis Stream 事件。

session history、active message snapshot 和业务消息 ID 不由 einoai 持久化。`DeleteSession` 会中断活动 run 并删除上述 run artifacts，不会删除业务数据库中的 history。

## 取消、错误与重连

- `CancelRun(ctx, sessionID, runID)` 精确取消指定 run。
- agent 错误会写入 error/finish 事件并将 run 标记为 failed。
- 取消和失败都不会触发完成 hook。
- `SubscribeEvents` 按 `sessionID + runID` 订阅，不使用 `Last-Event-ID` 断点恢复。

## 示例服务

`cmd/server` 展示了 AI SDK 与 OpenAI 两套 handler。示例中的 `app.onRunCompleted` 是业务 history repository 的替换点；生产应用应将它替换成自己的持久化实现。

## 测试

```bash
env GOCACHE=/tmp/edgeinfer-go-build go test ./...
```
