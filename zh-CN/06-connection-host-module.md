# 06 Connection Host 模块设计与实现

状态：Accepted

本文拥有 Connection Host 模块的职责、Go 架构、上下游交互和生命周期。RPC 字段、HTTP status、应用层 Mux/Host frame 与 WebSocket transport 的权威契约仍在[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)，本文不重复定义线协议；实现状态与验证证据见[08 实施进度](./08-implementation-progress.md)。

## 1. 源职责与模块范围

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner |
| --- | --- |
| `packages/client/connection/src/rpc-host.ts` | `connection`、`internal/connection` |
| `packages/client/connection/src/api-request-trust.ts` | `internal/connection/trust.go` |
| `packages/client/connection/src/api-path.ts` | `connection/rpc.go` |
| `packages/client/connection/src/websocket-downlink.ts` | `internal/connection` |
| `packages/host/apiproxy/src/fetch/handler.ts` 的 transport behavior | `internal/connection/http.go` |

## 2. 职责边界

### 2.1 `connection`

公共 `connection` package 是 transport-independent wire contract owner：

- 拥有四类 RPC message、`RpcResult`、`RpcError`、`RpcReceipt` 和固定 path；
- 拥有事件流的窄 `RPCRequest` 与补全 `ServerRequest` 的 codec；
- 执行第一层 envelope parse，保留 `rpcId` mint/echo 规则；
- 只在 wire erasure 点使用 `json.RawMessage`；
- 不依赖 Echo、API Proxy、Agent、Session 或具体 Provider；
- 不验证某个 method 的业务 payload。

### 2.2 `internal/connection`

`internal/connection` 是 Echo v5 inbound adapter：

- 拥有 listener、route、body budget、content type、HTTP error rendering、panic recovery、trust fence、WebSocket pump 和 graceful shutdown；
- 把 Echo request 映射为 `context.Context`、method、`rpcId` 与 raw payload；
- 只依赖 consumer-owned `RPCDispatcher` 与 `EventSource`，不导入 API Proxy 的具体类型；
- 可在所有协议 route 之后挂载一个 `http.Handler` 处理未被占用的浏览器 GET/HEAD 路径；
- 不包含 method payload schema、Host snapshot 组装或 Agent/Session 业务；
- 不读取或解释前端静态资源，具体页面由根级 `web` 包拥有；
- 不把 `echo.Context` 传给下游或异步 goroutine。

### 2.3 composition root

`cmd/goren` 是 composition root，但不直接构造 API method Catalog 或 Echo Host。它创建静态 Factory Catalog 与 Plugin Runtime，把 typed Plugin declarations 交给 `internal/assembly`，并用进程 signal context 触发 Runtime shutdown。默认 `@deepseek-ai/dsh-client-connection` Plugin 依赖 `apiProxy` 与 `webFrontend` Service，通过 effect 同步绑定 listener、启动 Echo，并由 disposer 等待 HTTP/WebSocket cleanup；不启用 Web 的自定义 composition 仍可只依赖 `apiProxy`。该装配不改变 Connection 的下游 contract。

## 3. 依赖方向

```text
TypeScript Client
  -> HTTP /api + WebSocket downlinks
  -> internal/connection
       -> connection wire contract
       -> RPCDispatcher (consumer-owned interface)
            <- apiproxy.Catalog
                 -> host.describe Provider
       -> EventSource (consumer-owned interface)
            <- apiproxy.EventStreams
       -> Frontend http.Handler (optional)
            <- web.Site
```

允许的依赖是 inbound adapter 指向 contract 和消费接口，具体 Provider 只在 composition root 装配。`connection` 与 API Proxy 不知道 Echo；Host Provider 不知道 HTTP。

## 4. 一元请求流程

```text
POST /api/<method>
  -> trust fence
  -> canonical path 与 privileged loopback policy
  -> media type / body budget / JSON syntax
  -> route ownership check
  -> ClientRequest envelope parse
  -> path == envelope.method
  -> RPCDispatcher.DispatchUnary(ctx, method, rpcId, payload)
  -> ServerResponse echoes rpcId
```

错误分层保持源语义：

- Host/Origin 不可信：Connection 返回 `403`，下游不执行；
- HTTP method、API root 或非 unary route 错误：Connection 返回 `404`；
- media type 错误：Connection 返回 `415`；
- body 不是 JSON：Connection 返回 `400`；
- 普通 `POST /api/<unknown>` 仍先执行 media type 与 JSON carrier validation，因此依次可能返回 `415`、`400` 或在 body 可读后返回 `404`；
- envelope、path/method 或 method payload 错误：返回 HTTP `200` 的 `bad-request`；
- Provider 返回技术错误或 panic：返回 HTTP `500`；
- 业务拒绝：仍返回 HTTP `200` 的 `RpcResult` failure。

## 5. WebSocket 下行流程与生命周期

```text
GET /api/events.mux 或 /api/events.host
  -> trust fence
  -> 非 upgrade 请求返回 426
  -> coder/websocket Accept
  -> EventSource.Mux 或 EventSource.Host
  -> connection.EncodeServerRequest
  -> 一个完整 JSON 文档写入一个 text message
```

API Proxy 事件源产生窄 `RPCRequest`，其中包含 `rpcId` 与带 `type` 判别的 payload；Connection 从 `payload.type` 补全 `ServerRequest.method`，不理解 Session/Host frame 的业务字段。两条 socket 及其 source context 相互独立，不提供跨流排序。

每条 downlink 使用同步 `emit -> EncodeServerRequest -> socket.Write` 链路，任一时刻最多一个在途 frame。Connection 不增加无界队列，不在慢客户端时丢弃或重排 frame；socket 写未完成时 `emit` 保持阻塞，把背压传回事件源。客户端断开或 Host shutdown 会取消该写，事件源必须响应 context 并结束清理。业务 owner 若未来需要合并 snapshot 或限制生产速率，应在自己的事件语义中设计，不能由 carrier 猜测哪些 frame 可丢弃。

此处的 frame 是应用层事件 payload：一个 `MuxFrame` 或 `HostFrame` 包入一个完整 `ServerRequest`，再写成一个 WebSocket text message。coder/websocket 可能采用的底层协议分片不属于 Harness contract，也不暴露给 API Proxy。

客户端发送任意 data message 时以 code `1008`、reason `downlink only` 关闭。source 技术失败时，carrier 尽力发送一个 `stream/error` 后正常结束 socket；socket 先丢失时不再发送失败帧。普通 GET 只返回 `426 upgrade required`，不提供 SSE fallback。

单条 socket 断开会取消对应 source，新 socket 创建新的 source context。Go Host 不创建或协调 connection generation；现有 TypeScript `ConnectionController` 观察任一流结束后废弃其 client-owned generation 并重建两条流。Host teardown 先禁止新注册，取消全部 source、终止 active socket，再等待所有 pump 和 source cleanup，等待受 graceful deadline 限制。

## 6. 验证所有权：避免重复与冗余

每类输入只由一个 owner 作语义验证：

| 验证 | 唯一 owner | 原因 |
| --- | --- | --- |
| Host、Origin、`Sec-Fetch-Site` | Connection trust fence | 到达任何 API handler 前建立浏览器信任边界 |
| media type、body 上限、JSON 语法 | Connection HTTP adapter | 决定 HTTP `415`、`413`、`400` |
| message `type`、`rpcId`、`method`、payload presence | `connection` envelope codec | 决定合法 JSON 内的 `bad-request` 与 correlation salvage |
| path 与 envelope method 一致 | Connection HTTP adapter | path 是 carrier 输入，只有 adapter 同时拥有两者 |
| method payload shape | API Proxy 注册行的 owner-defined decoder | method contract 不进入 transport |
| `ClientResponse` envelope 与 `RpcResult` union | `connection` envelope codec | 决定 carrier 是否返回 `bad-response` receipt |
| pending interaction response payload | API Proxy pending entry 的 owner-defined decoder | interaction contract 不进入 transport |
| Host/Session/Agent 业务不变量 | 对应 Provider / application owner | 不在 carrier 或 storage adapter 重复判断 |

JSON 语法检查与 envelope parse 是两个有意分离的阶段：前者决定 HTTP `400`，后者决定 HTTP `200 + bad-request`。除此之外，不再用 Echo Binder、额外 schema middleware 或 Provider 重复解析相同 payload。

## 7. `POST /api/respond` 流程

```text
POST /api/respond
  -> trust fence
  -> media type / body budget / JSON syntax
  -> ClientResponse envelope parse
  -> RPCDispatcher.Respond(ctx, message)
  -> RpcReceipt
```

Connection 不持有 pending table，也不判断 approval/question payload。envelope 无效时直接返回 HTTP `200 + {accepted:false, reason:"bad-response"}`；合法 envelope 交给 API Proxy 按 `rpcId` 路由。API Proxy 返回 `accepted`、`not-pending` 或 `bad-response` receipt；其内部技术错误由 Connection 映射为 HTTP `500`。

## 8. privileged method 信任边界

所有 `/api` 请求先经过 deployment-wide trust fence。下列 method 随后再次以空 `trustedHosts` 检查，因此即使部署声明了非 loopback trusted host，也只能从 loopback same-origin 到达：

- `agentPreset.read`、`agentPreset.copy`、`agentPreset.openDocument`、`agentPreset.remove`；
- `host.pickDirectory`、`host.openPath`；
- `settings.describe`、`settings.openDocument`、`settings.update`、`settings.replace`、`settings.mutate`；
- `credentials.describe`、`credentials.set`、`credentials.unset`；
- `llm.discoverModels`。

该检查发生在 method ownership、body 读取和业务 dispatch 之前，避免通过未注册状态或 payload 错误绕开 reachability policy。普通未知 method 则保持源 `toFetchHandler` 的 carrier precedence：先检查 media type/JSON，再判定未注册。该 policy 是 DNS rebinding/cross-site 防线，不是认证或授权。`agentPreset.list`、`agentPreset.select`、`llm.providers` 与 `llm.models` 按源契约不属于此集合；它们仍受全局 trust fence 约束。

## 9. Echo v5 与 coder/websocket 使用决策

实现选择 `github.com/labstack/echo/v5 v5.3.1`。组装方式遵循 Echo 官方 [Quick Start](https://echo.labstack.com/guide/quickstart/) 与 [Graceful Shutdown](https://echo.labstack.com/cookbook/graceful-shutdown/)：

- `echo.New()` 创建唯一 carrier；
- route 使用 `POST` parameter route，框架 `404/405` 由集中 error handler 映射为源协议的 `404`；
- `/api` 与 WebSocket route 先注册，`RouteNotFound` 才把其余浏览器路径交给可选 Frontend handler；未知 API 不会落入 SPA fallback；
- 使用官方 `middleware.RecoverWithConfig`，由集中 `HTTPErrorHandler` 负责未提交响应；
- `StartConfig.Start` 接收 lifecycle context 和 bounded graceful timeout；
- 不使用 Echo Binder 或默认 JSON error rendering。

WebSocket bridge 使用 `github.com/coder/websocket v1.8.15`，只在 `internal/connection` 访问 Echo 暴露的底层 `http.Request`/`http.ResponseWriter`。其默认 same-origin 检查不替代项目 trust fence；前者保护握手，后者还负责 canonical trusted authority 与 `Sec-Fetch-Site` 规则。

API Proxy 已在自身边界把 Provider panic 转为技术错误；Echo Recover 只兜底 adapter/middleware panic。两者保护不同 owner，不对同一业务输入做重复验证。

## 10. 取消与生命周期

Echo request 的底层 `Context` 原样传给 `RPCDispatcher`。客户端断开或上游取消时，只取消本次 owned operation；Catalog、Provider registry 和共享 Runtime 不随请求关闭。

进程 signal 取消 `cmd/goren` 的 lifecycle context后，Runtime 先按依赖图停止 Connection Consumer，再停止 API Proxy Provider。Echo 先停止接收新连接；随后 Connection 终止 hijacked WebSocket、取消事件源，并在 typed `gracefulTimeoutMillis`（零值使用 Connection 默认五秒）内等待 source cleanup。负值在 Factory strict decode 阶段失败。
