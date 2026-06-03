# AI SDK 协议接口

AI SDK 的 `useChat` / `useCompletion` 规范使用 **AI SDK Data Stream Protocol**，与 OpenAI 兼容格式不同。

Session 不在后端保存，前端自己维护。

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

### 1. 创建 AI Run（会话模式）

**POST** `/api/chat/usechat/sessions/:sessionId`

启动一个 Eino Agent Run，后台运行。

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

- `messages`：AI SDK 标准格式，每条 message 可含 `parts[]`
- `message`：兼容字段，与 `messages` 共存时优先取 `message`
- `params.type`：可选，`"deep"` 启用 deep agent，默认 `"chat"`

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

查询该 session 当前是否有一个 active AI run。run 结束后 current run 指针自动清除，返回 `run: null`。

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

SSE 订阅该 run 的 AI SDK Data Stream Protocol 事件流。

**Headers**

```
x-vercel-ai-ui-message-stream: v1
Content-Type: text/event-stream
```

**SSE 事件**

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

**事件类型说明**

| type | 说明 |
|------|------|
| `start` | 消息开始 |
| `start-step` | 步骤开始 |
| `reasoning-start/delta/end` | 推理过程 |
| `text-start/delta/end` | 最终文本输出 |
| `tool-input-start/delta/available` | 工具调用参数 |
| `tool-output-available` | 工具返回结果 |
| `data-usage` | token 用量 |
| `finish-step` | 步骤结束 |
| `finish` | 整个消息结束 |

**规则**

- `runId` 必须是该 session 的当前 active run
- run 结束后 current run 被清掉，同 `runId` 再订阅直接 `data: [DONE]`
- 无数据时定期发送 `: ping` 心跳

---

### 4. 取消 AI Run

**POST** `/api/chat/usechat/sessions/:sessionId/cancel/:runId`

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

### 5. AI SDK Completions（独立接口）

**POST** `/api/chat/usechat/completions`

AI SDK 标准的 chat completions 接口，无 session/run 管理，每个请求独立。

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

**响应**

SSE 流，AI SDK Data Stream Protocol 格式：

```
x-vercel-ai-ui-message-stream: v1
Content-Type: text/event-stream
```

```
data: {"type":"start","messageId":"msg_xxx"}
data: {"type":"start-step"}
data: {"type":"reasoning-start","id":"reasoning_xxx"}
data: {"type":"reasoning-delta","id":"reasoning_xxx","delta":"思考中..."}
data: {"type":"reasoning-end","id":"reasoning_xxx"}
data: {"type":"text-start","id":"text_xxx"}
data: {"type":"text-delta","id":"text_xxx","delta":"React hooks 是..."}
data: {"type":"text-end","id":"text_xxx"}
data: {"type":"finish-step"}
data: {"type":"finish"}
data: [DONE]
```

**说明**

- 不需要 sessionId / runId
- 不保存任何状态，请求结束后即结束
- 支持 `params.type: "deep"` 启用 deep agent

---

---

## 前端使用流程

```
进入页面
    │
    ├─ GET /usechat/sessions/:sessionId
    │      ├─ run != null → 订阅 POST /usechat/sessions/:sessionId/runs/:runId
    │      └─ run == null → 不订阅
    │
发送消息
    │
    └─ POST /usechat/sessions/:sessionId
           ├─ 收到 202 + runId
           └─ 订阅 POST /usechat/sessions/:sessionId/runs/:runId
```

## Redis 存储结构

| Key | 类型 | 说明 |
|-----|------|------|
| `chat:sessions:{sessionId}:current_run` | String | 指向当前 runId |
| `chat:sessions:{sessionId}:runs:{runId}:meta` | Hash | run 元数据 |
| `chat:sessions:{sessionId}:runs:{runId}:events` | Stream | chunk 事件流 |

TTL：2 小时。

> 注意：AI SDK 路由复用与 OpenAI 协议相同的 Redis 结构，sessionId 完全独立，互不影响。
