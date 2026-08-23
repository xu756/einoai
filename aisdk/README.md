# einoai/aisdk

`github.com/xu756/einoai/aisdk` 将 AI SDK UIMessage 请求转换为 Eino `schema.Message`，并把核心 `RunEvent` 输出为 UI Message Stream v1。它不保存或读取 session history。

## 请求转换

```go
req, err := aisdk.DecodeCreateRunRequest(body)
messages, err := aisdk.ToSchemaMessages(req)
```

转换会保留 UI message 的角色、parts、工具调用、reasoning、metadata 和多模态输入。请求中的 `messages` 必须非空。

## 运行与流

```go
run, err := svc.CreateRun(ctx, einoai.CreateRunRequest{
    SessionID: sessionID,
    Messages:  messages,
    Agent:     agent,
    OnTerminated: func(ctx context.Context, result *einoai.RunResult) error {
        return historyRepo.Replace(ctx, result.Run.SessionID, result.Messages)
    },
})
stream, err := svc.SubscribeEvents(ctx, einoai.SubscribeRequest{
    SessionID:    run.SessionID,
    RunID:        run.RunID,
    AfterEventID: "", // 重连时可传内部 event id
})
defer stream.Close()
aisdk.SetEventStreamHeaders(w.Header())
result, err := aisdk.WriteEventStreamTo(ctx, w, flush, stream)
// result.Output 是完整/部分 Eino 输出；result.Usage 是整个 agent run 的累计 token usage。
// 应用可将 result 落库；Redis run/event 数据按 1 小时 TTL 自动过期。
```

`OnCompleted` 只在正常完成时调用；`OnTerminated` 覆盖完成、取消和异常。hook 错误不会改变 run 状态，应用应自行处理幂等、超时和重试。

completions 和 run 订阅在建立 `EventStream` 前失败时，也应调用 `WriteEventStreamErrorTo`，以 UI Message Stream error part 后接 `data: [DONE]` 结束。创建、查询、删除和取消 run 仍使用普通 HTTP JSON 错误。

## Response helpers

- `NewCreateRunResponse(run)` 返回创建结果。
- `NewRunResponse(run)` 返回只包含 run metadata 的状态响应，不包含 history。
- `NewCancelResponse()` 和 `NewDeleteSessionResponse()` 返回 `{ "ok": true }`。

`GET /sessions/:sessionId` 的 history 应由应用自己的 endpoint 查询。实时 stream 使用 UI Message Stream 的 text、reasoning、provider-executed tool、finish/error/abort 事件；Eino 完整输出和累计 usage 同时由 writer 以 `StreamResult` 返回。


## Run 查询与生命周期

`GetRun(sessionID)` 只返回当前非终态 run；需要读取已经完成、取消或失败的 run metadata 时，使用 `NewService` 返回的 `RunLookupService.GetRunByID(sessionID, runID)`。默认 run timeout 为 10 分钟，可通过 `WithRunTimeout` 修改。
