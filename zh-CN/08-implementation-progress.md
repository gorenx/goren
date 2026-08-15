# 08 实施进度

状态：In Progress
更新时间：2026-08-14

本文是 DeepSeek Harness Go 复刻实施状态、验证证据、阻塞项和下一步的唯一记录。全局范围与 Gate 由[05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)拥有；模块职责与设计分别由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)、[07 API Proxy 模块设计与实现](./07-api-proxy-module.md)、[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)、[10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)、[11 System Prompt Registry 与 Assembly 模块设计](./11-system-prompt-registry-and-assembly.md)、[12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)、[13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)、[14 Agent Registry、Inbox 与实时事件模块设计](./14-agent-registry-inbox-and-events.md)、[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)和[16 Session API Gateway 与实时 Frame 投影](./16-session-api-gateway-and-live-frames.md)拥有。本文不重新定义协议或架构。

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
| 阶段 0：基线与 Contract Freeze | In Progress | 11 Completed / 2 In Progress / 1 Planned | Contract Verified | 补齐首期 surface mapping 与 NOTICE/provenance 决策 |
| 阶段 1：Connection Host Carrier | Completed | 19 Completed | Contract Verified | Gate 已完成，后续扩展随新增 included surface 进入 |
| 阶段 2：Plugin Runtime | Completed | 12 Completed | Go Verified | Gate 已完成；后续能力通过既有 Factory/Service/Event seam 进入 |
| 阶段 3：Session/Agent slice | In Progress | 12 Completed / 3 In Progress / 1 Planned | Contract Verified | Session API/live frame/client cancel 已完成；下一步接入 approval/question 并补 failure/resume golden |
| 阶段 4：LLM Contract | In Progress | 10 Completed / 2 In Progress / 1 Planned | Contract Verified | 完成 Runtime、DeepSeek adapter 与 Agent attempt loop；等待默认 retry policy consumer、可复用录制 fixture 和真实环境 smoke |
| 阶段 5：Session 持久化 | Planned | 0 Completed / 14 Planned | None | 先以内存 Session 验证状态机 |
| 阶段 6：客户端能力扩展 | Planned | 0 Completed / 19 Planned | None | 按 TypeScript Client 实际消费逐项进入 |
| 阶段 7：Deferred 能力 | Deferred | 7 Deferred | None | 不创建 package、handler 或依赖占位 |
| 阶段 8：Parity Hardening | In Progress | 0 Completed / 5 In Progress / 10 Planned | Contract Verified | 扩展当前 Connection contract suite，不等同发布验收 |

当前 Connection slice、Session core、System Prompt、Native Tools、Agent Inbox、Agent Loop、Session API Gateway 与 LLM/DeepSeek contract 达到 `Contract Verified`：固定 TypeScript schema 与 Go envelope/frame 已交叉校验，固定上游 `WebApiClient` 已通过真实 Go HTTP/WebSocket 完成 create/list/history/models/selectModel、prompt 到 `turn/end`、queue edit/remove、cancel 到 aborted turn，并读取 Mux/Host live frame；`ConnectionController` 已验证 client-owned generation 重建。固定源 Session、System Prompt、Native Tools、Agent Inbox、Agent Loop happy-path request/event/derived projection 和 DeepSeek serialization/stream/default retry 也分别与 Go 行为交叉验证。approval/question、Code Mode、原 Web 产品和使用真实 credential/endpoint 的 DeepSeek 环境验收尚未据此标记兼容。

## 3. 阶段 0：基线与 Connection Contract Freeze

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S0-D01 | 交付 | 固定源 commit、版本、许可证和本地参考路径 | Completed | Implemented：`01` 已固定 `47f943...`、`0.1.0-rc.5` 和 `../deepseek-harness` |
| S0-D02 | 交付 | 建立 included/excluded surface manifest | Completed | Contract Verified：`contracts/deepseek-harness/manifest.json` 固定源、当前 surface、Excluded/Deferred 与 privileged method |
| S0-D03 | 交付 | 提取 RPC message、result、receipt、frame、path 和 stable errors | Completed | Go Verified：RPC、path、stable errors、窄 stream request 与完整 Mux/Host frame union 已覆盖 |
| S0-D04 | 交付 | 提取首期 Host、Session、approval/question 和 respond contract | In Progress | Contract Verified：`host.describe`、respond envelope 与 Session core 已完成；interaction 未完成 |
| S0-D05 | 交付 | 建立 contract manifest 和可重复 TypeScript fixture generator | Completed | Contract Verified：固定源 Zod schema 可重复生成 `vectors.json` |
| S0-D06 | 交付 | 单列完整 Web Client 所需但首期未纳入的能力 | Completed | Implemented：`01` 已区分 Workspace、Settings、Goals 等 Deferred 能力 |
| S0-D07 | 交付 | 记录既有 Go `llm` 与目标 contract 的迁移差异 | Completed | Implemented：`01` 已记录 public model、stream 和 route 差异 |
| S0-D08 | 交付 | 确定 NOTICE/provenance 形式 | Planned | None：形式仍是明确未决项 |
| S0-G01 | Gate | fixture 可从干净源 checkout 重复生成 | Completed | Contract Verified：源 checkout 保持干净，生成结果与 committed fixture 逐字一致 |
| S0-G02 | Gate | 每个 surface 可映射到源路径、符号和 owner | In Progress | Implemented：首个 unary slice 已映射，全部 included surface 尚未覆盖 |
| S0-G03 | Gate | path、status、header、discriminant、缺失值和错误码有正负 fixture | Completed | Contract Verified：21 个 raw HTTP case、message/frame vectors、receipt、缺失字段、closed enum、status 与 media type 已覆盖 |
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
| S1-G02 | Gate | HTTP 路径、载荷和失败状态与源实现一致 | Completed | Contract Verified：固定源 `toFetchHandler` 与真实 Go HTTPHost 对 21 个 success/failure case 的 status、media type 与稳定 body 一致 |
| S1-G03 | Gate | Echo 默认路由、错误和 recovery 被协议 adapter 接管 | Completed | Go Verified：404/405、错误映射与 middleware panic recovery 已覆盖 |
| S1-G04 | Gate | `rpcId`、accepted、not-pending 和 bad-response 全覆盖 | Completed | Go Verified：合法结算、坏响应重试、late/duplicate 和并发首个 claim 已覆盖 |
| S1-G05 | Gate | WebSocket 客户端上行触发 policy close | Completed | Go Verified：code `1008`、reason `downlink only` |
| S1-G06 | Gate | socket 断开取消对应 source，Host teardown 等待全部 cleanup | Completed | Go Verified：断线取消、新连接隔离、cleanup wait 与 deadline 已覆盖 |
| S1-G07 | Gate | TypeScript Client 在任一 socket 结束后重建 generation | Completed | Contract Verified：首个 mux source 结束后固定源 `ConnectionController` 重开 mux/host 并再次 connected |
| S1-G08 | Gate | HTTP 断开只取消 owned handler | Completed | Go Verified：`TestUnaryRequestCancellationReachesProvider` |
| S1-G09 | Gate | `echo.Context` 不越过 transport boundary | Completed | Go Verified：API Proxy 仅接收 `context.Context` 和 typed request |
| S1-G10 | Gate | body、慢客户端、stream failure、shutdown 和泄漏有测试 | Completed | Go Verified：同步写背压、shutdown 解阻塞、32 次 source 清理、socket/pump 归零及既有 failure/deadline 均覆盖 |
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
| S2-D01 | 交付 | Plugin、Factory、Manifest、Scope、Disposer 和 typed keys | Completed | Go Verified：`plugin` 提供静态 interface、泛型自由函数、opaque Child Scope/lineage 与 LIFO ownership |
| S2-D02 | 交付 | Service graph、typed Event modes、rollback、replacement 和 shutdown | Completed | Go Verified：waiting settlement、live withdraw/re-provide、五种 Event mode、scoped waterfall、shadow replacement 与 dependent-first shutdown 已覆盖；callback 保留精确泛型类型 |
| S2-D03 | 交付 | Factory Catalog 与静态 composition root | Completed | Go Verified：`cmd/goren -> internal/assembly -> Catalog -> Runtime`，入口不再直接拼装 Echo/API Proxy |
| S2-D04 | 交付 | 首期 typed config 与 strict validation | Completed | Go Verified：owner config 拒绝 duplicate/unknown/type/range/combination 与多值输入，类型擦除止于 Factory Catalog；System Prompt 和 Tools 保留各自的 omitted/empty/null/default 语义 |
| S2-D05 | 交付 | 只包含当前 included server 能力的 Plugin assembly | Completed | Go Verified：LLM 提供 `llm`，DeepSeek 消费并注册 route；Agent Default Model、Session、System Prompt、Tools、Agent Loop、Session API Gateway、API Proxy 与 Connection 按 Service graph 结算 |
| S2-D06 | 交付 | lifecycle diagnostics 和 leak-oriented tests | Completed | Go Verified：`PluginStatus` 暴露状态/effect/error；rollback、unload、replacement、shutdown 后 effect/contribution 清空 |
| S2-G01 | Gate | Service 缺失、重复、启动失败、卸载和替换有测试 | Completed | Go Verified：`plugin/runtime_test.go` |
| S2-G02 | Gate | Event modes 的顺序与错误语义有 fixture | Completed | Go Verified：`plugin/events_test.go` 覆盖 emit/parallel/serial/bail/waterfall 及 global/ancestor/exact scope admission |
| S2-G03 | Gate | Plugin 启动失败不遗留 contribution 或资源 | Completed | Go Verified：LIFO rollback 与 composition occupied-listener rollback 后 Runtime 无 declaration |
| S2-G04 | Gate | listener 与 handler registration 由 effect 拥有且可撤销 | Completed | Go Verified：Event listener、Child Scope、System Prompt contribution、Tool/restriction/guard、Service 和 API Proxy Service scope 均有精确 disposer |
| S2-G05 | Gate | Excluded/Deferred 能力不进入 Catalog 或依赖闭包 | Completed | Go Verified：shipped Catalog 仅含 Agent/Default Model/Agent Loop、LLM/DeepSeek、Session、System Prompt、Native Tools、Host API Proxy 与 Connection Host half，显式拒绝 Web/SDK/Code Mode/ACP/MCP 等 factory |
| S2-G06 | Gate | `!!js`、未知字段、类型错误和无效组合严格失败 | Completed | Go Verified：generic、Connection、API Proxy、LLM/DeepSeek、System Prompt 与 Tools Factory config fixtures |

## 6. 阶段 3：Session/Agent 会话纵向切片

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S3-D01 | 交付 | in-memory append-only Session | Completed | Contract Verified：Header/Event、普通 append、seed marker 与 surface 已同固定源交叉验证；Go Verified：lossless snapshot、连续 `seq`、Store lifecycle/flush |
| S3-D02 | 交付 | System Prompt registry 与 snapshot assembly | Completed | Contract Verified：固定源与 Go 的 built-ins、global/scoped shadow、provider snapshot、suppression、complete、tool order、strict interpolation 与失败一致；Go Verified：typed config、change rollback、post-waterfall invariant 与 assembly detachment |
| S3-D03 | 交付 | Tool definition、registry、executor 和 policy waterfall | Completed | Contract Verified：固定源与 Go 的 Native config、scope shadow/restriction、pre/execute/post policy、result/failure、cancellation 和 finalizer 一致；Go Verified：typed behavior interface、schema cache、guard、detached snapshot、observer containment 与 System Prompt projection |
| S3-D04 | 交付 | Agent registry、inbox、scope 与实时事件 | Completed | Contract Verified：固定源与 Go 的 durable Inbox mutation/event/list/notification 顺序一致；Go Verified：Registry publication/rollback、runtime ownership、scoped event、initiator 与 model selection snapshot |
| S3-D05 | 交付 | 首个端到端 Agent Loop | Completed | Contract Verified：固定源与 Go 的 fresh Agent happy path 产生相同 19 个 ordered Session event、两次 request 和 derived messages；Go Verified：publication/teardown、maintenance wake、typed cancel cause、request retry 与 parallel Tool order；runtime-context projection 已实现但尚无独立跨语言 fixture |
| S3-D06 | 交付 | fake LLM Adapter 与 deterministic Tool | Completed | Go Verified：`NewSliceStream` scripted Adapter、echo Tool 和 blocking Tool 覆盖 completed、Tool continuation、retry、cancel/drain 与并发 ordering |
| S3-D07 | 交付 | 首期 `session.*` handler | Completed | Contract Verified：八个 method 的固定源 schema accept/reject vector 与原 `WebApiClient` E2E 已通过；Go Gateway 映射 Agent/Session/LLM/DefaultModel capability |
| S3-D08 | 交付 | Mux/Host frame 与 approval/question 闭环 | In Progress | Contract Verified：Session subscribed/event/queue 和 Host added/removed/status/error 已接入真实双流；approval/question requested/resolved/respond 尚未进入 |
| S3-D09 | 交付 | client cancel、disconnect 与 turn cancellation 映射 | Completed | Contract Verified：`session.cancel` 映射 `UserCancel + KeepInbox` 并产生 aborted turn；WebSocket disconnect 只取消其 stream source，不取消 Agent Turn |
| S3-G01 | Gate | TypeScript Connection 完成 Session prompt 并收到最终事件 | Completed | Contract Verified：固定源 `WebApiClient` 调用 Go `session.prompt`，收到 committed `turn/end` 与 idle status 并通过 history 读取结果 |
| S3-G02 | Gate | `user/message` 到 `turn/end` 全流程可运行 | Completed | Contract Verified：`TestPinnedSourceAgentLoopMatchesGo` 覆盖 prompt、request/header/context、chunks、Assistant、Tool call/result、第二 Step 和 completed Turn |
| S3-G03 | Gate | step、拒绝、取消、模型和 Tool failure 有 golden | In Progress | Go Verified：step、typed cancel/disposal race、request error retry 与 Tool cancel/drain 已覆盖；pre-step reject 和 Agent-level Tool failure 尚未进入跨语言 golden |
| S3-G04 | Gate | approval/question 通过 respond 形成闭环 | Planned | None |
| S3-G05 | Gate | 每个模型请求可由 Session 日志重建 | In Progress | Contract Verified：两步 Tool continuation 的 request 与固定源一致；Go Verified：header/context folds、surface derivation 和 replacement cache；resume/compaction theorem fixture 尚未完成 |
| S3-G06 | Gate | reconnect 读取 baseline 且不重复 committed event | Completed | Go Verified：Mux 先发送 subscribed/queue baseline，per-Session high-water 抑制 baseline 与迟到 callback 的重复 committed event；Host baseline 由 `session.list` 拥有 |
| S3-G07 | Gate | Agent Loop 不依赖 transport、driver、vendor 或 Deferred adapter | Completed | Go Verified：`agentloop` 只依赖 Agent、Session、System Prompt、Tools、LLM 与 Plugin capability；Echo、Connection、DeepSeek、persistence 和 SDK 不在依赖闭包 |

## 7. 阶段 4：LLM Contract 与 DeepSeek Provider

唯一 `llm` owner 已迁移到 Harness-compatible contract，旧 `Model`、`APIAdapter`、generic OpenAI SDK facade 和平行 factory 入口已删除。离线 fixed-source differential 不等同真实 DeepSeek credential/endpoint 验收；RetryPolicy contract 与 Agent request attempt loop 已完成，但默认 delay/jitter/attempt consumer 尚未实现。

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S4-D01 | 交付 | Harness-compatible Message、Content、ToolSchema、ContextSnapshotSection、StreamChunk、finish、usage 和 options | Completed | Contract Verified：固定源与 Go 的 wire message/request、chunk、assembly 和 core discriminant 一致；unknown extension 使用 opaque variant |
| S4-D02 | 交付 | LLM Adapter Registry 与 Runtime | Completed | Go Verified：route/directory/discovery、model resolution、prepared call、replacement、replay-state filter、waterfall 与 terminal normalization；`5fd5602` 进一步分离公共 value/codec、Runtime state 与 Adapter registration，未增加平行入口 |
| S4-D03 | 交付 | DeepSeek adapter | Completed | Contract Verified：direct chat-completions serialization、SSE translation、usage、finish 与默认 policy 同固定源一致；`5fd5602` 分离 Adapter 入口、model catalog、lazy stream、HTTP request/error、config resolve/codec 与双向映射职责 |
| S4-D04 | 交付 | 建立可注入 outbound transport、迁移调用者并删除旧入口 | Completed | Go Verified：assembly 已提供 `llm` 并注册 `deepseek-official`；旧 OpenAI SDK/factory/client/example 入口已移除 |
| S4-D05 | 交付 | retry、error classification、partial stream、usage 和 cancellation | In Progress | Go Verified：RetryPolicy、HTTP/provider/stream error、partial stream terminal、usage、idle timeout、cancel 和 Agent request-error retry attempt 已覆盖；默认 policy delay/jitter/attempt consumer 尚未实现 |
| S4-D06 | 交付 | fake stream 与录制响应 fixtures | In Progress | Go Verified：`NewSliceStream` 与 inline HTTP/SSE fake 已覆盖 deterministic stream；可复用录制响应 fixture 尚未建立 |
| S4-G01 | Gate | 从源 fixture 建立目标类型和 codec | Completed | Contract Verified：`TestPinnedSourceLLMDeepSeekMatchesGo` |
| S4-G02 | Gate | 在唯一 `llm` owner 内完成新 Runtime | Completed | Go Verified：没有平行 Harness LLM package |
| S4-G03 | Gate | 迁移 adapter、composition、example 和调用者 | Completed | Go Verified：DeepSeek Factory 已进入默认 composition，stale example 与旧调用入口已删除 |
| S4-G04 | Gate | 删除旧 `Model`/`APIAdapter` 重复入口 | Completed | Implemented：旧 public types、client/factory 与 OpenAI adapter tree 已删除 |
| S4-G05 | Gate | 迁移后运行 AST 命名审计 | Completed | Go Verified：`tests/architecture` 随 race suite 通过 |
| S4-G06 | Gate | TS/Go 双向 fixture 与全部 failure/cancel 场景通过 | Completed | Contract Verified：source differential 通过；Go 覆盖 HTTP classification、invalid credential、cancel、timeout、mid-stream read failure、malformed/truncated/empty stream |
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
| S6-D09 | 能力 | Attachment | Planned | None：Tools 只引入 image content 实际消费的稳定 `ImageAttachmentRef` metadata contract；upload/storage Service 尚未实现 |
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
| S8-D02 | 交付 | 跨语言 replay/differential suite | In Progress | Contract Verified：Connection/schema/client、Session core/API、System Prompt、Native Tools、Agent Inbox、Agent Loop happy path 与 LLM/DeepSeek differential slice 已建立；Session Client cancel/queue 已覆盖，Agent Loop reject/failure/resume replay 尚未完整覆盖 |
| S8-D03 | 交付 | 多平台 CI | Planned | None |
| S8-D04 | 交付 | race、fuzz、故障注入、泄漏和长时测试 | In Progress | Go Verified：race 与 Connection-owned WebSocket/source leak audit 已有证据；fuzz、故障注入和长时测试未完成 |
| S8-D05 | 交付 | dependency、license 和 NOTICE 清单 | In Progress | Implemented：Echo 准入已记录；完整发布清单未建立 |
| S8-D06 | 交付 | security threat review | Planned | None |
| S8-D07 | 交付 | 性能与资源预算 | Planned | None |
| S8-D08 | 交付 | 安装、升级、migration 和恢复说明 | Planned | None |
| S8-G01 | Gate | 每个 included surface 标明 P0/P1/P2/P3 | Planned | None |
| S8-G02 | Gate | Connection 与 Agent 关键路径达到计划层级 | Planned | None |
| S8-G03 | Gate | Linux、macOS、Windows 分别有支持证据 | Planned | None：当前只验证 `darwin/arm64` |
| S8-G04 | Gate | 全量 Go、格式和 contract suite 通过 | In Progress | Contract Verified：当前 Go checks 与显式 TypeScript contract suite 通过；全项目 parity suite 尚未完成 |
| S8-G05 | Gate | TypeScript Client 与 DeepSeek Provider 分别真实验收 | In Progress | Contract Verified：固定源 `WebApiClient` 已完成当前 included Session 会话、双流、queue 和 cancel；原 Web 产品、approval/question 与真实 DeepSeek Provider 环境尚未验收 |
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
| 慢客户端同步背压与 shutdown 解阻塞 | `TestWebSocketSlowClientBackpressuresSourceAndShutdownUnblocksWrite` |
| 重复连接/断开后的 source、socket 与 pump 回收 | `TestWebSocketRepeatedConnectDisconnectLeavesNoOwnedResources` |
| 固定源与 Go HTTP success/failure precedence | `TestPinnedSourceHTTPFailuresMatchGoHost` |
| Service waiting、动态撤回/恢复、失败回滚、替换和 shutdown | `plugin/runtime_test.go` |
| emit/parallel/serial/bail/waterfall mode | `plugin/events_test.go` |
| Child Scope 嵌套 ownership、提前释放与 inherited dependency | `plugin/scope_test.go` |
| scoped waterfall 的 global/ancestor/exact admission 与 sibling/descendant exclusion | `TestScopedWaterfallAdmitsGlobalAndAncestorListeners` |
| scoped emit 的 global/ancestor/exact admission 与 sibling/descendant exclusion | `TestScopedEmitAdmitsGlobalAndAncestorListeners` |
| strict typed config 与 Factory Catalog 边界 | `plugin/catalog_test.go`、`internal/assembly/assembly_test.go` |
| Connection Plugin 乱序依赖结算与真实 HTTP 服务 | `TestConnectionCompositionSettlesDependenciesAndServesHostDescribe` |
| composition bind failure 无 declaration/contribution 遗留 | `TestCompositionFailureRollsBackEarlierDeclarations` |
| Session payload snapshot、seed 连续性、surface 原子 replace 与负零拒绝 | `session/session_test.go` |
| Session create/append/flush/dispose、rollback、observer containment 与重入拒绝 | `session/store_test.go` |
| 固定源与 Go Session Header/Event/seed/surface 行为一致 | `TestPinnedSourceSessionCoreMatchesGo` |
| Session -> API Proxy -> `host.describe.attachedSessions` 实时 projection | `TestConnectionCompositionSettlesDependenciesAndServesHostDescribe` |
| 八个 `session.*` request union 的 strict decode 与负向组合 | `TestSessionRequestDecodersMatchIncludedUnionShapes`、`TestSessionRequestDecodersRejectNullAndInvalidCombinations` |
| history message-boundary pagination、durable queue fold 与宽 text extension 保真 | `TestHistoryPageCutsAtAppendMessageGroup`、`TestProjectQueueFoldsDurableSplicesAndUserPlacement`、`TestReplaceMessageContentPreservesLooseTextExtension` |
| Mux baseline high-water 与 frame ID failure 不丢 committed event | `TestMuxBaselineHighwaterSuppressesLateCommittedCallback`、`TestSessionFrameHubSurfacesMintFailureWithoutAdvancingHighwater` |
| live default 到显式选择的优先级与 prompt/request snapshot | `TestModelSelectionReadsLiveFallbackUntilExplicitlySelected`、`TestModelSelectionSnapshotsPromptAndRequestTogether` |
| 固定源 `WebApiClient` 调用 Go Session Gateway 完成 prompt/queue/cancel 双流会话 | `TestPinnedSourceSessionWebApiClientTalksToGoGateway` |
| System Prompt built-ins、scope shadow、snapshot、suppression、complete、tool order、插值与 invariant | `systemprompt/systemprompt_test.go` |
| 固定源与 Go System Prompt assembly/render/failure 行为一致 | `TestPinnedSourceSystemPromptMatchesGo` |
| Native Tool scope view、restriction/guard、schema cache、执行/取消、finalizer 与 detached result | `tools/runtime_test.go` |
| 固定源与 Go Native Tools config/visibility/policy/result/cancel 行为一致 | `TestPinnedSourceNativeToolsMatchesGo` |
| LLM/DeepSeek/System Prompt/Tools Factory strict config、shipped Catalog 与 Service composition | `internal/assembly/assembly_test.go` |
| core LLM content block variant clone 与 extension panic containment | `llm/harness_contract_test.go` |
| Harness Message/StreamChunk 严格 round-trip、opaque extension 与 provenance | `llm/message_test.go` |
| LLM route、prepared call、replacement、waterfall、replay state 与 terminal normalization | `llm/runtime_test.go` |
| RetryPolicy 默认值、tagged union、safe range 与 detached snapshot | `llm/retry_policy_test.go` |
| StreamChunk 增量组装、first-close 和 max-token tool-call 处理 | `llm/assembler_test.go` |
| DeepSeek typed config、环境优先级和 immutable snapshot | `internal/llmdeepseek/config_test.go` |
| DeepSeek message/request serialization 与 image/reasoning/stop 语义 | `internal/llmdeepseek/serialize_test.go` |
| DeepSeek SSE framing、translation、usage、finish、empty/malformed/timeout | `internal/llmdeepseek/stream_test.go` |
| DeepSeek HTTP headers、metadata、错误分类、credential、cancel 与中途失败 | `internal/llmdeepseek/adapter_test.go` |
| anonymous Harness user identity 的持久化与损坏恢复 | `internal/anonymoususerid/store_test.go` |
| 固定源与 Go 的 DeepSeek request、stream assembly 和 retry default 一致 | `TestPinnedSourceLLMDeepSeekMatchesGo` |
| Agent Registry publication、rollback、reentrant detach、ownership 与顺序 | `agent/registry_test.go` |
| Agent scoped emit/waterfall/serial 的 subject isolation | `agent/events_test.go` |
| durable Inbox replay、commit/notification 顺序、clear、duplicate 与并发 append | `agent/inbox_test.go` |
| initiator context 与 prompt/request model selection snapshot | `agent/initiator_test.go`、`agent/model_selection_test.go` |
| 固定源与 Go Inbox event、list、splice/claim/clear 和 notification 顺序一致 | `TestPinnedSourceAgentInboxMatchesGo` |
| Agent Loop fresh lifecycle、Turn/Step、Tool continuation、request reconstruction 与 teardown | `TestLoopPublishesDrivesAndDisposesOneAgentLifecycle` |
| bounded parallel Tool body 与 model-order result/context commit | `TestParallelBodiesCommitResultsAndContextsInModelOrder` |
| maintenance wake、`WhenIdle` successor convergence 与 first typed cancel cause | `TestMaintenanceLatchedWakeKeepsWhenIdleBehindSuccessorTurn`、`TestFirstCancelCauseSurvivesDisposalRace` |
| failed model attempt 通过 `agent/request-error` 在同一 Step retry | `TestRequestErrorRetryRepeatsAttemptInsideOneStep` |
| 固定源与 Go Agent Loop event、request 和 derived message projection 一致 | `TestPinnedSourceAgentLoopMatchesGo` |

## 13. 当前验证结果

本次 Session API Gateway 切片在 Go 1.26.6、`darwin/arm64` 执行并通过：

- `go test ./...`
- `go test -race ./agent ./apiproxy`
- `go vet ./...`
- `go test -tags=contract ./tests/contract -count=1`（固定源 commit `47f943...`）
- 变更文档本地链接检查
- `git diff --check`

迭代中只运行受影响的 API Proxy、Agent、Session、assembly 与 contract tests；最终执行一次普通全量测试，并对本次并发边界执行 targeted race，不为同一状态重复运行 `go build ./...`。此前 Agent Loop 的 full race、Connection WebSocket 压力测试与 `govulncheck` 仍是对应阶段证据，不冒充本次重复验证。

LLM 文件职责重组提交 `5fd5602` 不改变公共 contract 或运行行为。该提交先通过 `go test ./llm ./internal/llmdeepseek`；随后在只包含该提交的干净 worktree 中通过一次 `go test ./...` 和 `go vet ./...`。该重组没有新增并发语义，因此未重复 race、TypeScript differential 或真实 endpoint smoke。

先前真实进程 smoke 已证明 `cmd/goren` 可通过 Factory Catalog 与 Plugin Runtime 结算基础服务并响应 `host.describe`；本次未重复该 smoke。本次 contract test 直接组合真实 Registry/Session/LLM/SystemPrompt/Tools/AgentLoop/SessionGateway/Echo Host，让固定源 `WebApiClient` 通过真实 HTTP/WebSocket 完成 create/list/history/models/selectModel、prompt、queue edit/remove 和 cancel，并观察 `turn/end`、aborted turn 与 idle status。该证据仍不是原 Web 产品验收，也未使用真实 DeepSeek credential/endpoint。

## 14. 安全与依赖状态

- Echo 固定为 `github.com/labstack/echo/v5 v5.3.1`，`coder/websocket` 固定为 `v1.8.15`，准入记录见[04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)；
- 初次扫描在 Go 1.26.5 发现 6 个可达标准库漏洞；module 已提升到 Go 1.26.6 并复扫通过；
- 当前 listener 默认只绑定 `127.0.0.1:3080`；非 loopback deployment 的 TLS、认证和授权尚未进入范围；
- DeepSeek 配置只保存 `apiKeyEnv` reference，请求开始时才解析环境值；`.env`、credential 和 secret 未进入变更。

## 15. 下一实现切片

Agent Loop core、八个 Session method、live Mux/Host projection、queue mutation 和 client cancel 已完成。下一切片继续阶段 3 尚未闭合的交互和 golden：

1. 从固定源提取 approval/question 的 owner、requested/resolved frame、respond schema 和 replay 规则；
2. 复用 API Proxy pending correlation 建立 interaction 闭环，不把 approval/question 决策塞入 Agent Loop 或 Session Store；
3. 补 pre-step reject、Agent-level Tool failure 和模型失败的跨语言 golden；
4. 为 resume/compaction request reconstruction 建立 theorem fixture，但不在 persistence 进入前伪造 cold resume；
5. 按真实依赖决定 `session.search`、`rename`、`fork` 与 attachment method 的进入顺序。

Session persistence/resume 仍属于阶段 5：当前 fresh Agent Loop 不伪造 load/repair。默认 RetryPolicy 的 delay/jitter/attempt consumer 也沿 `agent/request-error` 作为独立 Plugin 进入，不回填 DeepSeek Adapter。Agent instance 继续消费既有 Child Scope 与 scoped listener isolation，不另建第二套 Registry。
