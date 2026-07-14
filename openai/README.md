# openai 包

`github.com/xu756/einoai/openai` 是 OpenAI-compatible 协议适配包。它负责解码 chat completions 请求、转换 OpenAI messages，并把核心 `einoai.RunEvent` 输出为 OpenAI-compatible streaming chunk。

这个包不注册 `/v1/chat/completions` 或任何固定路由，也不依赖 Gin。普通请求解析、响应构造和 SSE 写出都通过框架无关函数完成；示例服务里的 Gin handler 只是自己把 `gin.Context` 的 writer 接进来。

## 职责

- 解码 OpenAI-compatible chat completions 请求。
- 将 OpenAI `messages` 转成 Eino `[]*schema.Message`。
- 将 Redis 中保存的 `[]*schema.Message` 转成统一的协议无关 session 消息。
- 解析业务 session ID。
- 输出 OpenAI-compatible SSE stream chunk。
- 聚合非流式 chat completions 响应。
- 返回创建 run、查询 run、取消 run 的响应结构体。

## 请求结构

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

`messages` 不能为空。当前转换会保留全部消息，不会只取最后一条 user message。

消息结构：

```go
type ChatMessage struct {
    Role             string          `json:"role"`
    Content          json.RawMessage `json:"content"`
    Name             string          `json:"name,omitempty"`
    ReasoningContent string          `json:"reasoning_content,omitempty"`
    ToolCallID       string          `json:"tool_call_id,omitempty"`
    ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
}
```

`content` 支持字符串和 content parts 数组。转换完整支持 `text`、`image_url`、`input_audio`、兼容的 `video_url` 与 `file`；格式错误或未知 part 会返回明确错误。

## 消息转换规则

请求进入核心层：

```go
messages, err := openai.ToSchemaMessages(req)
```

session 历史从核心层返回给调用方：

```go
resp, err := openai.NewRunResponse(run, schemaMessages)
// resp.Messages 是 []einoai.SessionMessage
```

转换要点：

- `system`、`user`、`assistant`、`tool` role 会映射到 Eino schema role。
- assistant `tool_calls` 会保留 `id`、`type`、`index`、`function.name`、`function.arguments`。
- tool message 会保留 `tool_call_id`。
- `FromSchemaMessages` 仍可用于自定义 OpenAI message 转换；session GET 不调用它。

## 常用函数

| 函数 | 用途 |
| --- | --- |
| `DecodeChatCompletionsRequest(body)` | 解码 OpenAI-compatible 请求体 |
| `ResolveSessionID(req, candidates...)` | 从业务传入候选值中解析 session ID |
| `ToSchemaMessages(req)` | 转换 OpenAI messages 为 Eino `[]*schema.Message` |
| `FromSchemaMessages(messages)` | 转换 Eino `[]*schema.Message` 为 `[]openai.ChatMessage` |
| `NewCreateRunResponse(run)` | 构造创建 run JSON 响应结构体 |
| `NewRunResponse(run, messages)` | 构造统一 session 响应，返回 `(RunResponse, error)` |
| `NewCancelResponse()` | 构造取消 run JSON 响应结构体 |
| `NewDeleteSessionResponse()` | 构造删除 session JSON 响应结构体 |
| `SetChatCompletionStreamHeaders(header)` | 设置 OpenAI-compatible SSE 响应头 |
| `WriteChatCompletionStreamTo(ctx, writer, flush, req, stream)` | 写 OpenAI-compatible SSE 到任意 `io.Writer` |
| `CollectChatCompletion(ctx, req, stream)` | 聚合非流式响应 body |

## Session ID 解析

`ResolveSessionID(req, candidates...)` 按以下顺序返回：

1. 第一个非空 candidate，例如 Header `X-Session-ID` 或 Query `sessionId`
2. `openai-<model>`
3. `openai`

示例：

```go
sessionID := openai.ResolveSessionID(
    req,
    c.GetHeader("X-Session-ID"),
    c.Query("sessionId"),
)
```

## 组合示例

```go
func (h *Handler) ChatCompletions(c *gin.Context) {
    req, err := openai.DecodeChatCompletionsRequest(c.Request.Body)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    messages, err := openai.ToSchemaMessages(req)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    agent, err := h.ResolveAgent(c.Request.Context())
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    run, err := h.AIService.CreateRun(c.Request.Context(), einoai.CreateRunRequest{
        SessionID: openai.ResolveSessionID(req, c.GetHeader("X-Session-ID"), c.Query("sessionId")),
        Messages:  messages,
        Agent:     agent,
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    stream, err := h.AIService.SubscribeEvents(c.Request.Context(), einoai.SubscribeRequest{
        SessionID: run.SessionID,
        RunID:     run.RunID,
    })
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    defer stream.Close()

    if req.Stream {
        openai.SetChatCompletionStreamHeaders(c.Writer.Header())
        _ = openai.WriteChatCompletionStreamTo(c.Request.Context(), c.Writer, c.Writer.Flush, req, stream)
        return
    }

    body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    c.JSON(http.StatusOK, body)
}
```

示例 server 不会把 OpenAI 请求的 `model` 或协议名称写入 run metadata。Eino 模型只由传入 `Agent` 的配置决定；`model` 仍用于 OpenAI 响应 chunk 和默认 session ID。

非 Gin 框架也是同一组通用 writer：

```go
openai.SetChatCompletionStreamHeaders(header)

err := openai.WriteChatCompletionStreamTo(
    ctx,
    writer,
    flush,
    req,
    stream,
)
```

`writer` 只需要实现 `io.Writer`，`flush` 可以为 `nil`。Hertz 等框架可以把自己的 stream writer 接到这里。

查询 run 和统一 session 历史：

```go
func (h *Handler) GetOpenAIRun(c *gin.Context) {
    sessionID := c.Param("sessionId")

    run, err := h.AIService.GetRun(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    messages, err := h.AIService.GetMessages(c.Request.Context(), sessionID)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    response, err := openai.NewRunResponse(run, messages)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }
    c.JSON(http.StatusOK, response)
}
```

session GET 不再返回 OpenAI `ChatMessage` 数组，而是与 AI SDK 端点相同的协议无关 `messages[].parts`。支持文本、reasoning、工具调用/结果、多模态、finish reason 和 usage。这是 breaking change；实时 Chat Completions 流仍使用 OpenAI-compatible chunk。

取消 run：

```go
func (h *Handler) CancelOpenAIRun(c *gin.Context) {
    err := h.AIService.CancelRun(c.Request.Context(), c.Param("sessionId"), c.Param("run_id"))
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    c.JSON(http.StatusAccepted, openai.NewCancelResponse())
}
```

删除 session：

```go
func (h *Handler) DeleteOpenAISession(c *gin.Context) {
    err := h.AIService.DeleteSession(c.Request.Context(), c.Param("sessionId"))
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
        return
    }

    c.JSON(http.StatusAccepted, openai.NewDeleteSessionResponse())
}
```

## 流式输出

`WriteChatCompletionStreamTo` 会输出 OpenAI-compatible SSE：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":5}}}

data: [DONE]
```

Reasoning 输出使用常见的 OpenAI-compatible 扩展 `reasoning_content`；它不是 OpenAI 官方 Chat Completions 标准字段：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"reasoning_content":"先分析问题。"},"finish_reason":null}]}
```

Tool call 输出：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_001","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":\"北京\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}
```

说明：

- OpenAI 流不会输出非标准 `tool_result` 字段。
- tool call 首个 delta 输出 `id`、`type`、`function.name`，后续 arguments delta 不重复这些字段。
- `stream_options.include_usage=true` 时，最终非 `tool_calls` finish 后会追加一个空 `choices` 的 usage chunk。
- 未请求 usage 时 chunk 不包含 `usage`；请求后普通 chunk 为 `"usage":null`。
- SSE 只输出 `data:` 行和最终 `[DONE]`，不额外输出 SSE `id:` 行。
- finish reason 使用 OpenAI 的 `tool_calls`、`content_filter` 下划线格式。

## 非流式输出

`CollectChatCompletion` 会聚合 text、reasoning、最终工具调用和 usage。自动执行完成的中间工具调用只保留在统一 session 历史，不泄漏到最终 assistant message：

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
        "content": "你好，有什么可以帮你？",
        "reasoning_content": "先分析问题。"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150,
    "prompt_tokens_details": {"cached_tokens": 0},
    "completion_tokens_details": {"reasoning_tokens": 5}
  }
}
```

OpenAI content parts 会完整转换 `text`、`image_url`、`input_audio`、兼容的 `video_url` 和 `file`。缺少必填 URL/data 或遇到未知 part type 时返回明确请求错误，不再静默丢弃非文本内容。

## 错误格式

普通 JSON 错误由业务 handler 决定。示例服务使用 OpenAI 风格：

```json
{
  "error": {
    "message": "messages is required",
    "type": "invalid_request_error"
  }
}
```

SSE 错误：

```text
data: {"error":{"message":"some error","type":"server_error"}}
data: [DONE]
```
