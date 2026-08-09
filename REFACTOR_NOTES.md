# Refactor notes

这次重构重点是让 `einoai` 更适合作为可复用的 run orchestration 包，而不是只修示例 handler。

## 核心修复

- 同一 session 创建 run 改为 Redis Lua 原子占位，避免多实例并发创建两个活动 run。
- run 终态（`completed` / `cancelled` / `failed`）不可逆，终态后事件写入会被拒绝。
- Redis event append 的“状态检查 + XADD + TTL 刷新”合并为原子 Lua 操作。
- 删除 session 后，旧 worker 不再异步执行整 session 删除，避免误删随后创建的新 run。
- 活动 worker 每秒检查一次 Redis lifecycle；其它实例执行取消或删除后，本地执行 context 会被取消。
- `DeleteSession` 对 Redis glob 字符做转义，并对扫描结果做精确 key 结构过滤，避免相似 session ID 互相误删。

## API / 使用体验

- 新增 `WithRunTimeout`，默认 10 分钟；传 `<= 0` 可关闭 service-level deadline。
- 新增 `SubscribeRequest.AfterEventID`，支持从内部 Redis Stream event id 后继续消费。
- 新增 `ErrRunActive` / `ErrRunNotFound`，便于 `errors.Is` 做协议层错误映射。
- 保留原 `Service` 方法集；新增 `RunLookupService` 扩展接口提供 `GetRunByID`。`NewService` 返回该扩展接口，因此正常调用可直接查询终态 run，同时已有只实现 `Service` 的 mock 不受影响。
- `EventStream.Close()` 现在能立即打断阻塞中的 `Next()`。
- `CreateRun` 返回的 `RunInfo` 与后台 worker 使用的对象分离，避免后台状态更新与调用方读取同一指针。
- 输入 `schema.Message` 会复制后再注入内部 message ID；包括 metadata、tool calls、旧/新多模态字段等主要可变结构，不再原地修改调用方消息。

## 协议适配

- OpenAI 兼容入口在未提供显式 session ID 时生成唯一临时 session，不再按 model 复用同一个 session。
- AI SDK 示例 `/completions` 同样使用唯一临时 session，避免无状态并发请求互相冲突。
- tool call 输出顺序改为确定性顺序，避免 map iteration 导致流输出顺序不稳定。
- 示例服务新增按 run id 查询路由，并将 `ErrRunActive` 映射为 HTTP 409、`ErrRunNotFound` 映射为 HTTP 404。
- 示例 `.env` 增加 `REDIS_TTL` 与 `RUN_TIMEOUT`。

## 行为变化与迁移提醒

1. OpenAI 请求未传 `X-Session-ID` / `sessionId` 时，现在每次请求都会获得唯一临时 session。若业务需要连续 session，请显式传 session ID。
2. `GetRun(sessionID)` 仍只表示当前非终态 run；终态 run 请通过 `RunLookupService.GetRunByID` 查询。
3. `AfterEventID` 是内部事件游标。协议适配层没有自动把 HTTP `Last-Event-ID` 映射到它，因为一个内部事件可能展开为多个 SSE frame。
4. lifecycle watcher 默认每个活动 run 每秒读取一次 Redis run metadata，以换取跨实例 cancel/delete 的传播能力。

## 验证建议

项目 `go.mod` 使用 Go 1.25。建议在 Go 1.25 环境执行：

```bash
go test ./...
go test -race ./...
```

特别关注 Redis Lua/miniredis 测试、并发 run 创建、event stream close/resume、以及业务侧 completion hook 的集成测试。
