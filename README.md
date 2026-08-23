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
    einoai.WithRunTimeout(10*time.Minute),
    einoai.WithCompletionErrorHandler(func(ctx context.Context, sessionID, runID string, err error) {
        logger.Printf("history hook failed session=%s run=%s: %v", sessionID, runID, err)
    }),
)
```

Redis TTL 默认 7 天；传入 `0` 或负数表示不过期。run 默认最长 10 分钟，`WithRunTimeout(0)` 可关闭 service-level 超时。run 会与 HTTP 请求取消解耦，但保留 request context 中的 values，适合 tracing / tenant 信息继续传递。

## 核心 API

```go
type Service interface {
    CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
    GetRun(ctx context.Context, sessionID string) (*RunInfo, error) // 当前非终态 run
    DeleteSession(ctx context.Context, sessionID string) error // 只删除 einoai run artifacts
    CancelRun(ctx context.Context, sessionID, runID string) error
    SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}

type RunLookupService interface {
    Service
    GetRunByID(ctx context.Context, sessionID, runID string) (*RunInfo, error) // 包含终态
}

type SubscribeRequest struct {
    SessionID    string
    RunID        string
    AfterEventID string // 可选：从指定 Redis Stream event id 之后继续
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

`Messages` 是输入快照后接完整 assistant/tool 输出。创建 run 时会复制输入消息、主要多模态结构与 metadata，并在副本上注入内部 message ID，不会修改调用方传入的 `schema.Message`。取消、agent 错误、流错误和 session 删除都不会保存未完成 assistant 输出，也不会调用 hook。hook 在 run 状态已经为 `completed` 后执行；hook 返回错误不改变 run 状态。

## 协议适配

| 包 | 职责 |
| --- | --- |
| `github.com/xu756/einoai` | run 编排、Redis 状态、事件、取消和完成 hook |
| `github.com/xu756/einoai/aisdk` | AI SDK UIMessage 请求转换和 UI Message Stream 输出 |
| `github.com/xu756/einoai/openai` | OpenAI Chat Completions 请求转换和统一 SSE 流输出 |
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

## 并发与状态一致性

- 同一 session 的 run 创建通过 Redis 原子占位，多个服务实例同时创建时只允许一个成功，其余返回可用 `errors.Is(err, einoai.ErrRunActive)` 判断的错误。
- `completed`、`cancelled`、`failed` 都是不可逆终态。迟到的模型 chunk 不再写入终态 run。
- `DeleteSession` 对 session ID 中 Redis glob 字符（如 `*`、`?`、`[]`）做转义，并对扫描结果做精确 key 结构过滤；像 `tenant` 与 `tenant:child` 这样的 session 不会互相误删。
- `GetRun` 仍表示“当前活动 run”；`NewService` 返回的 `RunLookupService` 还支持 `GetRunByID` 查询已经完成/取消/失败的持久化 run metadata。原 `Service` 方法集保持不变，已有 mock 不需要为了此能力补方法。

## Redis 存储

核心包只创建以下类型的 key：

- run metadata：状态、错误、时间、metadata；
- current run 指针；
- per-run Redis Stream 事件。

session history、active message snapshot 和业务消息 ID 不由 einoai 持久化。`DeleteSession` 会中断活动 run 并删除上述 run artifacts，不会删除业务数据库中的 history。

## 取消、错误与重连

- `CancelRun(ctx, sessionID, runID)` 精确取消指定 run；终态不可逆，不会被迟到的 worker 覆盖。
- 活动 worker 会周期性检查 Redis run 状态，因此其它实例执行的取消/删除也能传播到本地 run context。
- agent 错误会写入 error/finish 事件并将 run 标记为 failed。
- 取消和失败都不会触发完成 hook。
- `SubscribeEvents` 按 `sessionID + runID` 订阅；可通过 `AfterEventID` 从某个内部 event id 之后继续读取。协议适配层暂不自动把 HTTP `Last-Event-ID` 映射为该游标，因为一个内部事件可能展开为多个 SSE frame。

## 示例服务

`cmd/server` 展示了 AI SDK 与 OpenAI 两套 handler。示例中的 `app.onRunCompleted` 是业务 history repository 的替换点；生产应用应将它替换成自己的持久化实现。

## 测试

```bash
env GOCACHE=/tmp/edgeinfer-go-build go test ./...
```
