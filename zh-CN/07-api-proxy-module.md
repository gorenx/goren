# 07 API Proxy 模块设计与实现

状态：Accepted

本文拥有 API Proxy 的 method 注册、typed dispatch、Provider 边界和上下游交互。线协议由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有，Connection carrier 由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)拥有，实现状态与验证证据见[08 实施进度](./08-implementation-progress.md)。

## 1. 源职责与模块范围

主要源证据：

- `packages/host/apiproxy/src/api/rpc-map.ts`：canonical method 到 typed signature 的映射；
- `packages/host/apiproxy/src/api/events.ts`：`EventsApi`、Mux/Host frame 与窄 `RpcRequest<Frame>`；
- `packages/host/apiproxy/src/fetch/handler.ts`：第二层 payload parse 与 method invoke；
- `packages/host/apiproxy/src/api/host.ts`、`host.schema.ts`：`host.describe` contract；
- `packages/host/apiproxy/src/api-proxy.ts`：Host snapshot 与 interaction pending owner。

## 2. 职责边界

`apiproxy` 拥有：

- canonical method 的唯一注册点和重复 owner 拒绝；
- transport-neutral `EventStreams` 与 mux/host 独立 stream handler；
- `Request[P]`、`Outcome[V]`、`PayloadDecoder[P]`、`UnaryHandler[P,V]`；
- method payload 的第二层 typed decode；
- answerable `ServerRequest` 的 pending correlation、response 第二层 typed decode 与原子 settle；
- 业务 success/failure 与技术 error/panic 的分离；
- Host description contract 与 consumer-owned `HostDescriptionProvider`；
- `/api/respond` 的业务入口。

API Proxy 不拥有：

- HTTP、Echo、Host/Origin trust、body budget 或 WebSocket；
- Session、Agent、LLM、Tool 的领域状态与不变量；
- Provider 的文件、数据库或网络实现；
- Plugin Runtime lifecycle；
- TypeScript Client 或 Typert dispatch。

## 3. typed method 注册方案

Go 不复制 TypeScript 的 `RpcMethodMap` 类型运算或 Typert code generation。每个进入范围的 method 通过一个泛型注册调用同时绑定：

```text
canonical method
  + PayloadDecoder[Request]
  + UnaryHandler[Request, Response]
  -> Catalog route
```

泛型在注册时保证 decoder、handler request 与 response 类型一致；Catalog 内部只在异构 route map 中擦除类型。`json.RawMessage` 不越过 decoder，Provider 收到的是 owner-defined Go request type。

Catalog 只允许一个 owner 注册同一 canonical method。Catalog 支持并发读取；注册发生在 listener 启动前，后续 Plugin Runtime 若支持 replacement，必须通过 Scope/effect 建立新的原子切换机制，不能直接在 live map 上覆盖。

同一 Catalog 还持有运行期 pending correlation table，但不把不同 interaction 擦成通用业务模型。interaction owner 使用 `RegisterPendingResponse[V]` 为稳定 `rpcId` 注册自己的 `ResponseDecoder[V]`，并通过 `PendingResponse[V]` 等待或撤回；异构只存在于表的内部路由点。

## 4. 上下游交互

```text
internal/connection
  -> RPCDispatcher.DispatchUnary
  -> apiproxy.Catalog route lookup
  -> method-owned PayloadDecoder
  -> typed UnaryHandler
  -> consumer-owned Provider interface
  -> typed Outcome
  -> connection.RpcResult
  -> internal/connection ServerResponse
```

API Proxy 接收的是 Connection 已验证的 `rpcId`、method 和 raw payload。它不重复检查 content type、JSON 语法、RPC message type 或 path/method；只进行当前 method 的 payload decode。

事件方向保持独立调用链：

```text
Session/Host owner
  -> API Proxy EventStreamHandler
  -> connection.RPCRequest（rpcId + frame payload）
  -> internal/connection WebSocket carrier
  -> connection.ServerRequest text message
```

API Proxy 拥有 frame 的业务来源、baseline/replay 和稳定 interaction `rpcId`；Connection 只从 `payload.type` 补全 wire `method` 并发送，不检查 Session/Host 业务字段。当前 API Proxy Plugin 在尚无 Session/Host owner 时提供可取消的空事件源，表示真实的零事件状态，不生成虚假 frame。Plugin 通过 `apiProxy` Service 同时提供 `RPCDispatcher` 与 `EventSource` 两个 Connection 所需 facet；Connection 只消费 interface，不依赖具体 Catalog/EventStreams 类型。

## 5. Mux/Host 应用层 frame union

`frame` 表示事件流中的一条完整应用层记录，不是 WebSocket 底层分片。`MuxFrame` 把多个 Session 的事件汇聚到 `/api/events.mux`；`HostFrame` 把 Harness 进程级成员关系、状态和 Workspace 变化发送到 `/api/events.host`。精确分支及 wire 语义由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md#64-两条-websocket-downlink)拥有。

Go 使用带私有 marker 的封闭 interface 和 exported concrete frame type 表达两个判别联合。具体 owner 只能构造已纳入 union 的分支，不能向 `EventStreams` 发送任意 `map[string]any` 或 raw payload。`StreamRequest[F]` 保留源 `RpcRequest<Frame>` 的窄形态：显式携带 `rpcId` 与 typed payload，不提前加入 wire `type`/`method`。

```text
Mux/Host owner
  -> StreamRequest[MuxFrame 或 HostFrame]
  -> API Proxy frame validation + canonical type encoding
  -> connection.RPCRequest
  -> Connection completes ServerRequest
```

API Proxy 持有浏览器所需的 consumer-owned projection，而不是复制领域聚合：

- `SessionEvent` 只固定 `type/seq/time/data` 等 wire envelope，`data` 仍由 Session event owner 定义；
- Tool view 只固定 `for` 与 `view.card`，card 内部由 Tool presenter 定义；
- queue message 只固定 identity、role、content/source 外壳，merge-extensible block/source 保持宽 JSON；
- projection value 与 remote event args 按源 schema 保持 owner-defined JSON；
- `WorkspaceView`、`JobView` 和 branded ID 是 API Proxy 面向浏览器的最小 DTO，不替代对应 owner 的内部模型。

每条 typed frame 只在 API Proxy 出口验证一次；Connection 只读取已编码 payload 的 `type` 以补全 `method`，不重复验证业务字段。`stream/error` 同时实现两个 union，是唯一共享分支。

## 6. `host.describe` 纵向切片设计

`RegisterHostDescribe` 注册 canonical `host.describe`，payload 是 `HostDescribeRequest`，返回 `HostDescription`。`HostDescriptionProvider` 是 API Proxy 消费方拥有的最小接口，composition root 提供实现。

Provider 必须从已装配的 live Service 组装：

- CLI 注入的 version；
- 进程当前工作目录；
- 当前 attached Session 数；
- native path open capability；
- 未配置默认 LLM 时省略 `provider` 与 `model`。

API Proxy 不缓存或推导这些状态，也不使用固定成功结果冒充未装配能力。

## 7. 结果与错误所有权

- `Outcome[V]` 表达业务 success/failure；业务 failure 被编码为 HTTP `200` 的 `RpcResult`；
- Go `error` 只表示 Provider/依赖技术失败，由 Connection 映射为 HTTP `500`；
- Provider panic 在 Catalog invoke 边界转为技术 error，避免越过模块边界崩溃；
- payload decode failure 由 API Proxy 构造 `bad-request`，且 Provider 不被调用；
- response `rpcId` 由 Connection 使用原 request ID 补齐，Provider 无权另行 mint 或改写。

最后一条在 Go 中把源 `RpcResponse.rpcId` 的回显不变量收紧为结构保证，不改变 wire 观察结果。

## 8. `/api/respond` pending 生命周期

```text
interaction owner
  -> mint stable rpcId
  -> RegisterPendingResponse(rpcId, owner decoder)
  -> publish answerable ServerRequest

POST /api/respond
  -> Connection parses ClientResponse envelope
  -> Catalog lookup by echoed rpcId
  -> owner decoder parses RpcResult
  -> atomically claim entry
  -> wake PendingResponse waiter
  -> RpcReceipt
```

状态规则：

- 未知、已撤回、late 或 duplicate `rpcId` 返回 `not-pending`；
- interaction payload 无效返回 `bad-response`，entry 保持 pending，允许客户端修正后重试；
- decoder panic 是技术错误，entry 保持 pending，由 Connection 返回 HTTP `500`；
- 合法 response 只有一个并发请求能原子 claim，winner 返回 `accepted`，其余返回 `not-pending`；
- owner context 取消时，`Wait` 撤回 entry 并返回取消原因；若合法 response 已先完成 claim，则 settlement 是权威结果；
- carrier/WebSocket 断开本身不撤回 pending，保证 reconnect 后仍可使用同一个 `rpcId` 回答；
- owner teardown 可显式 `Withdraw`，其后 late response 返回 `not-pending`。

通用 registry 不拥有 approval/question schema、requested/resolved frame、broadcast 或 reconnect replay。具体 interaction owner 仍持有其领域 pending 状态，从该状态生成首次 frame、replay baseline 和 resolved frame；registry 只保证 response 路由与结算并发语义。

## 9. 后续进入规则

每增加一个 API 模块，必须同时提供：

1. 固定源 method/schema/Provider owner；
2. owner-defined request、response 与 error details 类型；
3. 一条 typed Catalog 注册；
4. success、payload rejection、business failure、technical failure 与 cancellation test；
5. 对应 TypeScript-to-Go fixture 或 differential test；
6. 本模块文档的上下游、生命周期或 pending 规则变化。

不得为尚未进入范围的 method 预注册空 handler、固定成功结果或通用 `map[string]any` route。
