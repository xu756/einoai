# aisdk 包

`github.com/xu756/einoai/aisdk` 是 AI SDK / assistant-ui 协议适配包。它负责把前端 UIMessage 请求转换成 Eino `[]*schema.Message`，并把核心 `einoai.RunEvent` 输出为 AI SDK UI Message Stream SSE。

这个包不注册路由，也不依赖 Gin。普通请求解析、响应构造和 SSE 写出都通过框架无关函数完成；示例服务里的 Gin handler 只是自己把 `gin.Context` 的 writer 接进来。

## 职责

- 解码 AI SDK / assistant-ui 请求体。
- 将 `messages: UIMessage[]` 转成 Eino `[]*schema.Message`。
- 将 Redis 中保存的 `[]*schema.Message` 转成统一的协议无关 session 消息。
- 将核心事件写成 AI SDK UI Message Stream。
- 返回创建 run、查询 run、取消 run 的响应结构体。

## 请求结构

```go
type CreateRunRequest struct {
    Messages []Message      `json:"messages"`
    Model    string         `json:"model,omitempty"`
    Params   map[string]any `json:"params,omitempty"`
}
```

`messages` 不能为空，不支持旧的根级 `message` 字段。

`Message` 对齐 AI SDK UIMessage：

```go
type Message struct {
    ID       string         `json:"id,omitempty"`
    Role     string         `json:"role"`
    Metadata map[string]any `json:"metadata,omitempty"`
    Parts    []Part         `json:"parts"`
}
```

`Part` 支持 UIMessage 常用字段：

```go
type Part struct {
    ID               string         `json:"id,omitempty"`
    Type             string         `json:"type"`
    Text             string         `json:"text,omitempty"`
    State            string         `json:"state,omitempty"`
    Data             any            `json:"data,omitempty"`
    ToolCallID       string         `json:"toolCallId,omitempty"`
    Input            any            `json:"input,omitempty"`
    Output           any            `json:"output,omitempty"`
    ErrorText        string         `json:"errorText,omitempty"`
    ProviderExecuted *bool          `json:"providerExecuted,omitempty"`
    URL              string         `json:"url,omitempty"`
    MediaType        string         `json:"mediaType,omitempty"`
    Filename         string         `json:"filename,omitempty"`
    SourceID         string         `json:"sourceId,omitempty"`
    Title            string         `json:"title,omitempty"`
    ProviderMetadata map[string]any `json:"providerMetadata,omitempty"`
}
```

示例：

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

## 消息转换规则

请求进入核心层：

```go
messages, err := aisdk.ToSchemaMessages(req)
```

session 历史从核心层返回给前端：

```go
resp, err := aisdk.NewRunResponse(run, schemaMessages)
// resp.Messages 是 []einoai.SessionMessage
```

转换要点：

- user/system 文本 part 转为 schema `Content`。
- user file part 转为 schema `UserInputMultiContent`。
- 纯文本 user 消息不会同时设置 `Content` 和 `UserInputMultiContent`，避免 OpenAI ChatModel 报 `can't use both Content and MultiContent`。
- assistant `step-start` 会拆分成多个 schema assistant step。
- assistant `reasoning` 转为 schema `ReasoningContent`。
- assistant `tool-*` 转为 schema assistant `ToolCalls`。
- `tool-*` 的 `output-available` / `output-error` 会额外生成 schema tool message。
- `FromSchemaMessages` 仍可用于需要 AI SDK UIMessage 的自定义展示；session GET 不调用它。
- `id` 和 `metadata` 会保存在 schema message `Extra` 中，用于回放时还原。

## 常用函数

| 函数 | 用途 |
| --- | --- |
| `DecodeCreateRunRequest(body)` | 解码创建 run 请求 |
| `DecodeCompletionsRequest(body)` | 解码直接 completions 请求，结构同创建 run |
| `ToSchemaMessages(req)` | 转换 AI SDK UIMessage 为 Eino `[]*schema.Message` |
| `FromSchemaMessages(messages)` | 转换 Eino `[]*schema.Message` 为 `[]aisdk.Message` |
| `NewCreateRunResponse(run)` | 构造创建 run JSON 响应结构体 |
| `NewRunResponse(run, messages)` | 构造统一 session 响应，返回 `(RunResponse, error)` |
| `NewCancelResponse()` | 构造取消 run JSON 响应结构体 |
| `NewDeleteSessionResponse()` | 构造删除 session JSON 响应结构体 |
| `SetEventStreamHeaders(header)` | 设置 AI SDK SSE 响应头 |
| `WriteEventStreamTo(ctx, writer, flush, stream)` | 写 AI SDK SSE 到任意 `io.Writer` |

## 组合示例

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

示例 server 不会把 AI SDK 请求的 `model` 或 `params` 写入 run metadata。Eino 模型只由传入 `Agent` 的配置决定。

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

session GET 返回协议无关的 `messages[].parts`，与 OpenAI session 端点完全相同。它不再返回合并后的 AI SDK `UIMessage` 历史；text、reasoning、工具调用/结果、多模态和 usage 都按原始 Eino 消息顺序独立保留。这是 breaking change，实时 UI Message Stream 不受影响。

订阅事件：

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

非 Gin 框架也是同一组通用 writer：

```go
aisdk.SetEventStreamHeaders(header)

err := aisdk.WriteEventStreamTo(
    ctx,
    writer,
    flush,
    stream,
)
```

`writer` 只需要实现 `io.Writer`，`flush` 可以为 `nil`。Hertz 等框架可以把自己的 stream writer 接到这里。

取消 run：

```go
func (h *Handler) CancelAIRun(c *gin.Context) {
    err := h.AIService.CancelRun(c.Request.Context(), c.Param("sessionId"), c.Param("run_id"))
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusAccepted, aisdk.NewCancelResponse())
}
```

删除 session：

```go
func (h *Handler) DeleteAISession(c *gin.Context) {
    err := h.AIService.DeleteSession(c.Request.Context(), c.Param("sessionId"))
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusAccepted, aisdk.NewDeleteSessionResponse())
}
```

## SSE 输出要点

`SetEventStreamHeaders` 会设置：

```text
Content-Type: text/event-stream; charset=utf-8
Cache-Control: no-cache, no-transform
Connection: keep-alive
X-Accel-Buffering: no
x-vercel-ai-ui-message-stream: v1
```

主要事件：

```text
data: {"type":"start","messageId":"msg_run_xxx"}
data: {"type":"start-step"}
data: {"type":"reasoning-start","id":"reasoning_run_xxx_0"}
data: {"type":"reasoning-delta","id":"reasoning_run_xxx_0","delta":"..."}
data: {"type":"reasoning-end","id":"reasoning_run_xxx_0"}
data: {"type":"text-start","id":"text_run_xxx_0"}
data: {"type":"text-delta","id":"text_run_xxx_0","delta":"..."}
data: {"type":"text-end","id":"text_run_xxx_0"}
data: {"type":"finish-step"}
data: {"type":"message-metadata","messageMetadata":{"modelId":"gpt-4o"}}
data: {"type":"finish","finishReason":"stop","messageMetadata":{"custom":{"usage":{"inputTokens":100,"outputTokens":50,"totalTokens":150}}}}
data: [DONE]
```

工具事件：

```text
data: {"type":"tool-input-start","toolCallId":"call_001","toolName":"get_weather"}
data: {"type":"tool-input-delta","toolCallId":"call_001","inputTextDelta":"{\"location\":\"北京\"}"}
data: {"type":"tool-input-available","toolCallId":"call_001","toolName":"get_weather","input":{"location":"北京"}}
data: {"type":"tool-output-available","toolCallId":"call_001","output":{"temperature":26}}
```

说明：

- `toolName` 和 `toolCallId` 来自 Eino tool call，不生成 `toolName: "tool"` 之类的兜底值。
- `finish-step` 当前只输出 `type`。
- usage 会在最终 `finish.messageMetadata.custom.usage` 中返回；session 历史则使用统一消息的顶层 `usage` 结构。
- `tool_calls`、`content_filter` 会输出为 AI SDK 的 `tool-calls`、`content-filter`。
- 当前订阅直接订阅指定 `runID`，不读取 `Last-Event-ID`。

## 错误格式

普通 JSON 错误由业务 handler 决定。示例服务使用：

```json
{
  "error": "messages is required"
}
```

如果 SSE 已开始写入，则输出：

```text
data: {"type":"error","errorText":"some error"}
data: [DONE]
```
