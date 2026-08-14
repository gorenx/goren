# 08 实施进度

状态：In Progress
更新时间：2026-08-14

本文是 DeepSeek Harness Go 复刻实施状态、验证证据、阻塞项和下一步的唯一记录。全局范围与 Gate 由[05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)拥有；模块职责与设计分别由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)和[07 API Proxy 模块设计与实现](./07-api-proxy-module.md)拥有。本文不重新定义协议或架构。

## 1. 阶段状态

| 阶段 | 状态 | 当前证据等级 | 说明 |
| --- | --- | --- | --- |
| 阶段 0：基线与 Contract Freeze | In Progress | Implemented | 已固定源 commit 并提取首个 unary contract；可重复 fixture/manifest 尚未建立 |
| 阶段 1：Connection Host Carrier | In Progress | Go Verified | unary carrier、trust fence、`/api/respond` 基本 receipt 与 `host.describe` 已运行 |
| 阶段 2：Plugin Runtime | Planned | None | 尚未创建 package 或占位实现 |
| 阶段 3：Session/Agent slice | Planned | None | 尚未开始 |
| 阶段 4 以后 | Planned / Deferred | None | 按 05 的进入条件执行 |

当前没有能力达到 Contract Verified：TypeScript fixture/differential suite 尚未调用 Go server。原 Web Client 也未标记可运行。

## 2. 已实现纵向切片

```text
POST /api/host.describe
  -> Echo v5 Connection Host
  -> RPC envelope decode
  -> typed API Proxy Catalog
  -> HostDescriptionProvider
  -> ServerResponse
```

已实现：

- `connection`：四类 RPC envelope、完整 error code/detail shape、`RpcReceipt`、path 常量和第一层 codec；
- `apiproxy`：泛型 unary Catalog、method payload decoder、业务/技术失败分离和 duplicate owner 拒绝；
- `host.describe`：typed request/response 与 consumer-owned Provider；
- `internal/connection`：Echo route、trust fence、body budget、集中 error mapping、Recover 与 graceful lifecycle；
- `/api/respond`：`ClientResponse` envelope parse、`bad-response` 与当前无 pending entry 时的 `not-pending`；
- `cmd/goren`：最小静态 composition root 和可取消进程 lifecycle；
- `tests/architecture`：仓库级 AST 命名审计，自动覆盖变量、参数、receiver、named return、短声明和 range binding。

## 3. 代码与测试证据

| 行为 | 证据 |
| --- | --- |
| RPC envelope、error detail、receipt decode | `connection/rpc_test.go` |
| typed `host.describe` dispatch | `apiproxy/catalog_test.go` |
| payload failure 不调用 Provider | `TestCatalogRejectsInvalidPayloadBeforeProvider` |
| returned error 与 panic 分为技术失败 | `TestCatalogSeparatesBusinessAndTechnicalFailure` |
| unary status、correlation、method/path、payload failure | `internal/connection/http_test.go` |
| request cancellation 传播 | `TestUnaryRequestCancellationReachesProvider` |
| body budget | `TestBodyLimit` |
| Host/Origin/cross-site fence | `internal/connection/trust_test.go` |
| 命名规则与审计器自测 | `tests/architecture/naming_test.go` |

## 4. 本次验证结果

在 Go 1.26.6、`darwin/arm64` 执行并通过：

- `go fmt ./...`
- `go mod tidy`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`：`No vulnerabilities found`
- 变更文档本地链接检查
- `git diff --check`

真实进程 smoke：启动 `cmd/goren`，向 `127.0.0.1` 发送 `POST /api/host.describe`，得到同 `rpcId` 的成功 `ServerResponse`，value 包含 version、cwd、`attachedSessions=0` 和 `canOpenPath=false`。

## 5. 安全与依赖状态

- Echo 固定为 `github.com/labstack/echo/v5 v5.3.1`，准入记录见[04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)；
- 初次扫描在 Go 1.26.5 发现 6 个可达标准库漏洞；module 已提升到 Go 1.26.6 并复扫通过；
- 当前 listener 默认只绑定 `127.0.0.1:3080`；非 loopback deployment 的 TLS、认证和授权尚未进入范围；
- `.env`、credential 和 secret 未进入变更。

## 6. 未完成与下一步

阶段 1 仍缺少：

1. `/api/events.mux` 与 `/api/events.host` WebSocket downlink；
2. `coder/websocket` 依赖、text-frame 与 client-write policy close；
3. connection generation、双 socket readiness、断线失效和 bounded stream shutdown；
4. approval/question pending table 与 accepted response；
5. TypeScript Connection 对 Go server 的跨语言 fixture/differential test；
6. Plugin effect/disposer 接管 listener 与 handler registration；
7. trust authority、header 与错误 body 的完整 differential fixtures。

下一实现切片应先完成两条 WebSocket downlink 与 generation lifecycle，再把 `/api/respond` 接到真实 pending interaction；在此之前不开始 Session/Agent 业务。
