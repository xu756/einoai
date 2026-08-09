# einoai/openai

`github.com/xu756/einoai/openai` 提供 OpenAI-compatible Chat Completions 请求转换、SSE 流和非流式结果。它不保存或读取 session history。

## 请求转换

```go
req, err := openai.DecodeChatCompletionsRequest(body)
messages, err := openai.ToSchemaMessages(req)
```

转换保留标准 `messages`、tool calls/results、reasoning、message name 和 Eino 支持的多模态 parts。请求中的 `messages` 必须非空。

## 运行与流

```go
run, err := svc.CreateRun(ctx, einoai.CreateRunRequest{
    SessionID: openai.ResolveSessionID(req, headerSessionID, querySessionID),
    Messages:  messages,
    Agent:     agent,
    OnCompleted: func(ctx context.Context, result *einoai.RunResult) error {
        return historyRepo.Replace(ctx, result.Run.SessionID, result.Messages)
    },
})
stream, err := svc.SubscribeEvents(ctx, einoai.SubscribeRequest{
    SessionID:    run.SessionID,
    RunID:        run.RunID,
    AfterEventID: "", // 重连时可传内部 event id
})
defer stream.Close()

if req.Stream {
    openai.SetChatCompletionStreamHeaders(w.Header())
    return openai.WriteChatCompletionStreamTo(ctx, w, flush, req, stream)
}
body, err := openai.CollectChatCompletion(ctx, req, stream)
```

`OnCompleted` 只在正常完成时调用；取消和异常不会调用。hook 错误不会改变 run 状态，应用应自行处理幂等、超时和重试。

## Response helpers

- `NewCreateRunResponse(run)` 返回创建结果。
- `NewRunResponse(run)` 返回只包含 run metadata 的状态响应，不包含 history。
- `NewCancelResponse()` 和 `NewDeleteSessionResponse()` 返回 `{ "ok": true }`。

session GET 只返回 run 状态。业务 history 必须由应用自己的 endpoint 查询；实时 stream 仍使用 OpenAI-compatible chunk，支持 reasoning extension、tool calls、finish reason 和可选 usage。


## Run 查询与生命周期

`GetRun(sessionID)` 只返回当前非终态 run；需要读取已经完成、取消或失败的 run metadata 时，使用 `NewService` 返回的 `RunLookupService.GetRunByID(sessionID, runID)`。默认 run timeout 为 10 分钟，可通过 `WithRunTimeout` 修改。
