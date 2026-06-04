# aisdk 包

`github.com/xu756/einoai/aisdk` 是 enio-ai 的 AI SDK / assistant-ui 协议适配包。它负责把前端 AI SDK 请求转换成 Eino `[]*schema.Message`，并把核心 `einoai.RunEvent` 输出为 AI SDK UI Message Stream SSE。

这个包可以依赖 Gin，但它不是路由注册器。业务方应该在自己的 Gin handler 中完成鉴权、限流、日志、agent 选择、参数校验、埋点等逻辑后，再组合调用本包函数。

## 职责

- 绑定 AI SDK / assistant-ui 请求体。
- 将 `messages` 转成 Eino `[]*schema.Message`。
- 从 query/header 解析断点恢复游标。
- 将核心事件写成 AI SDK UI Message Stream。
- 写出创建 run、查询 run、取消 run、错误等响应。
- 提供可选的便捷 handler 函数。

## 请求结构

```go
type CreateRunRequest struct {
    Messages []Message      `json:"messages"`
    Model    string         `json:"model,omitempty"`
    Params   map[string]any `json:"params,omitempty"`
}
```

`messages` 不能为空，不支持旧的根级 `message` 字段。

```go
type Message struct {
    Role     string         `json:"role,omitempty"`
    Parts    []Part         `json:"parts,omitempty"`
    Content  string         `json:"content,omitempty"`
    Metadata map[string]any `json:"metadata,omitempty"`
    Data     map[string]any `json:"data,omitempty"`
}

type Part struct {
    Type      string `json:"type,omitempty"`
    Text      string `json:"text,omitempty"`
    URL       string `json:"url,omitempty"`
    MediaType string `json:"mediaType,omitempty"`
    Filename  string `json:"filename,omitempty"`
}
```

`parts` 支持文本、图片和文件输入。`image/file` 会根据 `mediaType` 转换为 Eino 的多模态 message part；`data:` URL 会拆出 base64 数据。

## 常用函数

| 函数 | 用途 |
| --- | --- |
| `BindCreateRunRequest(c)` | 绑定创建 run 请求 |
| `BindCompletionsRequest(c)` | 绑定直接 completions 请求，结构同创建 run |
| `ToSchemaMessages(req)` | 转换为 Eino `[]*schema.Message` |
| `GetLastEventID(c)` | 从 `after`、`lastEventId` 或 `Last-Event-ID` 读取恢复游标 |
| `WriteCreateRunResponse(c, run)` | 写创建 run 响应 |
| `WriteRunResponse(c, run)` | 写查询 run 响应 |
| `WriteCancelResponse(c, err)` | 写取消响应 |
| `WriteEventStream(c, stream)` | 写 AI SDK SSE 流 |
| `WriteError(c, err)` | 写 JSON 错误或 SSE 错误 |

便捷函数：

| 函数 | 用途 |
| --- | --- |
| `HandleCreateRun` | 已有 messages 和 agent 时创建 run |
| `HandleGetRun` | 查询当前 run |
| `HandleCancelRun` | 取消当前 run |
| `HandleSubscribeEvents` | 订阅事件并写流 |
| `HandleCompletions` | 绑定请求、创建 run、订阅并写流 |

便捷函数只是可选入口，不会强制注册路由。

## 组合示例

创建 run：

```go
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
        SessionID:     c.Param("sessionId"),
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

## SSE 输出要点

`WriteEventStream` 会设置：

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

- `message-metadata.messageMetadata.modelId` 来自环境变量 `MODEL_NAME`。
- usage 只在最终 `finish.messageMetadata.custom.usage` 中返回。
- `finish-step` 当前只输出 `type`，不携带 `finishReason`。
- 核心 finish reason 中的 `tool_calls`、`content_filter` 会输出为 `tool-calls`、`content-filter`。

## 错误格式

普通 JSON 错误：

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
