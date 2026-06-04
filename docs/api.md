# einoai 接口文档

本文档以当前 Gin 示例程序为准。核心包 `einoai` 不依赖 Gin，HTTP 路由只是在根目录示例程序中演示如何组合 `einoai/aisdk` 和 `einoai/openai` 协议包。

## 基本信息

默认服务地址：

```text
http://127.0.0.1:8080
```

健康检查：

```http
GET /ping
```

响应：

```json
{
  "message": "pong"
}
```

当前约定：

- 一个 `sessionId` 同一时间只对应一个当前 run。
- 查询、订阅、取消都以 `sessionId` 为主，路径中的 `run_id` 只用于兼容前端路由形态，核心服务不会用它查找 run。
- 创建新 run 会覆盖该 session 的 current run 指针。
- Redis 保存 run meta、status、events、current run、error、usage、metadata。
- `agent` 不保存到 Redis，由业务方每次创建 run 时传入。
- 请求只使用标准 message 数组，不支持旧的根级 `message` 字段。
- 不会只取最后一条 user message，也不会生成默认兜底消息。

## Run 状态

| 状态 | 说明 |
| --- | --- |
| `queued` | 已创建，等待后台执行 |
| `running` | 正在执行 |
| `completed` | 正常完成 |
| `cancelled` | 已取消 |
| `failed` | 执行失败 |

Run 信息结构：

```json
{
  "session_id": "session_001",
  "run_id": "run_xxx",
  "status": "running",
  "created_at": "2026-06-04T15:30:00+08:00",
  "updated_at": "2026-06-04T15:30:01+08:00",
  "error": "",
  "metadata": {
    "protocol": "aisdk",
    "model": "gpt-4o"
  }
}
```

## 断点恢复

订阅 SSE 时支持以下任一方式指定游标：

```text
?after=<event_id>
?lastEventId=<event_id>
Last-Event-ID: <event_id>
```

服务端会从 `AfterEventID` 之后继续推送 Redis 中尚未消费的事件。客户端断开后，可使用最后收到的 SSE `id` 继续订阅。

## AI SDK / assistant-ui 协议

路由前缀：

```text
/api/usechat
```

### 直接补全并返回流

```http
POST /api/usechat/completions
```

请求体：

```json
{
  "model": "gpt-4o",
  "messages": [
    {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "帮我介绍一下这个项目"
        }
      ]
    }
  ],
  "params": {
    "traceId": "trace_001"
  }
}
```

响应为 AI SDK UI Message Stream SSE。

### 创建 run

```http
POST /api/usechat/sessions/:sessionId
```

请求体：

```json
{
  "model": "gpt-4o",
  "messages": [
    {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "北京今天适合出门吗？"
        }
      ]
    }
  ],
  "params": {
    "source": "web"
  }
}
```

响应：

```json
{
  "sessionId": "session_001",
  "run_id": "run_xxx",
  "status": "queued"
}
```

### 获取当前 run

```http
GET /api/usechat/sessions/:sessionId
```

响应：

```json
{
  "run": {
    "session_id": "session_001",
    "run_id": "run_xxx",
    "status": "running",
    "created_at": "2026-06-04T15:30:00+08:00",
    "updated_at": "2026-06-04T15:30:01+08:00",
    "metadata": {
      "protocol": "aisdk",
      "model": "gpt-4o",
      "params": {
        "source": "web"
      }
    }
  }
}
```

### 订阅 run 事件

```http
POST /api/usechat/sessions/:sessionId/runs/:run_id
```

响应头：

```text
Content-Type: text/event-stream; charset=utf-8
x-vercel-ai-ui-message-stream: v1
```

SSE 示例：

```text
id: 1748937600001-0
data: {"type":"start","messageId":"msg_run_xxx"}

id: 1748937600001-1
data: {"type":"start-step"}

id: 1748937600001-2
data: {"type":"reasoning-start","id":"reasoning_run_xxx_0"}

id: 1748937600001-3
data: {"type":"reasoning-delta","id":"reasoning_run_xxx_0","delta":"先分析问题。"}

id: 1748937600001-4
data: {"type":"reasoning-end","id":"reasoning_run_xxx_0"}

id: 1748937600001-5
data: {"type":"text-start","id":"text_run_xxx_0"}

id: 1748937600001-6
data: {"type":"text-delta","id":"text_run_xxx_0","delta":"今天北京天气不错。"}

id: 1748937600001-7
data: {"type":"text-end","id":"text_run_xxx_0"}

id: 1748937600001-8
data: {"type":"finish-step"}

id: 1748937600001-9
data: {"type":"message-metadata","messageMetadata":{"modelId":"gpt-4o"}}

id: 1748937600001-10
data: {"type":"finish","finishReason":"stop","messageMetadata":{"custom":{"usage":{"inputTokens":100,"outputTokens":50,"totalTokens":150,"cachedInputTokens":0,"inputTokenDetails":{"cacheReadTokens":0,"noCacheTokens":100},"outputTokenDetails":{"textTokens":45,"reasoningTokens":5},"reasoningTokens":5}}}}

data: [DONE]
```

工具调用事件：

```text
data: {"type":"tool-input-start","toolCallId":"call_001","toolName":"get_weather"}
data: {"type":"tool-input-delta","toolCallId":"call_001","inputTextDelta":"{\"location\":\"北京\"}"}
data: {"type":"tool-input-available","toolCallId":"call_001","toolName":"get_weather","input":{"location":"北京"}}
data: {"type":"tool-output-available","toolCallId":"call_001","output":{"temperature":26}}
```

说明：

- usage 只在最终 `finish` 事件返回。
- `message-metadata.messageMetadata.modelId` 来自 `MODEL_NAME` 环境变量。
- 中间工具步骤会发送 `finish-step` 后再发送新的 `start-step`。
- AI SDK finish reason 使用 `tool-calls`、`content-filter` 这种连字符格式。

### 取消当前 run

```http
POST /api/usechat/sessions/:sessionId/cancel
```

响应：

```json
{
  "ok": true
}
```

## OpenAI-compatible 协议

路由前缀：

```text
/api/v1
```

另外保留一个本地调试兼容路径：

```http
POST /api/chat/completions
```

### Chat Completions

```http
POST /api/v1/chat/completions
```

请求体支持 OpenAI-compatible chat completions 常见字段：

```json
{
  "model": "gpt-4o",
  "stream": true,
  "stream_options": {
    "include_usage": true
  },
  "messages": [
    {
      "role": "system",
      "content": "你是一个简洁的助手。"
    },
    {
      "role": "user",
      "content": "你好"
    }
  ],
  "temperature": 0.7,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "查询天气"
      }
    }
  ],
  "tool_choice": "auto"
}
```

Session ID 解析顺序：

1. Header `X-Session-ID`
2. Query `sessionId`
3. `openai-<model>`
4. `openai`

#### 流式响应

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

id: 1748937600001-0
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}

id: 1748937600001-1
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":5}}}

data: [DONE]
```

Reasoning 输出：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"reasoning_content":"先分析问题。"},"finish_reason":null}]}
```

Tool call 输出：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_001","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"北京\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}
```

说明：

- OpenAI 流不输出非标准 `tool_result` 字段。
- usage 只在最终非 `tool_calls` 的 `finish` chunk 返回。
- OpenAI finish reason 使用 `tool_calls`、`content_filter` 这种下划线格式。

#### 非流式响应

当 `stream` 为 `false` 或不传时，接口会聚合文本内容后返回：

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1780560000,
  "model": "gpt-4o",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好，有什么可以帮你？"
      },
      "finish_reason": "stop"
    }
  ]
}
```

### 创建 run

```http
POST /api/v1/sessions/:sessionId
```

请求体同 `/api/v1/chat/completions`，但只创建后台 run，不直接返回模型内容。

响应：

```json
{
  "sessionId": "session_001",
  "run_id": "run_xxx",
  "status": "queued"
}
```

### 获取当前 run

```http
GET /api/v1/sessions/:sessionId
```

响应：

```json
{
  "run": {
    "session_id": "session_001",
    "run_id": "run_xxx",
    "status": "running",
    "created_at": "2026-06-04T15:30:00+08:00",
    "updated_at": "2026-06-04T15:30:01+08:00",
    "metadata": {
      "protocol": "openai",
      "model": "gpt-4o"
    }
  }
}
```

### 订阅 run 事件

```http
POST /api/v1/sessions/:sessionId/runs/:run_id
```

Query 参数：

| 参数 | 说明 |
| --- | --- |
| `model` | 可选，用于 OpenAI chunk 的 `model` 字段 |
| `after` | 从指定 event id 之后恢复 |
| `lastEventId` | 同 `after` |

也支持 Header：

```text
Last-Event-ID: <event_id>
```

响应为 OpenAI-compatible streaming chunk，格式同 `/api/v1/chat/completions` 的流式响应。

### 取消当前 run

```http
POST /api/v1/sessions/:sessionId/cancel
```

响应：

```json
{
  "ok": true
}
```

## 核心包接口

业务方可以不使用示例 Gin handler，直接组合协议包函数。

```go
svc := einoai.NewService(model, redisClient)
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

AI SDK 组合示例：

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
    })
    if err != nil {
        aisdk.WriteError(c, err)
        return
    }

    aisdk.WriteCreateRunResponse(c, run)
}
```

OpenAI 组合示例：

```go
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
    })
    if err != nil {
        openai.WriteError(c, err)
        return
    }

    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID: run.SessionID,
        AfterEventID: openaiLastEventID(c),
    })
    if err != nil {
        openai.WriteError(c, err)
        return
    }
    defer stream.Close()

    openai.WriteChatCompletionStream(c, req, stream)
}
```

## 内部事件类型

核心 `RunEvent` 是协议无关事件，AI SDK 和 OpenAI 包负责把它转换成各自协议。

| 类型 | 说明 |
| --- | --- |
| `run_created` | run 已创建 |
| `run_started` | run 开始执行 |
| `text_start` | 文本块开始 |
| `text_delta` | 文本增量 |
| `text_end` | 文本块结束 |
| `reasoning_start` | reasoning 块开始 |
| `reasoning_delta` | reasoning 增量 |
| `reasoning_end` | reasoning 块结束 |
| `tool_call` | 工具调用 |
| `tool_result` | 工具结果 |
| `error` | 错误 |
| `finish` | 当前步骤或最终完成 |

Reasoning 处理规则：

- 如果 Eino 返回 `ReasoningContent`，转换为 reasoning 事件。
- 如果模型把 `<think>...</think>` 混在 `Content` 中，输出转换层拆分为 reasoning 事件和 text 事件。
- 流式分片里 `<think>` 标签被拆开时也会正确拼接和拆分。
