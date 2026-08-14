# 03 协议与 API 兼容设计

本文定义 Goren 与 TypeScript DeepSeek Harness 的兼容边界。运行时架构与插件生命周期由 [02 运行时架构与插件模型](02-runtime-architecture-and-plugin-model.md) 负责；依赖选择由 [04 Go 技术架构决策与选型](04-go-technology-decisions.md) 负责。

## 1. 兼容目标

兼容性分为四层，不能用“能够完成类似任务”替代协议一致：

| 层级 | 要求 | 典型证据 |
| --- | --- | --- |
| P0 Wire | JSON 字段、判别值、HTTP 路径、错误信封与流事件一致 | TypeScript/Go 双向 contract fixture |
| P1 Semantic | 顺序、取消、重试、作用域、失败传播和资源释放语义一致 | 生命周期测试、故障注入与回放 |
| P2 API | 公共概念、职责和扩展入口一致；允许用 Go 惯用表达替代 TypeScript 语法 | 公共 Go API 审查与映射表 |
| P3 Model-visible | 模型实际看见的消息、工具结果、压缩结果和 system prompt 一致 | session replay 与 snapshot diff |

浏览器客户端实现、Web UI 和 DSH SDK 不复制，但 TypeScript Client 使用的 Connection Host wire contract 是首要兼容面，详见 [01 复制范围与基线](01-porting-scope-and-baseline.md)。

## 2. TypeScript 到 Go 的表达映射

| TypeScript 表达 | Go 表达 | 兼容要求 |
| --- | --- | --- |
| decorator、declaration merging | 显式 descriptor、命名 key、注册函数 | 保留服务名、方法名和 wire schema，不复制编译器技巧 |
| `Promise<T>` | 普通返回值和 `error` | 调用完成与错误语义一致 |
| `AsyncIterable<T>` | 只读 channel 或带 `Next` 的迭代接口 | 顺序、背压、结束和取消可测试 |
| `AbortSignal` | `context.Context` | 超时、主动取消和上游断开必须传播 |
| branded string | 命名字符串类型加构造校验 | 不允许绕过格式约束 |
| discriminated union | tag 字段、具体结构与自定义 JSON 编解码 | 判别字段和值保持不变，未知值显式失败 |
| optional field | 指针、presence wrapper 或自定义解码 | 区分缺失、`null` 和零值 |
| Zod/Schemastery schema | Go 类型加 JSON Schema 校验 | 先校验 wire 数据，再进入领域逻辑 |

公共 wire 名称、事件 `type`、错误 `code`、provider 名和工具名不得为了 Go 命名习惯而改变。Go 内部标识符可以符合 Go 习惯，但映射必须集中在协议边界。

## 3. Session 与事件协议

Session 是追加写事件日志，不是可被原地修改的会话对象。持久化信封保持：

```json
{
  "type": "user/message",
  "seq": 12,
  "time": 1786743546510,
  "data": {},
  "ignorable": true
}
```

约束如下：

- Session Header 的 `version` 初始保持 TypeScript 基线的 `0`；升级只能通过显式迁移。
- `seq` 在单个 session 内严格递增；Event 进入内存日志后才发布 `session/event`，durability 则由 `session/flush` 单独确认。
- 未知且未标记 `ignorable` 的事件必须拒绝读取；未知且可忽略的事件保留原始内容并跳过解释。
- surface event 保留 `sourceEventSeqs` 与 `surfaceOp`，不得丢失到原始事件的可追溯关系。
- 所有模型可见消息必须可由日志重建；模型可见但未落日志、或已落日志但请求未包含，均视为兼容缺陷。
- Header `createdAt` 与 Event `time` 都保持 Unix epoch milliseconds number，并限制在 JavaScript safe integer 范围。

Header、Event、surface、append commit 与 flush 的唯一详细设计见[10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)。

核心 turn 顺序保持为：

```text
turn/start
  -> claim input
  -> step/start
  -> user/message
  -> request/header
  -> LLM stream
  -> assistant chunks/message
  -> tool calls/results（可能触发下一 step）
  -> step/end
turn/end
```

插件扩展点可以观察或转换受支持的阶段，但不能绕过 session 记录直接向模型注入隐藏上下文。

## 4. LLM 协议

### 4.1 Message 与 content block

Go 公共契约保留 TypeScript 的消息角色、`source` 判别和 content block 种类：

- `text`
- `reasoning`
- `image`
- `tool-call`
- `tool-result`

`source` 继续区分 `user`、`plugin`、`model` 和 `tool`。不得把来源压平成一个自由文本字段，否则 session 回放、审计和插件转换会失去依据。

### 4.2 流事件

流事件保持以下判别值及其相对顺序：

- `block-start`
- `text-delta`
- `reasoning-delta`
- `tool-call-delta`
- `block-end`
- `usage`
- `finish`

完成原因保持 `stop`、`tool-calls`、`max-tokens`、`aborted` 和 `error`。适配器负责将供应商事件归一化为这些类型；Agent Loop 不直接依赖供应商 SDK。

当前 `llm` 包早于 Harness 复制方向。迁移时应把既有 provider 实现接到新的公共契约，逐个迁移调用方，然后删除旧入口；禁止再建立一个平行的 `harness/llm` 包或长期兼容包装层。

## 5. Tool 协议

Tool 定义至少保留：

- 稳定名称和描述；
- 输入 schema 与输出 schema；
- `execute` 与可选 `finalize` 生命周期；
- timeout 与 concurrency 约束；
- presentation metadata；
- 执行前 `allow`、`deny`、`ask` 决策；
- 执行后 `accept`、`block` 决策；
- 成功、失败、拒绝、取消等可判别结果。

输入必须先按 schema 校验。`ask` 通过 Interaction 能力请求许可；未来若实现 Headless，也不能把无人值守模式自动解释为允许。工具运行产生的过程输出与最终结果应能关联到调用 ID 和 session event。

## 6. TypeScript Client Connection

### 6.1 它才是客户端协议

源职责链为：

```text
TypeScript Client Connection
  -> HTTP POST unary + WebSocket downlink
  -> Host Connection carrier
  -> API Proxy handler
  -> Agent / Session / Interaction services
```

Go 不复制左侧的 TypeScript Client 实现，但复制 Host carrier 和 API Proxy contract。兼容目标是现有 Client 可以使用原 path、信封、method、payload/result、frame 和重连流程连接 Go Server。

### 6.2 四类 RPC message

Client 发起一元请求：

```json
{
  "type": "client-request",
  "rpcId": "client-generated-id",
  "method": "session.prompt",
  "payload": {}
}
```

Server 回应相同 `rpcId`：

```json
{
  "type": "server-response",
  "rpcId": "client-generated-id",
  "result": {
    "ok": true,
    "value": {}
  }
}
```

业务失败仍是成功送达的 `ServerResponse`：

```json
{
  "type": "server-response",
  "rpcId": "client-generated-id",
  "result": {
    "ok": false,
    "error": {
      "code": "session-not-found",
      "message": "session not found",
      "details": {
        "sessionId": "missing"
      }
    }
  }
}
```

Server 主动推送或请求 interaction：

```json
{
  "type": "server-request",
  "rpcId": "server-generated-or-stable-interaction-id",
  "method": "approval/requested",
  "payload": {
    "type": "approval/requested"
  }
}
```

需要回答的 `ServerRequest` 由 Client 回传：

```json
{
  "type": "client-response",
  "rpcId": "same-server-request-id",
  "result": {
    "ok": true,
    "value": {}
  }
}
```

四个 `type` 判别值、`rpcId` mint/echo 规则、`RpcResult` 分支和各错误的 `details` shape 必须保持。Go 不增加统一 `data` wrapper，也不把业务错误改成 HTTP 4xx。

### 6.3 HTTP unary 与 respond

一元调用使用：

```text
POST /api/<method>
Content-Type: application/json
```

例如 `POST /api/session.prompt`。约束如下：

- path 中的 `<method>` 必须与 `ClientRequest.method` 完全相同；
- 只接受 `application/json`，否则返回 HTTP `415`；
- body 不是 JSON 时返回 HTTP `400`；
- JSON 合法但信封或 payload 不合法时，返回 HTTP `200` 的 `ServerResponse`，错误码为 `bad-request`；
- 信封中有可读字符串 `rpcId` 时错误响应回显它，否则使用固定 `invalid-request`；
- 未注册 path 返回 HTTP `404`；
- handler 自身崩溃返回 HTTP `500`；
- 业务成功或业务拒绝均返回 HTTP `200`，由 `RpcResult` 表达；
- request 断开取消传入 handler 的 `context.Context`；
- 默认 body 上限保持源基线的 160 MiB，并与 attachment aggregate limit 联动校验。

回答 Server interaction 使用：

```text
POST /api/respond
```

body 是 `ClientResponse`，返回不属于四类 message 的 `RpcReceipt`：

```json
{"accepted": true}
```

或：

```json
{"accepted": false, "reason": "not-pending"}
```

`reason` 只允许 `not-pending` 或 `bad-response`。

### 6.4 两条 WebSocket downlink

Client 同时连接：

```text
/api/events.mux
/api/events.host
```

两条 WebSocket 都是 Server 到 Client 的单向 text-frame transport：

- `MuxFrame`/`HostFrame` 是应用层事件流的一条 payload，不是 WebSocket 协议的底层分片；
- 每个应用层 frame 包入一个完整 `ServerRequest` JSON，并占一个 WebSocket text message；
- `method` 等于 `payload.type`；
- `events.mux` 传 Session Event、subscription baseline、approval/question、queue、jobs 和 projection；
- `events.host` 传 Session membership/status、Agent error、Workspace change 和纳入的 Host frame；
- Client 在 socket 上发送业务消息属于协议错误，Server 以 policy violation 关闭；
- stream 实现失败时尽力发送一个 `stream/error` frame 后关闭；
- 任一 socket 断开使当前 generation 失效，两条流一起重建。

`MuxFrame` 是所有 Session 共用一条 socket 的 multiplexed union：

| `payload.type` | 作用 |
| --- | --- |
| `session/event` | 传递一条持久化 Session Event 及可选 Tool view |
| `session/subscribed` | 建立某 Session 的 `lastSeq` baseline |
| `approval/requested` / `approval/resolved` | approval interaction 的请求与权威结果 |
| `question/requested` / `question/resolved` | question interaction 的请求与权威结果 |
| `session/queue` | 完整瞬态 inbox snapshot |
| `session/jobs` | 完整可见 background job snapshot |
| `session/projection` | 一个 projection unit 的新值与 watermark |
| `stream/error` | mux stream 的终止技术错误 |

`HostFrame` 描述 Harness 进程整体的成员关系和主机级变化：

| `payload.type` | 作用 |
| --- | --- |
| `host/session-added` / `host/session-removed` | Session membership 变化 |
| `host/session-status` | Session running 状态 |
| `host/agent-error` | 没有 turn position 的 live Agent failure |
| `host/workspace-changed` / `host/workspace-removed` | Workspace snapshot upsert/remove |
| `host/workspace-order-changed` | 完整 Workspace 顺序 |
| `host/archived-sessions-changed` | 完整归档 Session 集合 |
| `host/remote-event` | allowlisted Host Cordis event |
| `stream/error` | host stream 的终止技术错误 |

两条应用流没有跨流排序保证；`stream/error` 是唯一同时属于两个 union 的分支。

连接 readiness 保持源顺序：两条 downlink 已打开，并且 `host.describe` 成功后，Client 才发布 `onConnected`。重连依赖 stream baseline 与随后重新读取 list/history，不假设 Server replay 所有历史 frame。

源 `toFetchHandler` 还支持进程内 SSE 测试载体，但浏览器网络 carrier 没有 SSE fallback；Go 的网络兼容实现只需 WebSocket，contract tests 可以提供进程内测试 adapter。

### 6.5 Trust、取消与生命周期

- 每个 `/api` HTTP 请求和 WebSocket upgrade 都先执行 Host authority fence；
- Host 必须是 loopback 或显式 `trustedHosts` 中的规范 authority；
- 存在 `Origin` 时必须与 Host authority 一致；
- `sec-fetch-site: cross-site` 明确拒绝；
- privileged method 即使部署配置了 trusted host，也可以保持 loopback-only；
- 该 fence 防 DNS rebinding，不是认证；没有认证层时不能把任意公网暴露描述成安全部署；
- Server shutdown 先停止新请求，取消 connection-owned request/stream，关闭 socket，等待 frame pump，再卸载核心 Plugin。

### 6.6 API Surface

源 `RpcMethodMap` 和各 domain interface 是方法、payload、result 与错误的权威。第一兼容切片优先：

- `host.describe`
- 全部 `session.*`
- `events.mux`
- `events.host`
- `/api/respond`
- approval/question 的 request/response frame

其他方法进入 capability matrix 后才能声明兼容。未纳入的方法返回源 carrier 的未注册行为，不能用固定成功、空对象或无副作用 stub 冒充实现。

这意味着“客户端可以连接并完成 Agent 对话”与“整个原 Web 产品所有页面可用”是两项不同声明；首期只承诺前者。

### 6.7 Typert 的真实位置

Typert 不是 Client Connection 协议。固定源基线中，Connection 在共享 `/api` channel 上先让 Typert interceptor 认领已迁移的 Remote endpoint，剩余方法交给 API Proxy。

当前 Typert Remote 主要覆盖 commands、goals、动态 Cordis、plugin inventory 和 message feedback。核心 `session.prompt`、history 与 event streams 不依赖 Typert。

因此首期不实现 Typert。某个上述 Remote endpoint 被明确纳入后，再为它实现：

- endpoint descriptor；
- exact `{args}` payload；
- lookup/context binding；
- Host dispatch 和错误映射。

仍不复制 TypeScript generator、decorator、declaration merging、ClientRemote、`$mount/$on/$dispatch` 或生成的 Client artifacts。

### 6.8 明确排除的 Client 实现

- browser `ConnectionController`；
- TypeScript `AbstractApiClient`/Web API Client；
- React Client Runtime 与状态管理；
- Client Remote namespace service；
- 客户端 WebSocket 重连代码本身；
- browser bundle、frontend static 和 UI。

这些代码不进入 Go；它们的 source tests 和 wire schema 反过来作为 Go Server 的 compatibility oracle。

## 7. Deferred：ACP 与 MCP

ACP 不是当前客户端协议兼容的前置能力。未来若进入范围，其独立 adapter 需要：

- stdio transport；
- `initialize`、认证、创建 session、prompt、cancel；
- session update；
- permission request；
- ACP 生命周期与 Agent turn/session 的映射必须显式。

MCP 同样是 Deferred 的出站工具发现与调用能力：

- 初次纳入时只接入 MCP tools；
- 工具命名保持 `mcp__<serverName>__<rawName>`；
- reconnect 使用 generation swap，旧 generation 不得在替换后接收新调用；
- server 进程、连接和工具注册都必须随插件 effect 释放。

ACP/MCP 的第三方 Go SDK 只承担编码与 transport，不拥有 Goren 的 Agent、Session 或 Tool 领域模型。转换位于各自 adapter 包。

## 8. 明确排除 DSH SDK

TypeScript DSH SDK 的 newline JSON-RPC、Python SDK、`initialize`、`session/prompt`、`shutdown` 和通知协议不复制，也不作为公共兼容入口。Subagent 不得通过重新实现 DSH SDK 绕开 Goren 的内部服务边界。

若某个隔离子进程确实需要 RPC，它应定义为 Goren 私有协议，使用独立命名和版本，不得声称与 DSH SDK 兼容。该选择需先补充 ADR 和安全边界。

## 9. JSON 编解码规则

- 公共输入默认拒绝未知字段；只有 TypeScript 明确允许扩展对象时例外。
- 输出不得依赖 Go map 的迭代顺序；golden fixture 比较语义 JSON，签名或 snapshot 场景使用规范化编码。
- 64 位整数越过 JavaScript safe integer 范围时按源协议使用字符串或受约束类型，不能直接暴露为 JSON number。
- `[]` 与 `null`、缺失与零值按源协议分别处理。
- 错误对外只暴露稳定 code、可安全展示的 message 和允许的 details；Go error chain 留在服务端日志。
- 所有解码均设置请求体大小上限，流式读取也必须受预算和 context 控制。

## 10. 兼容证据

契约资产与固定源基线绑定，但不把 TypeScript schema 或客户端实现带入 Go 运行时：

```text
contracts/deepseek-harness/
  manifest.json
  vectors.json

tests/contract/
  fixtures_test.go
  source_client_test.go
  source_http_test.go
  typescript/
    generate-vectors.ts
    client-smoke.ts
    connection-reconnect.ts
    http-reference.ts
```

`manifest.json` 是机器可读的范围与 provenance owner：记录源 commit/version/license/toolchain、HTTP/WS path、message/receipt/frame 判别集合、当前 unary method、privileged method、Excluded 和 Deferred 能力。源 commit 改变时必须显式更新该文件和全部向量，不能自动追随相邻 checkout。

`generate-vectors.ts` 把固定上游 Zod schema 当作 oracle，对预先定义的 JSON 样本执行 `safeParse`，把输入、`accepted` 判定和 schema 规范化后的 JSON 写成确定性 `vectors.json`。这里的“向量”是 contract test case，不是 embedding，也不生成 Go 类型或生产代码。fixture 不手工“修正”为 Go 输出。

普通 `go test ./...` 只读取已提交向量，验证 Go envelope decoder、`host.describe` 和 Mux/Host encoder，不需要 Node 或源 checkout。显式的 `go test -tags=contract ./tests/contract` 才使用 `DSH_SOURCE`（默认 `../deepseek-harness`）运行以下跨语言链：

```text
固定 TypeScript schema
  -> generate-vectors.ts
  -> 与已提交 vectors.json 逐字比较

固定 WebApiClient
  -> HTTP host.describe + 两条真实 WebSocket + /api/respond
  -> Go Connection Host -> API Proxy

固定 ConnectionController
  -> 建立 mux + host + describe readiness
  -> 任一 socket 结束
  -> client-owned generation 作废并重建两条 socket

同一批 raw HTTP cases
  -> 固定 toFetchHandler 与真实 Go HTTPHost
  -> 比较 status、media type、稳定 text/JSON 和 failure precedence
```

测试只导入上游 schema、`WebApiClient` 和 `ConnectionController` 作为 compatibility oracle；它们不编译进 Go binary，也不改变“客户端实现不复制”的范围。测试依赖缺失时显式失败并给出源依赖安装提示，不在普通 Go test 中静默安装依赖或修改源 checkout。

验收包含：

1. TypeScript schema 生成接受/拒绝/规范化向量，Go 对已纳入的 Connection message 和 API payload 作相同判断；
2. Go 编码 Mux/Host frame 与响应，固定 TypeScript schema 和 `WebApiClient` 直接解析；
3. 同一输入分别运行两端，比较事件顺序、稳定字段和最终 model-visible context；
4. 取消、超时、插件卸载、无效参数和未知事件的负向用例；
5. provider 特有事件经过 adapter 后的统一流 snapshot。

只有完成对应层级的双向测试，能力矩阵才可以标记为 P0/P1/P2/P3 已兼容。编译成功或存在同名类型都不构成协议兼容证据。
