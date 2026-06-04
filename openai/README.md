# openai 包

`github.com/xu756/einoai/openai` 是 enio-ai 的 OpenAI-compatible 协议适配包。它负责绑定 chat completions 请求、转换 OpenAI messages，并把核心 `einoai.RunEvent` 输出为 OpenAI-compatible streaming chunk。

这个包可以依赖 Gin，但不强制注册 `/v1/chat/completions` 或任何固定路由。业务方可以在自己的 handler 中先完成鉴权、限流、用户态 agent 选择、计费、日志和参数校验，再组合调用本包函数。

## 职责

- 绑定 OpenAI-compatible chat completions 请求。
- 将 OpenAI `messages` 转成 Eino `[]*schema.Message`。
- 解析业务 session ID。
- 输出 OpenAI-compatible SSE stream chunk。
- 聚合非流式 chat completions 响应。
- 输出 OpenAI 风格错误响应。

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
    Role       string          `json:"role"`
    Content    json.RawMessage `json:"content"`
    Name       string          `json:"name,omitempty"`
    ToolCallID string          `json:"tool_call_id,omitempty"`
    ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
}
```

`content` 支持字符串，也支持 OpenAI content parts 数组；当前转换会提取其中 `type: "text"` 的文本。

## 常用函数

| 函数 | 用途 |
| --- | --- |
| `BindChatCompletionsRequest(c)` | 绑定 OpenAI-compatible 请求体 |
| `ToSchemaMessages(req)` | 转换为 Eino `[]*schema.Message` |
| `ResolveSessionID(c, req)` | 解析 session ID |
| `WriteChatCompletionStream(c, req, stream)` | 写 OpenAI-compatible SSE 流 |
| `CollectChatCompletion(ctx, req, stream)` | 聚合非流式响应 body |
| `WriteError(c, err)` | 写 OpenAI-compatible JSON 错误 |
| `WriteStreamError(c, err)` | 写 OpenAI-compatible SSE 错误 |
| `HandleChatCompletions(c, svc, sessionID, agent)` | 可选便捷函数 |

## Session ID 解析

`ResolveSessionID` 按以下顺序返回：

1. Header `X-Session-ID`
2. Query `sessionId`
3. `openai-<model>`
4. `openai`

## 组合示例

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
        Metadata: map[string]any{
            "protocol": "openai",
            "model":    req.Model,
        },
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

    if req.Stream {
        openai.WriteChatCompletionStream(c, req, stream)
        return
    }

    body, err := openai.CollectChatCompletion(c.Request.Context(), req, stream)
    if err != nil {
        openai.WriteError(c, err)
        return
    }
    c.JSON(http.StatusOK, body)
}
```

## 流式输出

`WriteChatCompletionStream` 会输出 OpenAI-compatible SSE：

```text
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

id: 1748937600001-0
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}

id: 1748937600001-1
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1780560000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}

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
- `stream_options.include_usage` 可以被绑定，但当前实现只要最终 `finish` 事件中有 usage 就会写出 `usage` 字段。
- finish reason 使用 `tool_calls`、`content_filter` 这种下划线格式。

## 非流式输出

`CollectChatCompletion` 会聚合 `text_delta` 并返回：

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

## 错误格式

普通 JSON 错误：

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
