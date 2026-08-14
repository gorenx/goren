# 07 API Proxy 模块设计与实现

状态：Accepted

本文拥有 API Proxy 的 method 注册、typed dispatch、Provider 边界和上下游交互。线协议由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有，Connection carrier 由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)拥有，实现状态与验证证据见[08 实施进度](./08-implementation-progress.md)。

## 1. 源职责与模块范围

主要源证据：

- `packages/host/apiproxy/src/api/rpc-map.ts`：canonical method 到 typed signature 的映射；
- `packages/host/apiproxy/src/fetch/handler.ts`：第二层 payload parse 与 method invoke；
- `packages/host/apiproxy/src/api/host.ts`、`host.schema.ts`：`host.describe` contract；
- `packages/host/apiproxy/src/api-proxy.ts`：Host snapshot 与 interaction pending owner。

## 2. 职责边界

`apiproxy` 拥有：

- canonical method 的唯一注册点和重复 owner 拒绝；
- `Request[P]`、`Outcome[V]`、`PayloadDecoder[P]`、`UnaryHandler[P,V]`；
- method payload 的第二层 typed decode；
- 业务 success/failure 与技术 error/panic 的分离；
- Host description contract 与 consumer-owned `HostDescriptionProvider`；
- `/api/respond` 未来 pending interaction table 的业务入口。

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

## 5. `host.describe` 纵向切片设计

`RegisterHostDescribe` 注册 canonical `host.describe`，payload 是 `HostDescribeRequest`，返回 `HostDescription`。`HostDescriptionProvider` 是 API Proxy 消费方拥有的最小接口，composition root 提供实现。

Provider 必须从已装配的 live Service 组装：

- CLI 注入的 version；
- 进程当前工作目录；
- 当前 attached Session 数；
- native path open capability；
- 未配置默认 LLM 时省略 `provider` 与 `model`。

API Proxy 不缓存或推导这些状态，也不使用固定成功结果冒充未装配能力。

## 6. 结果与错误所有权

- `Outcome[V]` 表达业务 success/failure；业务 failure 被编码为 HTTP `200` 的 `RpcResult`；
- Go `error` 只表示 Provider/依赖技术失败，由 Connection 映射为 HTTP `500`；
- Provider panic 在 Catalog invoke 边界转为技术 error，避免越过模块边界崩溃；
- payload decode failure 由 API Proxy 构造 `bad-request`，且 Provider 不被调用；
- response `rpcId` 由 Connection 使用原 request ID 补齐，Provider 无权另行 mint 或改写。

最后一条在 Go 中把源 `RpcResponse.rpcId` 的回显不变量收紧为结构保证，不改变 wire 观察结果。

## 7. `/api/respond` 目标设计

approval/question owner 在 API Proxy 中创建 pending entry：ServerRequest 创建稳定 `rpcId`，`Respond` 按 ID 路由并做 interaction payload 的第二层 decode，成功后原子 settle；late/duplicate 返回 `not-pending`。Connection 只负责 transport receipt，不拥有 pending 业务状态。

## 8. 后续进入规则

每增加一个 API 模块，必须同时提供：

1. 固定源 method/schema/Provider owner；
2. owner-defined request、response 与 error details 类型；
3. 一条 typed Catalog 注册；
4. success、payload rejection、business failure、technical failure 与 cancellation test；
5. 对应 TypeScript-to-Go fixture 或 differential test；
6. 本模块文档的上下游、生命周期或 pending 规则变化。

不得为尚未进入范围的 method 预注册空 handler、固定成功结果或通用 `map[string]any` route。
