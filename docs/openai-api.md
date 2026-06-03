# OpenAI 兼容协议接口

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
  "agent": "chat"
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

查询该 session 当前是否有一个 active run。run 结束后 current run 指针自动清除，返回 `run: null`。

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

无 active run：
```json
{
  "sessionId": "test111",
  "run": null
}
```

---

### 3. 订阅 Run 事件流

**POST** `/api/chat/sessions/:sessionId/runs/:runId`

SSE 订阅该 run 的 chunk 事件流，OpenAI Chat Completions 兼容格式。

**Query 参数**

| 参数 | 说明 |
|------|------|
| `after` | 从指定 eventId 之后开始读 |
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

- `runId` 必须是该 session 的当前 active run
- run 结束后 current run 被清掉，同 `runId` 再订阅直接 `data: [DONE]`
- 无数据时定期发送 `: ping` 心跳

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

### 5. Chat Completions（独立接口）

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

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":1748937600,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}

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
- 不保存任何状态
- `model` 可选，默认读环境变量 `MODEL_NAME`

---

## 前端使用流程

```
进入页面
    │
    ├─ GET /sessions/:sessionId
    │      ├─ run != null → 订阅 POST /sessions/:sessionId/runs/:runId
    │      └─ run == null → 不订阅
    │
发送消息
    │
    └─ POST /sessions/:sessionId
           ├─ 收到 202 + runId
           └─ 订阅 POST /sessions/:sessionId/runs/:runId
```

## Redis 存储结构

| Key | 类型 | 说明 |
|-----|------|------|
| `chat:sessions:{sessionId}:current_run` | String | 指向当前 runId |
| `chat:sessions:{sessionId}:runs:{runId}:meta` | Hash | run 元数据 |
| `chat:sessions:{sessionId}:runs:{runId}:events` | Stream | chunk 事件流 |

TTL：2 小时。
