# 08 实施进度

状态：In Progress
更新时间：2026-08-14

本文是 DeepSeek Harness Go 复刻实施状态、验证证据、阻塞项和下一步的唯一记录。全局范围与 Gate 由[05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)拥有；模块职责与设计分别由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)和[07 API Proxy 模块设计与实现](./07-api-proxy-module.md)拥有。本文不重新定义协议或架构。

## 1. 进度记录规则

一个阶段包含多个交付或验收子目标时，本文必须逐项展示，不能只给阶段汇总状态。

- **执行状态**：`Planned`、`In Progress`、`Completed`、`Deferred` 或 `Excluded`；
- **证据等级**：`None`、`Implemented`、`Go Verified`、`Contract Verified` 或 `Environment Verified`，含义由[05 的能力状态表](./05-porting-roadmap-and-acceptance.md#13-能力状态表)定义；
- **阶段完成条件**：全部非 Deferred/Excluded 子目标为 `Completed`，且该阶段全部 Gate 已通过；
- **部分实现**：复合子目标只完成一部分时标记 `In Progress`，证据栏写明已完成部分和缺口；
- **既有代码**：只有已经迁移到 Harness contract 并由当前阶段消费，才计入该阶段进度。

## 2. 阶段总览

| 阶段 | 执行状态 | 子目标进度 | 最高证据等级 | 当前重点 |
| --- | --- | --- | --- | --- |
| 阶段 0：基线与 Contract Freeze | In Progress | 10 Completed / 3 In Progress / 1 Planned | Contract Verified | 补齐首期 surface mapping 与 HTTP 负向 differential |
| 阶段 1：Connection Host Carrier | In Progress | 17 Completed / 2 In Progress | Contract Verified | 补齐 HTTP differential 与慢客户端/泄漏验证 |
| 阶段 2：Plugin Runtime | Planned | 0 Completed / 12 Planned | None | 等待 Connection carrier Gate 完成 |
| 阶段 3：Session/Agent slice | Planned | 0 Completed / 16 Planned | None | 不先于阶段 1、2 进入实现 |
| 阶段 4：LLM Contract | Planned | 0 Completed / 13 Planned | None | 既有 `llm` 尚未迁移到 Harness contract |
| 阶段 5：Session 持久化 | Planned | 0 Completed / 14 Planned | None | 先以内存 Session 验证状态机 |
| 阶段 6：客户端能力扩展 | Planned | 0 Completed / 19 Planned | None | 按 TypeScript Client 实际消费逐项进入 |
| 阶段 7：Deferred 能力 | Deferred | 7 Deferred | None | 不创建 package、handler 或依赖占位 |
| 阶段 8：Parity Hardening | In Progress | 0 Completed / 4 In Progress / 11 Planned | Contract Verified | 扩展当前 Connection contract suite，不等同发布验收 |

当前最小 Connection slice 达到 `Contract Verified`：固定 TypeScript schema 与 Go envelope/frame 已交叉校验，固定上游 `WebApiClient` 已调用 Go `host.describe`、读取两条 WebSocket 并调用 `/api/respond`，`ConnectionController` 已验证 client-owned generation 重建。Session/Agent 会话、完整 HTTP 失败矩阵、原 Web 产品和真实 DeepSeek Provider 均未据此标记兼容。

## 3. 阶段 0：基线与 Connection Contract Freeze

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S0-D01 | 交付 | 固定源 commit、版本、许可证和本地参考路径 | Completed | Implemented：`01` 已固定 `47f943...`、`0.1.0-rc.5` 和 `../deepseek-harness` |
| S0-D02 | 交付 | 建立 included/excluded surface manifest | Completed | Contract Verified：`contracts/deepseek-harness/manifest.json` 固定源、当前 surface、Excluded/Deferred 与 privileged method |
| S0-D03 | 交付 | 提取 RPC message、result、receipt、frame、path 和 stable errors | Completed | Go Verified：RPC、path、stable errors、窄 stream request 与完整 Mux/Host frame union 已覆盖 |
| S0-D04 | 交付 | 提取首期 Host、Session、approval/question 和 respond contract | In Progress | Go Verified：`host.describe` 与 respond envelope 已完成；Session 和 interaction 未完成 |
| S0-D05 | 交付 | 建立 contract manifest 和可重复 TypeScript fixture generator | Completed | Contract Verified：固定源 Zod schema 可重复生成 `vectors.json` |
| S0-D06 | 交付 | 单列完整 Web Client 所需但首期未纳入的能力 | Completed | Implemented：`01` 已区分 Workspace、Settings、Goals 等 Deferred 能力 |
| S0-D07 | 交付 | 记录既有 Go `llm` 与目标 contract 的迁移差异 | Completed | Implemented：`01` 已记录 public model、stream 和 route 差异 |
| S0-D08 | 交付 | 确定 NOTICE/provenance 形式 | Planned | None：形式仍是明确未决项 |
| S0-G01 | Gate | fixture 可从干净源 checkout 重复生成 | Completed | Contract Verified：源 checkout 保持干净，生成结果与 committed fixture 逐字一致 |
| S0-G02 | Gate | 每个 surface 可映射到源路径、符号和 owner | In Progress | Implemented：首个 unary slice 已映射，全部 included surface 尚未覆盖 |
| S0-G03 | Gate | path、status、header、discriminant、缺失值和错误码有正负 fixture | In Progress | Contract Verified：message/frame discriminant、缺失 payload/details、closed enum 和 receipt 已覆盖；HTTP status/header differential 未完成 |
| S0-G04 | Gate | extractor 不纳入排除项 | Completed | Contract Verified：generator 只读取固定协议 schema；WebApiClient/ConnectionController 仅作为测试 oracle，不进入 Go 依赖闭包 |
| S0-G05 | Gate | Agent 会话兼容与完整 Web 产品兼容分开记录 | Completed | Implemented：`01` 和 `05` 已明确区分 |
| S0-G06 | Gate | 未实现能力只标记 Planned 或 Deferred | Completed | Implemented：当前矩阵未把未实现能力计为完成 |

## 4. 阶段 1：Connection Host Carrier

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S1-D01 | 交付 | `connection` 拥有 RPC、receipt、窄 stream request 和协议常量 | Completed | Go Verified：envelope、codec、path 与 `RPCRequest` 已覆盖 |
| S1-D02 | 交付 | Echo v5 与 `coder/websocket` Host carrier | Completed | Go Verified：HTTP 与真实网络 WebSocket carrier 已覆盖 |
| S1-D03 | 交付 | typed API method registry 与 deterministic handler | Completed | Go Verified：Catalog 与 `host.describe` 已覆盖 |
| S1-D04 | 交付 | `POST /api/<method>` 与 `POST /api/respond` | Completed | Go Verified：unary、accepted、bad-response、not-pending 与技术失败已覆盖 |
| S1-D05 | 交付 | `/api/events.mux` 与 `/api/events.host` 两条下行 WebSocket | Completed | Go Verified：两条真实 socket 独立发送 text `ServerRequest` |
| S1-D06 | 交付 | 独立 stream lifecycle、pending correlation、取消和 bounded shutdown | Completed | Go Verified：stream 独立取消、pending 原子 claim/withdraw 与 bounded teardown 已覆盖 |
| S1-D07 | 交付 | Host/Origin/cross-site fence 与 privileged method loopback policy | Completed | Go Verified：全局 fence 与 15 个源 privileged method 的二次 loopback fence 已覆盖 |
| S1-D08 | 交付 | `apiproxy` 拥有 Mux/Host frame union | Completed | Go Verified：10 个 MuxFrame 与 10 个 HostFrame 分支、宽字段边界和 canonical type encoding 已覆盖 |
| S1-G01 | Gate | TypeScript Connection 可调用 Go `host.describe` | Completed | Contract Verified：固定源 `WebApiClient.host.describe` 直接调用真实 Go HTTPHost |
| S1-G02 | Gate | HTTP 路径、载荷和失败状态与源实现一致 | In Progress | Contract Verified：`host.describe`、两条 WS path 和 respond/not-pending 已通过；完整 status/header/失败 differential 未完成 |
| S1-G03 | Gate | Echo 默认路由、错误和 recovery 被协议 adapter 接管 | Completed | Go Verified：404/405、错误映射与 middleware panic recovery 已覆盖 |
| S1-G04 | Gate | `rpcId`、accepted、not-pending 和 bad-response 全覆盖 | Completed | Go Verified：合法结算、坏响应重试、late/duplicate 和并发首个 claim 已覆盖 |
| S1-G05 | Gate | WebSocket 客户端上行触发 policy close | Completed | Go Verified：code `1008`、reason `downlink only` |
| S1-G06 | Gate | socket 断开取消对应 source，Host teardown 等待全部 cleanup | Completed | Go Verified：断线取消、新连接隔离、cleanup wait 与 deadline 已覆盖 |
| S1-G07 | Gate | TypeScript Client 在任一 socket 结束后重建 generation | Completed | Contract Verified：首个 mux source 结束后固定源 `ConnectionController` 重开 mux/host 并再次 connected |
| S1-G08 | Gate | HTTP 断开只取消 owned handler | Completed | Go Verified：`TestUnaryRequestCancellationReachesProvider` |
| S1-G09 | Gate | `echo.Context` 不越过 transport boundary | Completed | Go Verified：API Proxy 仅接收 `context.Context` 和 typed request |
| S1-G10 | Gate | body、慢客户端、stream failure、shutdown 和泄漏有测试 | In Progress | Go Verified：body、stream failure、cleanup wait/deadline 已覆盖；慢客户端和 leak audit 未完成 |
| S1-G11 | Gate | trust fence 覆盖 loopback、allowlist、Origin mismatch 和 cross-site | Completed | Go Verified：`internal/connection/trust_test.go` |

当前纵向调用链：

```text
POST /api/host.describe
  -> Echo v5 Connection Host
  -> RPC envelope decode
  -> typed API Proxy Catalog
  -> HostDescriptionProvider
  -> ServerResponse
```

当前下行调用链：

```text
GET /api/events.mux 或 /api/events.host
  -> trust fence / WebSocket upgrade
  -> typed apiproxy.EventStreams
  -> StreamRequest[MuxFrame 或 HostFrame]
  -> connection.RPCRequest
  -> ServerRequest text message
  -> socket close cancels only its source
```

当前 response 调用链：

```text
interaction owner registers stable rpcId + decoder
  -> POST /api/respond
  -> ClientResponse envelope decode
  -> API Proxy pending lookup and second parse
  -> atomic settle or withdrawal-safe receipt
```

该链路已完成通用 pending 基础设施；approval/question 的具体 schema、requested/resolved frame、replay 和业务结果仍属于 S3-D08，不计为当前完成。

## 5. 阶段 2：Plugin Runtime 与 Server Assembly

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S2-D01 | 交付 | Plugin、Factory、Manifest、Scope、Disposer 和 typed keys | Planned | None |
| S2-D02 | 交付 | Service graph、typed Event modes、rollback、replacement 和 shutdown | Planned | None |
| S2-D03 | 交付 | Factory Catalog 与静态 composition root | Planned | None：当前 `cmd/goren` 是直接装配，不是 Plugin Runtime |
| S2-D04 | 交付 | 首期 typed config 与 strict validation | Planned | None |
| S2-D05 | 交付 | 只包含 Connection Server 的 Plugin assembly | Planned | None |
| S2-D06 | 交付 | lifecycle diagnostics 和 leak-oriented tests | Planned | None |
| S2-G01 | Gate | Service 缺失、重复、启动失败、卸载和替换有测试 | Planned | None |
| S2-G02 | Gate | Event modes 的顺序与错误语义有 fixture | Planned | None |
| S2-G03 | Gate | Plugin 启动失败不遗留 contribution 或资源 | Planned | None |
| S2-G04 | Gate | listener 与 handler registration 由 effect 拥有且可撤销 | Planned | None |
| S2-G05 | Gate | Excluded/Deferred 能力不进入 Catalog 或依赖闭包 | Planned | None |
| S2-G06 | Gate | `!!js`、未知字段、类型错误和无效组合严格失败 | Planned | None |

## 6. 阶段 3：Session/Agent 会话纵向切片

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S3-D01 | 交付 | in-memory append-only Session | Planned | None |
| S3-D02 | 交付 | System Prompt registry 与 snapshot assembly | Planned | None |
| S3-D03 | 交付 | Tool definition、registry、executor 和 policy waterfall | Planned | None |
| S3-D04 | 交付 | Agent registry、inbox、scope 与实时事件 | Planned | None |
| S3-D05 | 交付 | 最小 Agent Loop | Planned | None |
| S3-D06 | 交付 | fake LLM Adapter 与 deterministic Tool | Planned | None |
| S3-D07 | 交付 | 首期 `session.*` handler | Planned | None：`host.describe` 已在阶段 1 完成 |
| S3-D08 | 交付 | Mux/Host frame 与 approval/question 闭环 | Planned | None |
| S3-D09 | 交付 | client cancel、disconnect 与 turn cancellation 映射 | Planned | None |
| S3-G01 | Gate | TypeScript Connection 完成 Session prompt 并收到最终事件 | Planned | None |
| S3-G02 | Gate | `user/message` 到 `turn/end` 全流程可运行 | Planned | None |
| S3-G03 | Gate | step、拒绝、取消、模型和 Tool failure 有 golden | Planned | None |
| S3-G04 | Gate | approval/question 通过 respond 形成闭环 | Planned | None |
| S3-G05 | Gate | 每个模型请求可由 Session 日志重建 | Planned | None |
| S3-G06 | Gate | reconnect 读取 baseline 且不重复 committed event | Planned | None |
| S3-G07 | Gate | Agent Loop 不依赖 transport、driver、vendor 或 Deferred adapter | Planned | None |

## 7. 阶段 4：LLM Contract 与 DeepSeek Provider

既有 `llm` 代码只是迁移材料，尚未采用 Harness-compatible contract，因此不计为本阶段完成。

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S4-D01 | 交付 | Harness-compatible Message、Content、StreamChunk、finish、usage 和 options | Planned | None |
| S4-D02 | 交付 | LLM Adapter Registry 与 Runtime | Planned | None |
| S4-D03 | 交付 | DeepSeek adapter | Planned | None |
| S4-D04 | 交付 | 复用既有 transport 并迁移调用者 | Planned | None |
| S4-D05 | 交付 | retry、error classification、partial stream、usage 和 cancellation | Planned | None |
| S4-D06 | 交付 | fake stream 与录制响应 fixtures | Planned | None |
| S4-G01 | Gate | 从源 fixture 建立目标类型和 codec | Planned | None |
| S4-G02 | Gate | 在唯一 `llm` owner 内完成新 Runtime | Planned | None |
| S4-G03 | Gate | 迁移 adapter、example 和调用者 | Planned | None |
| S4-G04 | Gate | 删除旧 `Model`/`APIAdapter` 重复入口 | Planned | None |
| S4-G05 | Gate | 迁移后运行 AST 命名审计 | Planned | None：审计器已存在，但阶段迁移尚未发生 |
| S4-G06 | Gate | TS/Go 双向 fixture 与全部 failure/cancel 场景通过 | Planned | None |
| S4-G07 | Gate | 真实 DeepSeek smoke 独立验收 | Planned | None |

## 8. 阶段 5：Session 持久化与重连恢复

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S5-D01 | 交付 | JSONL Store adapter 与 Session fork/repair use case | Planned | None |
| S5-D02 | 交付 | reconnect baseline、pending replay 和 queue snapshot | Planned | None |
| S5-D03 | 交付 | 按实际需求加入 SQLite/sqlc projection adapter | Planned | None |
| S5-D04 | 交付 | context transform、compaction、attachment/spill 和 budget | Planned | None |
| S5-D05 | 交付 | stable identity 与 scope | Planned | None |
| S5-G01 | Gate | JSONL crash、截断、未知事件和开放 turn repair 有测试 | Planned | None |
| S5-G02 | Gate | adapter 只报告技术状态，Recovery 决定业务修复 | Planned | None |
| S5-G03 | Gate | reconnect 不丢 committed event 或发布未提交状态 | Planned | None |
| S5-G04 | Gate | compaction 保留请求、Tool 关联和 evidence | Planned | None |
| S5-G05 | Gate | 大事件、慢盘、锁冲突、磁盘满和取消不伪成功 | Planned | None |
| S5-G06 | Gate | SQLite migration 与 sqlc generation 可重复 | Planned | None |
| S5-G07 | Gate | sqlc 和 driver 类型不泄漏到业务 contract | Planned | None |
| S5-G08 | Gate | Projection use case 拥有 mutation 和 transaction intent | Planned | None |
| S5-G09 | Gate | SQLite projection 可从 JSONL 事实流重建 | Planned | None |

## 9. 阶段 6：按客户端需求扩展服务端能力

每项能力在实际 Client/API、Agent preset 或 Tool 消费证据出现后独立进入，不按源目录批量开始。

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S6-D01 | 能力 | Workspace | Planned | None |
| S6-D02 | 能力 | Filesystem | Planned | None |
| S6-D03 | 能力 | Shell | Planned | None |
| S6-D04 | 能力 | PTY | Planned | None |
| S6-D05 | 能力 | LSP | Planned | None |
| S6-D06 | 能力 | Sandbox | Planned | None |
| S6-D07 | 能力 | Guard | Planned | None |
| S6-D08 | 能力 | Credentials | Planned | None |
| S6-D09 | 能力 | Attachment | Planned | None |
| S6-D10 | 能力 | Spill | Planned | None |
| S6-D11 | 能力 | Settings | Planned | None |
| S6-D12 | 能力 | Goals | Planned | None |
| S6-D13 | 能力 | 其他经 capability matrix 纳入的服务端能力 | Planned | None |
| S6-G01 | Gate | 确认真实 Consumer 后才进入实现 | Planned | None |
| S6-G02 | Gate | 保留源 Definition、Provider 和 Consumer owner | Planned | None |
| S6-G03 | Gate | effect-time enforcement 归 permission/guard/sandbox owner | Planned | None |
| S6-G04 | Gate | 覆盖 success、failure、cancel、shutdown 和平台限制 | Planned | None |
| S6-G05 | Gate | credential 不进入日志、Session、错误或 fixture | Planned | None |
| S6-G06 | Gate | 首次实现前完成技术依赖准入 | Planned | None |

## 10. 阶段 7：Deferred 能力

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S7-D01 | 能力 | Headless CLI | Deferred | None：不是客户端协议兼容前置条件 |
| S7-D02 | 能力 | ACP stdio adapter | Deferred | None |
| S7-D03 | 能力 | MCP Client Bridge | Deferred | None |
| S7-D04 | 能力 | 最小 Typert endpoint dispatch | Deferred | None：仅在纳入 endpoint 确有依赖时进入 |
| S7-D05 | 能力 | Jobs、Subagent 与 Workflow | Deferred | None |
| S7-G01 | Gate | 进入任一能力时同步更新范围、contract、技术决策和矩阵 | Deferred | None |
| S7-G02 | Gate | 不以空 package、空 handler、固定成功或未使用依赖占位 | Deferred | None：当前未创建占位 |

## 11. 阶段 8：Parity Hardening 与发布

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S8-D01 | 交付 | included capability matrix | Planned | None：当前实施矩阵不替代最终 capability matrix |
| S8-D02 | 交付 | 跨语言 replay/differential suite | In Progress | Contract Verified：Connection/schema/client slice 已建立；Session/Agent/LLM replay 尚未进入 |
| S8-D03 | 交付 | 多平台 CI | Planned | None |
| S8-D04 | 交付 | race、fuzz、故障注入、泄漏和长时测试 | Planned | None |
| S8-D05 | 交付 | dependency、license 和 NOTICE 清单 | In Progress | Implemented：Echo 准入已记录；完整发布清单未建立 |
| S8-D06 | 交付 | security threat review | Planned | None |
| S8-D07 | 交付 | 性能与资源预算 | Planned | None |
| S8-D08 | 交付 | 安装、升级、migration 和恢复说明 | Planned | None |
| S8-G01 | Gate | 每个 included surface 标明 P0/P1/P2/P3 | Planned | None |
| S8-G02 | Gate | Connection 与 Agent 关键路径达到计划层级 | Planned | None |
| S8-G03 | Gate | Linux、macOS、Windows 分别有支持证据 | Planned | None：当前只验证 `darwin/arm64` |
| S8-G04 | Gate | 全量 Go、格式和 contract suite 通过 | In Progress | Contract Verified：当前 Go checks 与显式 TypeScript contract suite 通过；全项目 parity suite 尚未完成 |
| S8-G05 | Gate | TypeScript Client 与 DeepSeek Provider 分别真实验收 | In Progress | Contract Verified：固定 TypeScript Connection 最小 slice 已通过；DeepSeek Provider 与完整会话未验收 |
| S8-G06 | Gate | 依赖闭包和二进制扫描确认排除范围未进入 | Planned | None |
| S8-G07 | Gate | open decision 已解决、受控延期或移出 release | Planned | None |

## 12. 当前代码与测试证据

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
| RPCRequest 到 ServerRequest 的 method/payload 补全 | `connection/stream_test.go` |
| API Proxy mux/host 独立事件源 | `apiproxy/events_test.go` |
| Mux/Host 全部分支、closed enum、required array 与宽字段保真 | `apiproxy/frame_test.go` |
| WebSocket 双流、426、1008、stream error、取消和 teardown | `internal/connection/websocket_test.go` |
| pending accepted、坏响应重试、取消、late/duplicate 与并发 claim | `apiproxy/pending_test.go` |
| `/api/respond` accepted/技术失败、privileged loopback 与 Echo Recover | `internal/connection/http_test.go` |
| 命名规则与审计器自测 | `tests/architecture/naming_test.go` |
| manifest 与 Go path/message/frame surface 一致 | `TestPinnedManifestMatchesGoSurface` |
| Go envelope/frame 与固定 TypeScript schema 向量一致 | `TestGoAgreesWithPinnedSourceVectors` |
| 固定源 schema 可重复生成 committed fixture | `TestPinnedSourceGeneratesCommittedVectors` |
| 固定源 `WebApiClient` 调用 Go HTTP/WS/respond | `TestPinnedSourceWebApiClientTalksToGoHost` |
| 固定源 `ConnectionController` 在单流结束后重建双流 | `TestPinnedSourceConnectionRebuildsBothStreams` |

## 13. 当前验证结果

在 Go 1.26.6、`darwin/arm64` 执行并通过：

- `go fmt ./...`
- `go mod tidy`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `go test -tags=contract ./tests/contract`（Node v22.23.0；源 commit `47f943...`）
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`：`No vulnerabilities found`
- 变更文档本地链接检查
- `git diff --check`

真实进程 smoke：启动 `cmd/goren`，向 `127.0.0.1` 发送 `POST /api/host.describe`，得到同 `rpcId` 的成功 `ServerResponse`，value 包含 version、cwd、`attachedSessions=0` 和 `canOpenPath=false`。跨语言测试另使用固定源 `WebApiClient` 直连测试 Go HTTPHost，并由固定源 `ConnectionController` 完成两代双流连接。

## 14. 安全与依赖状态

- Echo 固定为 `github.com/labstack/echo/v5 v5.3.1`，`coder/websocket` 固定为 `v1.8.15`，准入记录见[04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)；
- 初次扫描在 Go 1.26.5 发现 6 个可达标准库漏洞；module 已提升到 Go 1.26.6 并复扫通过；
- 当前 listener 默认只绑定 `127.0.0.1:3080`；非 loopback deployment 的 TLS、认证和授权尚未进入范围；
- `.env`、credential 和 secret 未进入变更。

## 15. 下一实现切片

按阶段 1 矩阵顺序推进：

1. 补齐慢客户端背压和 goroutine/WebSocket leak audit；
2. 完成剩余 HTTP status/header/失败 differential，收敛阶段 1 Gate。

阶段 1 Gate 完成后进入 Plugin Runtime；approval/question 的业务闭环随阶段 3 的 Session/Agent owner 一起实现，不在 Connection 或通用 pending registry 中提前伪造。
