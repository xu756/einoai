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
