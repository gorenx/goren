# 06 Connection Host 模块设计与实现

状态：Accepted

本文拥有 Connection Host 模块的职责、Go 架构、上下游交互和生命周期。RPC 字段、HTTP status 与 WebSocket frame 的权威契约仍在[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)，本文不重复定义线协议；实现状态与验证证据见[08 实施进度](./08-implementation-progress.md)。

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
- 执行第一层 envelope parse，保留 `rpcId` mint/echo 规则；
- 只在 wire erasure 点使用 `json.RawMessage`；
- 不依赖 Echo、API Proxy、Agent、Session 或具体 Provider；
- 不验证某个 method 的业务 payload。

### 2.2 `internal/connection`

`internal/connection` 是 Echo v5 inbound adapter：

- 拥有 listener、route、body budget、content type、HTTP error rendering、panic recovery、trust fence 和 graceful shutdown；
- 把 Echo request 映射为 `context.Context`、method、`rpcId` 与 raw payload；
- 只依赖 consumer-owned `RPCDispatcher`，不导入 API Proxy 的具体类型；
- 不包含 method payload schema、Host snapshot 组装或 Agent/Session 业务；
- 不把 `echo.Context` 传给下游或异步 goroutine。

### 2.3 composition root

`cmd/goren` 是 composition root：创建 API method Catalog、注册 Provider、创建 Connection Host，并用进程 signal context 控制 Echo 生命周期。Plugin Runtime 进入后，由 Plugin effect/disposer 接管同一 listener 与 handler assembly，不改变下游 contract。

## 3. 依赖方向

```text
TypeScript Client
  -> HTTP /api carrier
  -> internal/connection
       -> connection wire contract
       -> RPCDispatcher (consumer-owned interface)
            <- apiproxy.Catalog
                 -> host.describe Provider
```

允许的依赖是 inbound adapter 指向 contract 和消费接口，具体 Provider 只在 composition root 装配。`connection` 与 API Proxy 不知道 Echo；Host Provider 不知道 HTTP。

## 4. 一元请求流程

```text
POST /api/<method>
  -> trust fence
  -> route ownership check
  -> media type / body budget / JSON syntax
  -> ClientRequest envelope parse
  -> path == envelope.method
  -> RPCDispatcher.DispatchUnary(ctx, method, rpcId, payload)
  -> ServerResponse echoes rpcId
```

错误分层保持源语义：

- Host/Origin 不可信：Connection 返回 `403`，下游不执行；
- path 未注册或 HTTP method 错误：Connection 返回 `404`；
- media type 错误：Connection 返回 `415`；
- body 不是 JSON：Connection 返回 `400`；
- envelope、path/method 或 method payload 错误：返回 HTTP `200` 的 `bad-request`；
- Provider 返回技术错误或 panic：返回 HTTP `500`；
- 业务拒绝：仍返回 HTTP `200` 的 `RpcResult` failure。

## 5. 验证所有权：避免重复与冗余

每类输入只由一个 owner 作语义验证：

| 验证 | 唯一 owner | 原因 |
| --- | --- | --- |
| Host、Origin、`Sec-Fetch-Site` | Connection trust fence | 到达任何 API handler 前建立浏览器信任边界 |
| media type、body 上限、JSON 语法 | Connection HTTP adapter | 决定 HTTP `415`、`413`、`400` |
| message `type`、`rpcId`、`method`、payload presence | `connection` envelope codec | 决定合法 JSON 内的 `bad-request` 与 correlation salvage |
| path 与 envelope method 一致 | Connection HTTP adapter | path 是 carrier 输入，只有 adapter 同时拥有两者 |
| method payload shape | API Proxy 注册行的 owner-defined decoder | method contract 不进入 transport |
| Host/Session/Agent 业务不变量 | 对应 Provider / application owner | 不在 carrier 或 storage adapter 重复判断 |

JSON 语法检查与 envelope parse 是两个有意分离的阶段：前者决定 HTTP `400`，后者决定 HTTP `200 + bad-request`。除此之外，不再用 Echo Binder、额外 schema middleware 或 Provider 重复解析相同 payload。

## 6. Echo v5 使用决策

实现选择 `github.com/labstack/echo/v5 v5.3.1`。组装方式遵循 Echo 官方 [Quick Start](https://echo.labstack.com/guide/quickstart/) 与 [Graceful Shutdown](https://echo.labstack.com/cookbook/graceful-shutdown/)：

- `echo.New()` 创建唯一 carrier；
- route 使用 `POST` parameter route，框架 `404/405` 由集中 error handler 映射为源协议的 `404`；
- 使用官方 `middleware.RecoverWithConfig`，由集中 `HTTPErrorHandler` 负责未提交响应；
- `StartConfig.Start` 接收 lifecycle context 和 bounded graceful timeout；
- 不使用 Echo Binder 或默认 JSON error rendering。

API Proxy 已在自身边界把 Provider panic 转为技术错误；Echo Recover 只兜底 adapter/middleware panic。两者保护不同 owner，不对同一业务输入做重复验证。

## 7. 取消与生命周期

Echo request 的底层 `Context` 原样传给 `RPCDispatcher`。客户端断开或上游取消时，只取消本次 owned operation；Catalog、Provider registry 和共享 Runtime 不随请求关闭。

进程 signal 取消 `cmd/goren` 的 lifecycle context后，Echo 停止接收新连接，并在当前默认五秒 graceful timeout 内等待在途请求。该 timeout 后续移入 Connection Plugin typed config；负值在启动前失败。
