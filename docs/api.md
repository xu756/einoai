# HTTP API

`cmd/server` 是示例服务，核心包不强制使用 Gin。以下接口展示当前 run/event 边界：einoai 不保存 session history，业务应用必须从自己的 history repository 读取和保存消息。

## 存储与生命周期

- Redis 保存 run metadata、current run 指针和 per-run event stream。
- `CreateRun` 接收本次请求的完整 `messages` 快照，并复制消息后再写内部 ID，不修改调用方对象。
- 同一 session 只能有一个非终态 run；创建占位由 Redis 原子保证。
- 默认 run timeout 为 10 分钟，可通过 `WithRunTimeout` 配置。
- 正常完成后，`CreateRunRequest.OnCompleted` 收到 `RunResult.Messages`。
- 取消、异常和删除不会触发完成 hook。
- hook 错误不会改变已完成 run 的状态；通过 `WithCompletionErrorHandler` 观察。
- `DeleteSession` 只删除 einoai 的 run artifacts，并在必要时中断活动 run，不删除业务 history。

## AI SDK

基础路径：`/api/usechat`

### `POST /completions`

直接创建并订阅一个 UI Message Stream。请求使用 AI SDK `messages` 数组，响应为 UI Message Stream v1。

### `POST /sessions/:sessionId`

创建异步 run，返回：

```json
{
  "sessionId": "session_1",
  "run_id": "run_1",
  "status": "queued"
}
```

### `GET /sessions/:sessionId`

返回当前非终态 run，不返回 history：

```json
{
  "run": {
    "session_id": "session_1",
    "run_id": "run_1",
    "status": "running"
  }
}
```

run 进入终态后该接口返回空 run；应用自己的 history endpoint 应直接查询业务 repository。

### `GET /sessions/:sessionId/runs/:run_id`

按 run id 查询持久化 metadata，可读取 `completed`、`cancelled`、`failed` 等终态。该方法属于 `RunLookupService` 扩展接口；原 `Service` 方法集保持兼容。run 不存在时返回可由 `errors.Is(err, einoai.ErrRunNotFound)` 识别的错误。

### `POST /sessions/:sessionId/runs/:run_id`

订阅指定 run 的 UI Message Stream。事件包括 text、reasoning、tool call、tool result、finish 和 usage。核心 `SubscribeEvents` 支持 `AfterEventID` 作为内部事件游标。

### `POST /sessions/:sessionId/runs/:run_id/cancel`

取消指定 run。响应为 `{ "ok": true }`；取消不会调用 `OnCompleted`。

### `DELETE /sessions/:sessionId`

删除该 session 的 run metadata、current pointer 和 events；活动 run 会先中断。业务 history 不受影响。

## OpenAI-compatible

基础路径：`/api/v1`

### `POST /chat/completions`

请求使用标准 OpenAI Chat Completions `messages`。`stream=true` 返回 OpenAI-compatible SSE；`stream=false` 返回最终 completion。`X-Session-ID` header 或 `sessionId` query 可指定 session。

### `POST /sessions/:sessionId`

创建异步 run，返回同样的 run metadata；history 不由该接口返回。

### `GET /sessions/:sessionId`

返回当前非终态 run；run 完成后返回空 run。

### `GET /sessions/:sessionId/runs/:run_id`

按 run id 查询持久化 metadata，因此可以读取终态状态。

### `POST /sessions/:sessionId/runs/:run_id`

订阅指定 run 的 OpenAI-compatible stream。`include_usage=true` query 会请求最终 usage chunk。核心 `SubscribeEvents` 支持 `AfterEventID`；示例 HTTP handler 尚未直接映射 `Last-Event-ID`。

### `POST /sessions/:sessionId/runs/:run_id/cancel`

取消指定 run。

### `DELETE /sessions/:sessionId`

删除 einoai run artifacts，不删除业务 history。

## 应用集成

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

生产应用应在 repository 层增加幂等键（通常使用 `run_id`）和自己的超时、重试或 outbox 策略。einoai 的完成 hook 是进程内回调，进程崩溃后不会自动重试。

## 请求错误

- 缺少 `messages`、`sessionId`、`run_id` 或 agent 参数会返回 4xx/5xx 错误。
- malformed protocol parts 会在请求转换阶段拒绝。
- 流开始后发生错误时，错误通过对应协议的 stream error 形式发送。
