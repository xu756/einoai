# Chat API

流生命周期在后端，前端只订阅 SSE。Session 不在后端保存，前端自己维护。

## Run 状态

| 状态 | 含义 |
|------|------|
| `running` | 运行中 |
| `canceling` | 取消中 |
| `done` | 正常结束 |
| `error` | 出错 |
| `canceled` | 被取消 |

---

## 接口列表

### 1. 创建 Run

**POST** `/api/chat/sessions/:sessionId`

发送消息，启动一个 Eino Agent Run。

**请求体**

```json
{
  "message": "你好",
  "agent": "chat"  // 可选，默认 "chat"
}
```

**响应 202**

```json
{
  "sessionId": "test111",
  "runId": "54ea1a309853ccd1da77bd68b1296c84",
  "status": "running"
}
```

---

### 2. 查询 Session 当前 Run

**GET** `/api/chat/sessions/:sessionId`

查询该 session 当前是否有一个 active run，用于前端判断是否需要订阅 SSE。

**响应 200**

有 active run：

```json
{
  "sessionId": "test111",
  "run": {
    "runId": "54ea1a309853ccd1da77bd68b1296c84",
    "message": "你好",
    "status": "running",
    "createdAt": 1748937600000
  }
}
```

无 active run（run 已结束或从未创建）：

```json
{
  "sessionId": "test111",
  "run": null
}
```

> run 结束后 current run 指针会被自动清除，不再回放旧 Stream。

---

### 3. 订阅 Run 事件流

**POST** `/api/chat/sessions/:sessionId/runs/:runId`

SSE 订阅该 run 的 chunk 事件流。必须带上正确的 `runId`。

**Query 参数**

| 参数 | 说明 |
|------|------|
| `after` | 从指定 eventId 之后开始读，支持 SSE `Last-Event-ID` header |
| `lastEventId` | 同 `after` |
| `Last-Event-ID` | 同 `after`（header） |

**SSE 事件**

```
id: 1748937600001-0
data: {"choices":[{"delta":{"content":"你"}}]}

id: 1748937600001-1
data: {"choices":[{"delta":{"content":"好"}}]}

id: 1748937600001-2
data: [DONE]
```

**规则**

- `runId` 必须是该 session 的当前 active run，否则直接返回 `data: [DONE]`，不会串流到其他 run。
- run 结束后 current run 被清掉，同一 `runId` 再订阅也只会收到 `data: [DONE]`。
- 无数据时定期发送 `: ping` 心跳，防止连接断开。
- 出现错误时发送 error 对象后结束流。

---

### 4. 取消 Run

**POST** `/api/chat/sessions/:sessionId/cancel/:runId`

取消指定 run，只对当前 session 的当前 active run 生效。

**响应 202**（成功取消）

```json
{
  "sessionId": "test111",
  "run": {
    "runId": "54ea1a309853ccd1da77bd68b1296c84",
    "status": "canceled"
  }
}
```

**响应 200**（无 active run 或 runId 不匹配）

```json
{
  "sessionId": "test111",
  "run": null
}
```

---

## 前端使用流程

```
进入页面
    │
    ├─ GET /sessions/:sessionId
    │      ├─ run != null → 订阅 POST /sessions/:sessionId/runs/:runId
    │      └─ run == null → 不订阅，显示空界面
    │
发送消息
    │
    └─ POST /sessions/:sessionId
           ├─ 收到 202 + runId
           └─ 订阅 POST /sessions/:sessionId/runs/:runId

页面切换/刷新
    │
    └─ GET /sessions/:sessionId（重新查询状态）
```

## Redis 存储结构

| Key | 类型 | 说明 |
|-----|------|------|
| `chat:sessions:{sessionId}:current_run` | String | 指向当前 runId |
| `chat:sessions:{sessionId}:runs:{runId}:meta` | Hash | run 元数据（sessionId, runId, message, status, createdAt） |
| `chat:sessions:{sessionId}:runs:{runId}:events` | Stream | chunk/token 事件流 |

TTL：2 小时。

---

## 接口列表（续）

### 5. Chat Completions（OpenAI 兼容）

**POST** `/api/chat/completions`

OpenAI 兼容的 chat completions 接口，无 session/run 管理，每个请求独立。

**请求体**

```json
{
  "model": "gpt-4",
  "messages": [
    {"role": "user", "content": "你好"}
  ],
  "stream": true
}
```

**响应（stream: true）**

SSE 流，格式与 OpenAI Chat Completions 完全一致：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1748937600,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1748937600,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1748937600,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

**响应（stream: false）**

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1748937600,
  "model": "gpt-4",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "你好！"},
      "finish_reason": "stop"
    }
  ]
}
```

**说明**

- 不需要 sessionId / runId
- 不保存任何状态，请求结束后即结束
- 支持 `stream: true` / `stream: false`
- `model` 字段可选，默认取环境变量 `MODEL_NAME`
- 内部复用 Eino Agent，支持 tool 调用

---

## AI SDK useChat 风格接口

### 概述

AI SDK 的 `useChat` / `useCompletion` 规范要求 SSE 流使用 **AI SDK Data Stream Protocol**，与 OpenAI 兼容格式不同。这套接口与上方 OpenAI 协议路由一一对应：

| 用途 | OpenAI 协议 | AI SDK 协议 |
|------|------------|------------|
| 创建 Run | `POST /sessions/:sessionId` | `POST /usechat/sessions/:sessionId` |
| 查询当前 Run | `GET /sessions/:sessionId` | `GET /usechat/sessions/:sessionId` |
| 订阅事件流 | `POST /sessions/:sessionId/runs/:runId` | `POST /usechat/sessions/:sessionId/runs/:runId` |
| 取消 Run | `POST /sessions/:sessionId/cancel/:runId` | `POST /usechat/sessions/:sessionId/cancel/:runId` |

**主要区别：**

- SSE 流格式为 AI SDK Data Stream Protocol（带 `x-vercel-ai-ui-message-stream: v1` header）
- 请求体支持 AI SDK 标准的 `messages` 数组（每条 message 可含 `parts[]`）或兼容字段 `message`
- `params.type` 可传 `"deep"` 切换 deep agent

---

### 1. 创建 AI Run

**POST** `/api/chat/usechat/sessions/:sessionId`

**请求体**

```json
{
  "messages": [
    {"role": "user", "content": "帮我解释 React hooks"}
  ],
  "message": "帮我解释 React hooks",
  "params": {
    "type": "chat"
  }
}
```

- `messages`：AI SDK 标准格式，`parts[]` 也支持
- `message`：兼容字段，与 `messages` 共存时优先取 `message`
- `params.type`：可选，`"deep"` 启用 deep agent

**响应 202**

```json
{
  "sessionId": "test111",
  "runId": "54ea1a309853ccd1da77bd68b1296c84",
  "status": "running"
}
```

---

### 2. 查询 AI Session 当前 Run

**GET** `/api/chat/usechat/sessions/:sessionId`

**响应 200**

有 active run：

```json
{
  "sessionId": "test111",
  "run": {
    "runId": "54ea1a309853ccd1da77bd68b1296c84",
    "message": "帮我解释 React hooks",
    "status": "running",
    "createdAt": 1748937600000
  }
}
```

无 active run：

```json
{
  "sessionId": "test111",
  "run": null
}
```

---

### 3. 订阅 AI Run 事件流

**POST** `/api/chat/usechat/sessions/:sessionId/runs/:runId`

**SSE 事件（AI SDK Data Stream Protocol）**

```
x-vercel-ai-ui-message-stream: v1
Content-Type: text/event-stream
```

```
data: {"type":"start","messageId":"msg_xxx"}

data: {"type":"start-step"}

data: {"type":"reasoning-start","id":"reasoning_xxx"}
data: {"type":"reasoning-delta","id":"reasoning_xxx","delta":"思考内容..."}
data: {"type":"reasoning-end","id":"reasoning_xxx"}

data: {"type":"text-start","id":"text_xxx"}
data: {"type":"text-delta","id":"text_xxx","delta":"这是"}
data: {"type":"text-delta","id":"text_xxx","delta":"回复内容"}
data: {"type":"text-end","id":"text_xxx"}

data: {"type":"tool-input-start","toolCallId":"tool_call_0","toolName":"get_weather"}
data: {"type":"tool-input-delta","toolCallId":"tool_call_0","inputTextDelta":"{"}
data: {"type":"tool-input-available","toolCallId":"tool_call_0","toolName":"get_weather","input":{"location":"北京"}}
data: {"type":"tool-output-available","toolCallId":"tool_call_0","output":{"weather":"晴天"}}

data: {"type":"data-usage","data":{"finishReason":"stop","usage":{"promptTokens":100,"completionTokens":50,"totalTokens":150}}}

data: {"type":"finish-step"}
data: {"type":"finish"}

data: [DONE]
```

**规则**

- `runId` 必须是该 session 的当前 active run
- run 结束后 current run 被清掉，同 `runId` 再订阅直接 `data: [DONE]`
- 无数据时定期发送 `: ping` 心跳

---

### 4. 取消 AI Run

**POST** `/api/chat/usechat/sessions/:sessionId/cancel/:runId`

**响应 202**（成功取消）

```json
{
  "sessionId": "test111",
  "run": {
    "runId": "54ea1a309853ccd1da77bd68b1296c84",
    "status": "canceled"
  }
}
```

**响应 200**（无 active run 或 runId 不匹配）

```json
{
  "sessionId": "test111",
  "run": null
}
```
