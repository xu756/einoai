# einoai API 文档

本文档描述 `cmd/server` 示例 Gin 服务暴露的 HTTP API，以及 `einoai` 核心包和协议包的组合方式。核心包 `github.com/xu756/einoai` 本身不依赖 Gin，HTTP 路由只是示例服务如何组合 `aisdk` 与 `openai` 的参考。

## 基本信息

### 默认服务地址

```text
http://127.0.0.1:8080
```

示例服务实际监听地址由 `HTTP_ADDR` 控制，默认值是 `:8080`。

### 健康检查

```http
GET /ping
```

响应：

```json
{
  "message": "pong"
}
```

## 核心约定

- 一个 `sessionId` 同一时间只对应一个 current run。
- 查询、删除以 `sessionId` 为主；订阅、取消以 `sessionId + runID` 为主。
- 创建新 run 会覆盖该 session 的 current run 指针。
- Redis 保存 run meta、status、events、current run、error、usage、metadata。
- `adk.Agent` 不保存到 Redis，由业务方每次创建 run 时传入。
- 请求只使用标准 `messages` 数组，不支持旧的根级 `message` 字段。
- 不会只取最后一条 user message。
- 不会生成默认兜底消息。
- 删除 session 会删除 history、active snapshot、run meta、events 和 current run；如果有运行中 run 会先中断。

## Run 状态

| 状态 | 说明 |
| --- | --- |
| `queued` | run 已创建，等待后台执行 |
| `running` | run 正在执行 |
| `completed` | run 正常完成 |
| `cancelled` | run 已取消 |
| `failed` | run 执行失败 |

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

## 事件订阅

订阅 SSE 时需要指定 `runID`：

```text
POST /api/usechat/sessions/:sessionId/runs/:run_id
POST /api/v1/sessions/:sessionId/runs/:run_id
```

当前核心服务不读取 `Last-Event-ID`，重新连接时直接按 `sessionID + runID` 订阅该 run。

## AI SDK / assistant-ui 协议

路由前缀：

```text
/api/usechat
```

请求体类型由 `aisdk.CreateRunRequest` 定义：

```go
type CreateRunRequest struct {
    Messages []Message      `json:"messages"`
    Model    string         `json:"model,omitempty"`
    Params   map[string]any `json:"params,omitempty"`
}
```

`messages` 不能为空。`Message` 支持 `role`、`parts`、`content`、`metadata`、`data`；`parts` 支持 `text`、`image`、`file`，并可携带 `url`、`mediaType`、`filename`。

### 直接补全并返回流

```http
POST /api/usechat/completions
```

该接口创建一个固定 session ID 为 `usechat-completions` 的 run，并直接返回 AI SDK SSE 流。

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
    "source": "web"
  }
}
```

响应为 `text/event-stream`。

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

如果 current run 不存在，核心服务返回 `run: null`：

```json
{
  "run": null
}
```

### 订阅当前 run 事件

```http
POST /api/usechat/sessions/:sessionId/runs/:run_id
```

响应头：

```text
Content-Type: text/event-stream; charset=utf-8
Cache-Control: no-cache, no-transform
Connection: keep-alive
X-Accel-Buffering: no
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
data: {"type":"finish","finishReason":"stop","messageMetadata":{"custom":{"usage":{"inputTokens":2846,"inputTokenDetails":{"noCacheTokens":1182,"cacheReadTokens":1664},"outputTokens":642,"outputTokenDetails":{"textTokens":642,"reasoningTokens":0},"totalTokens":3488,"reasoningTokens":0,"cachedInputTokens":1664}}}}

data: [DONE]
```

说明：

- `finish-step` 当前只输出 `type`，不携带 `finishReason`。
- `message-metadata.messageMetadata.modelId` 来自 `MODEL_NAME` 环境变量。
- usage 会在最终 `finish.messageMetadata.custom.usage` 中返回；历史消息回放时，assistant message 的 `metadata.custom.usage` 也会保留同样的结构。
- AI SDK finish reason 会把核心事件中的 `tool_calls`、`content_filter` 转成 `tool-calls`、`content-filter`。
- 如果是工具调用中间步骤，会先输出 `finish-step`，然后输出新的 `start-step` 继续下一步。

工具调用事件：

```text
data: {"type":"tool-input-start","toolCallId":"call_001","toolName":"get_weather"}
data: {"type":"tool-input-delta","toolCallId":"call_001","inputTextDelta":"{\"location\":\"北京\"}"}
data: {"type":"tool-input-available","toolCallId":"call_001","toolName":"get_weather","input":{"location":"北京"}}
data: {"type":"tool-output-available","toolCallId":"call_001","output":{"temperature":26}}
```

### 取消 run

```http
POST /api/usechat/sessions/:sessionId/runs/:run_id/cancel
```

响应：

```json
{
  "ok": true
}
```

### 删除 session

```http
DELETE /api/usechat/sessions/:sessionId
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

请求体类型由 `openai.ChatCompletionsRequest` 定义，支持常见 OpenAI-compatible chat completions 字段：

```go
type ChatCompletionsRequest struct {
    Model               string            `json:"model"`
    Messages            []ChatMessage     `json:"messages"`
    Stream              bool              `json:"stream,omitempty"`
    StreamOptions       *StreamOptions    `json:"stream_options,omitempty"`
    Temperature         *float64          `json:"temperature,omitempty"`
    TopP                *float64          `json:"top_p,omitempty"`
    MaxTokens           *int              `json:"max_tokens,omitempty"`
    MaxCompletionTokens *int              `json:"max_completion_tokens,omitempty"`
    N                   *int              `json:"n,omitempty"`
    Stop                json.RawMessage   `json:"stop,omitempty"`
    PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
    FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
    LogitBias           map[string]int    `json:"logit_bias,omitempty"`
    User                string            `json:"user,omitempty"`
    ResponseFormat      json.RawMessage   `json:"response_format,omitempty"`
    Tools               []Tool            `json:"tools,omitempty"`
    ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
    ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
    Metadata            map[string]string `json:"metadata,omitempty"`
}
```

`messages` 不能为空。

### Chat Completions

```http
POST /api/v1/chat/completions
```

请求示例：

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

- OpenAI 流不会输出非标准 `tool_result` 字段。
- usage 只在最终非 `tool_calls` 的 finish chunk 返回。
- `stream_options.include_usage` 可以被请求绑定，但当前实现只要最终 `finish` 事件中有 usage 就会写出 `usage` 字段。
- OpenAI finish reason 使用 `tool_calls`、`content_filter` 这种下划线格式。

#### 非流式响应

当 `stream` 为 `false` 或不传时，示例服务会聚合 `text_delta` 后返回：

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

请求体同 `/api/v1/chat/completions`，但该接口只创建后台 run，不直接返回模型内容。

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

### 订阅当前 run 事件

```http
POST /api/v1/sessions/:sessionId/runs/:run_id
```

Query 参数：

| 参数 | 说明 |
| --- | --- |
| `model` | 可选，用于 OpenAI chunk 的 `model` 字段 |

响应为 OpenAI-compatible streaming chunk，格式同 `/api/v1/chat/completions` 的流式响应。

### 取消 run

```http
POST /api/v1/sessions/:sessionId/runs/:run_id/cancel
```

响应：

```json
{
  "ok": true
}
```

### 删除 session

```http
DELETE /api/v1/sessions/:sessionId
```

响应：

```json
{
  "ok": true
}
```

### 本地调试兼容路径

示例服务还注册了一个 OpenAI chat completions 兼容调试路径：

```http
POST /api/chat/completions
```

处理逻辑与 `/api/v1/chat/completions` 相同。

## 核心包接口

核心包导入路径：

```go
import "github.com/xu756/einoai"
```

创建 Service：

```go
svc := einoai.NewService(redisClient)
svcWithTTL := einoai.NewService(
    redisClient,
    einoai.WithRedisTTL(einoai.DefaultRedisTTL),
)
```

真实核心接口：

```go
type Service interface {
    CreateRun(ctx context.Context, req CreateRunRequest) (*RunInfo, error)
    GetRun(ctx context.Context, sessionID string) (*RunInfo, error)
    GetMessages(ctx context.Context, sessionID string) ([]*schema.Message, error)
    DeleteSession(ctx context.Context, sessionID string) error
    CancelRun(ctx context.Context, sessionID string, runID string) error
    SubscribeEvents(ctx context.Context, req SubscribeRequest) (EventStream, error)
}
```

请求结构：

```go
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
    })
    if err != nil {
        openai.WriteError(c, err)
        return
    }
    defer stream.Close()

    openai.SetChatCompletionStreamHeaders(c.Writer.Header())
    _ = openai.WriteChatCompletionStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, req, stream)
}
```

## 内部事件类型

核心 `RunEvent` 是协议无关事件，协议包负责将它转换为 AI SDK 或 OpenAI 输出。

| 类型 | 用途 |
| --- | --- |
| `run_created` | run 已创建 |
| `run_started` | run 开始执行 |
| `text_start` | 文本块开始 |
| `text_delta` | 文本增量 |
| `text_end` | 文本块结束 |
| `reasoning_start` | reasoning 块开始 |
| `reasoning_delta` | reasoning 增量 |
| `reasoning_end` | reasoning 块结束 |
| `tool_call` | 工具调用参数 |
| `tool_result` | 工具执行结果 |
| `error` | run 或流式执行错误 |
| `finish` | 当前步骤或最终完成，携带 finish reason 和 usage |

## Reasoning 处理规则

- 如果 Eino 返回 `ReasoningContent`，转换为 reasoning 事件。
- 如果模型把 `<think>...</think>` 混在 `Content`，输出转换层拆成 reasoning 事件和 text 事件。
- 流式分片里 `<think>` 标签被拆开时也会正确处理。

## 错误响应

不同协议包的错误格式不同。

AI SDK 普通 JSON 错误：

```json
{
  "error": "messages is required"
}
```

AI SDK SSE 已开始写入后的错误：

```text
data: {"type":"error","errorText":"some error"}

data: [DONE]
```

OpenAI 普通 JSON 错误：

```json
{
  "error": {
    "message": "messages is required",
    "type": "invalid_request_error"
  }
}
```

OpenAI SSE 错误：

```text
data: {"error":{"message":"some error","type":"server_error"}}

data: [DONE]
```

常见错误：

| 场景 | 错误 |
| --- | --- |
| `messages` 为空 | `messages is required` |
| `sessionID` 为空 | `sessionID is required` |
| `agent` 为空 | `agent is required` |
| 订阅不存在的 current run | `run for session <sessionId> not found` |
