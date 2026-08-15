# 08 实施进度

状态：In Progress
更新时间：2026-08-15

本文是 DeepSeek Harness Go 复刻实施状态、验证证据、阻塞项和下一步的唯一记录。全局范围与 Gate 由[05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)拥有，当前 Web Agent 交付闭包由[21 Web Agent 主会话闭环与能力边界](./21-web-agent-main-flow.md)拥有；模块职责与设计分别由[06 Connection Host 模块设计与实现](./06-connection-host-module.md)、[07 API Proxy 模块设计与实现](./07-api-proxy-module.md)、[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)、[10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)、[11 System Prompt Registry 与 Assembly 模块设计](./11-system-prompt-registry-and-assembly.md)、[12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)、[13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)、[14 Agent Registry、Inbox 与实时事件模块设计](./14-agent-registry-inbox-and-events.md)、[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)、[16 Session API Gateway 与实时 Frame 投影](./16-session-api-gateway-and-live-frames.md)、[17 Approval、UserQuestions 与 Interaction Gateway](./17-approval-user-questions-and-interaction-gateway.md)、[18 Session Projection 与 Session Title 模块设计](./18-session-projection-and-title.md)、[19 Session Persistence 与 SQLite 事实存储设计](./19-session-persistence-and-sqlite.md)、[20 Workspace Registry、SQLite 与 API Gateway](./20-workspace-registry-and-api.md)和[22 Credentials 与 API Key 管理](./22-credentials-and-api-key-management.md)拥有；代码近邻说明由各领域模块 `README.zh-CN.md` 持有。本文不重新定义协议或架构。

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
| 阶段 0：基线与 Contract Freeze | In Progress | 12 Completed / 1 In Progress / 1 Planned | Contract Verified | 补齐其余 included surface mapping 与 NOTICE/provenance 决策 |
| 阶段 1：Connection Host Carrier | Completed | 19 Completed | Contract Verified | Gate 已完成，后续扩展随新增 included surface 进入 |
| 阶段 2：Plugin Runtime | Completed | 12 Completed | Go Verified | Gate 已完成；后续能力通过既有 Factory/Service/Event seam 进入 |
| 阶段 3：Session/Agent slice | Completed | 16 Completed | Contract Verified | 全部交付与 Gate 已闭合；cold persistence/resume 仍由阶段 5 拥有 |
| 阶段 4：LLM Contract | Completed | 13 Completed | Environment Verified | Runtime、DeepSeek adapter、response recordings、Agent attempt loop、默认 retry Consumer 与真实 Provider smoke 均已完成 |
| 阶段 5：Session 持久化 | In Progress | 15 Completed / 2 Planned | Contract Verified | SQLite facts、cold recovery/API/Agent resume、turn-end checkpoint 与默认装配主流程已闭环；剩余读取优化 |
| 阶段 6：客户端能力扩展 | Deferred | 19 Completed / 21 Deferred | Contract Verified | 主会话 UI、Question 回答与 DeepSeek API Key 设置已完成；其余页面和管理能力保持冻结 |
| 阶段 7：Deferred 能力 | Deferred | 7 Deferred | None | 不创建 package、handler 或依赖占位 |
| 阶段 8：Parity Hardening | In Progress | 1 Completed / 4 In Progress / 10 Planned | Environment Verified | 默认 UI/Provider 主流程已有环境证据，完整发布验收仍未完成 |

当前 Connection slice、Credentials、Session core、System Prompt、Native Tools、Agent Inbox、Agent Loop、Session API Gateway、Session Projection、Session Title/Rename、Session Persistence/SQLite、Workspace Registry/API、Approval/UserQuestions/Interaction Gateway 与 LLM/DeepSeek contract 达到 `Contract Verified`：固定 TypeScript schema 与 Go envelope/frame 已交叉校验，固定上游 `WebApiClient` 已通过真实 Go HTTP/WebSocket 完成 credential describe/set/unset 与主会话 create/list/history/model/prompt、`turn/end`、queue、cancel、rename、respond、reconnect；SQLite 重启后 cold list/history/create(resume) 也已有 Go E2E。默认 `DefaultSpecs` 已内嵌主会话 UI 和 local Credentials Store，并经离线 DeepSeek oracle 与真实 DeepSeek endpoint 完成对话、会话切换和历史恢复。正常 Agent 轮次在 committed `turn/end` 后等待 `session.Store.Flush`，再进入 successor/idle convergence；原版完整 Web 产品、Settings Provider、Agent Preset composition 与其他页面能力不属于当前闭包。

### 2.1 当前 Web Agent 主流程状态矩阵

| ID | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- |
| WAF-01 | 建立 HTTP 与双 WebSocket connection generation | Completed | Contract Verified：固定 `WebApiClient` 和 `ConnectionController` 已连接真实 Go Host |
| WAF-02 | 读取 Session/Workspace baseline | Completed | Contract Verified：`session.list`、`workspace.list` 与 subscribed/status baseline 已覆盖 |
| WAF-03 | 使用 `cwd` 或 `workspaceId` 创建 Agent-backed Session | Completed | Contract Verified：固定客户端已调用 Go Gateway，两种创建入口及冲突输入均已覆盖 |
| WAF-04 | 读取当前和 cold Session history | Completed | Contract Verified：当前 history 由固定客户端读取；Go E2E 覆盖 SQLite 重启后 history |
| WAF-05 | 枚举并选择可路由模型 | Completed | Contract Verified：`session.models`、`session.selectModel` 与请求 snapshot 已覆盖 |
| WAF-06 | 文本 prompt 驱动真实 Agent Loop 到 `turn/end` | Completed | Contract Verified：固定客户端经真实 HTTP/WebSocket 驱动 Go Agent Loop；当前 fixture 使用 deterministic fake LLM |
| WAF-07 | Mux/Host 实时投影 committed event、running 与 error | Completed | Contract Verified：`session/event`、`session/subscribed`、`host/session-status` 与 reconnect high-water 已覆盖 |
| WAF-08 | 查看并修改 turn 内输入队列 | Completed | Contract Verified：queue baseline、edit/remove 与 durable fold 已覆盖 |
| WAF-09 | 取消运行中轮次 | Completed | Contract Verified：`session.cancel` 产生 aborted turn，socket disconnect 不误取消 Agent |
| WAF-10 | Approval/Question 经 `/api/respond` 结算 | Completed | Contract Verified：requested、respond、resolved、replay 与错误响应恢复已覆盖 |
| WAF-11 | rename 并投影稳定标题 | Completed | Contract Verified：固定客户端、`Session.rename()` 与 higher-seq-wins store 已覆盖 |
| WAF-12 | SQLite 保存并恢复完整会话事实 | Completed | Go Verified：cold list/history/resume 已通过；正常 `turn/end` 显式触发 durability checkpoint，并在 Agent idle 前从 SQLite storage-only Backend 直接读到完整边界 |
| WAF-UI01 | 默认服务内嵌主会话 UI | Completed | Go Verified：React/Vite/Tailwind 生产构建、content-hashed embedded assets、HTML revalidation、immutable asset cache、SPA fallback 和 Connection `http.Handler` delegation 已覆盖 |
| WAF-UI02 | UI 完成发送、会话创建/选择和历史恢复 | Completed | Contract Verified：`web-ui-main-flow.ts` 加载真实内嵌页面，完成 prompt、流式终态、新建 Session、切回与 history |
| WAF-UI03 | UI 回答 Question 并继续同一 Agent Turn | Completed | Contract Verified：真实内嵌页面保留 requested `rpcId`，经 `/api/respond` 提交选项、收到 resolved、驱动 Tool result continuation 到最终 assistant message；等待期间普通 composer 禁用，plugin runtime-context 不显示为用户消息 |
| WAF-C01 | Credentials Manager/local Store、Host write-only API 与 Web API Key 设置 | Completed | Contract Verified：固定源 `WebApiClient.credentials` 调用真实 Go Host 完成 describe/set/unset；Manager precedence、owner-only JSON Store 与 value-free response 已覆盖；React dialog 已通过 TypeScript/生产构建，尚未声明交互式浏览器验收 |
| WAF-A01 | 固定客户端经默认 composition 与 DeepSeek Adapter 完成轮次 | Completed | Contract Verified：默认 `DefaultSpecs`、真实 HTTP/WebSocket、DeepSeek Adapter 和离线 HTTP oracle 在同一进程到达 `turn/end` |
| WAF-A02 | 使用真实 DeepSeek credential/endpoint 独立 smoke | Completed | Environment Verified：显式加载本地 `.env` 后，内嵌 UI 经真实 `https://api.deepseek.com` 完成 prompt、终态、会话切换与历史恢复；credential 未输出或提交 |

## 3. 阶段 0：基线与 Connection Contract Freeze

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S0-D01 | 交付 | 固定源 commit、版本、许可证和本地参考路径 | Completed | Implemented：`01` 已固定 `47f943...`、`0.1.0-rc.5` 和 `../deepseek-harness` |
| S0-D02 | 交付 | 建立 included/excluded surface manifest | Completed | Contract Verified：`contracts/deepseek-harness/manifest.json` 固定源、当前 surface、Excluded/Deferred 与 privileged method |
| S0-D03 | 交付 | 提取 RPC message、result、receipt、frame、path 和 stable errors | Completed | Go Verified：RPC、path、stable errors、窄 stream request 与完整 Mux/Host frame union 已覆盖 |
| S0-D04 | 交付 | 提取首期 Host、Session、approval/question 和 respond contract | Completed | Contract Verified：`host.describe`、Session method/frame、Approval/Question response/frame、respond receipt 与固定源 `WebApiClient` interaction E2E 已覆盖 |
| S0-D05 | 交付 | 建立 contract manifest 和可重复 TypeScript fixture generator | Completed | Contract Verified：固定源 Zod schema 可重复生成 `vectors.json` |
| S0-D06 | 交付 | 单列完整 Web Client 所需但首期未纳入的能力 | Completed | Implemented：`01` 已记录初始差异；Workspace 后续已进入阶段 6，Settings、Goals 等仍保持 Deferred/Planned |
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

该链路同时被已完成的 Approval/Question Interaction Gateway 消费：具体 schema、requested/resolved frame、replay 和业务结果由[17](./17-approval-user-questions-and-interaction-gateway.md)拥有，Catalog 仍只拥有通用 correlation。

## 5. 阶段 2：Plugin Runtime 与 Server Assembly

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S2-D01 | 交付 | Plugin、Factory、Manifest、Scope、Disposer 和 typed keys | Completed | Go Verified：`plugin` 提供静态 interface、泛型自由函数、opaque Child Scope/lineage 与 LIFO ownership |
| S2-D02 | 交付 | Service graph、typed Event modes、rollback、replacement 和 shutdown | Completed | Go Verified：waiting settlement、live withdraw/re-provide、五种 Event mode、scoped waterfall、shadow replacement 与 dependent-first shutdown 已覆盖；callback 保留精确泛型类型 |
| S2-D03 | 交付 | Factory Catalog 与静态 composition root | Completed | Go Verified：`cmd/goren -> internal/assembly -> Catalog -> Runtime`，入口不再直接拼装 Echo/API Proxy |
| S2-D04 | 交付 | 首期 typed config 与 strict validation | Completed | Go Verified：owner config 拒绝 duplicate/unknown/type/range/combination 与多值输入，类型擦除止于 Factory Catalog；System Prompt 和 Tools 保留各自的 omitted/empty/null/default 语义 |
| S2-D06 | 交付 | lifecycle diagnostics 和 leak-oriented tests | Completed | Go Verified：`PluginStatus` 暴露状态/effect/error；rollback、unload、replacement、shutdown 后 effect/contribution 清空 |
| S2-G01 | Gate | Service 缺失、重复、启动失败、卸载和替换有测试 | Completed | Go Verified：`plugin/runtime_test.go` |
| S2-G02 | Gate | Event modes 的顺序与错误语义有 fixture | Completed | Go Verified：`plugin/events_test.go` 覆盖 emit/parallel/serial/bail/waterfall 及 global/ancestor/exact scope admission |
| S2-G03 | Gate | Plugin 启动失败不遗留 contribution 或资源 | Completed | Go Verified：LIFO rollback 与 composition occupied-listener rollback 后 Runtime 无 declaration |
| S2-G04 | Gate | listener 与 handler registration 由 effect 拥有且可撤销 | Completed | Go Verified：Event listener、Child Scope、System Prompt contribution、Tool/restriction/guard、Service 和 API Proxy Service scope 均有精确 disposer |
| S2-G05 | Gate | Excluded/Deferred 能力不进入 Catalog 或依赖闭包 | Completed | Go Verified：shipped Catalog 只新增自有 `@gorenx/dsh-web`；SQLite adapter 没有独立 Factory/Service，源 Web/client runtime、SDK、Code Mode、ACP/MCP factory 仍未注册 |
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
| S3-D08 | 交付 | Mux/Host frame 与 approval/question 闭环 | Completed | Contract Verified：Session/Host live frame 及 Approval/Question requested、resolved、respond、bad-response retry、取消和 stable reconnect replay 已接入真实双流 |
| S3-D09 | 交付 | client cancel、disconnect 与 turn cancellation 映射 | Completed | Contract Verified：`session.cancel` 映射 `UserCancel + KeepInbox` 并产生 aborted turn；WebSocket disconnect 只取消其 stream source，不取消 Agent Turn |
| S3-G01 | Gate | TypeScript Connection 完成 Session prompt 并收到最终事件 | Completed | Contract Verified：固定源 `WebApiClient` 调用 Go `session.prompt`，收到 committed `turn/end` 与 idle status 并通过 history 读取结果 |
| S3-G02 | Gate | `user/message` 到 `turn/end` 全流程可运行 | Completed | Contract Verified：`TestPinnedSourceAgentLoopMatchesGo` 覆盖 prompt、request/header/context、chunks、Assistant、Tool call/result、第二 Step 和 completed Turn |
| S3-G03 | Gate | step、拒绝、取消、模型和 Tool failure 有 golden | Completed | Contract Verified：固定源与 Go 的 pre-step blocked turn、模型 structured terminal failure 和 Agent-level scheduler failure event sequence 一致；Go race 验证 failure 停止补充、drain 已启动 body 且不伪造 Tool result |
| S3-G04 | Gate | approval/question 通过 respond 形成闭环 | Completed | Contract Verified：固定源 `WebApiClient` 通过真实 HTTP/WebSocket 回答 Go Approval/Question；accepted、bad-response、not-pending、client cancellation 与 replay 已验证 |
| S3-G05 | Gate | 每个模型请求可由 Session 日志重建 | Completed | Contract Verified：固定源与 Go 的 dispatch 前缀都经全新 Session 重建为实际 request；覆盖初始 generation、surface replacement summary、新 Loop seeded generation、`resume` header、system/config change 与 request/context 去重；既有 Tool continuation fixture 覆盖同 Turn 多 Step |
| S3-G06 | Gate | reconnect 读取 baseline 且不重复 committed event | Completed | Go Verified：Mux 先发送 subscribed/queue baseline，per-Session high-water 抑制 baseline 与迟到 callback 的重复 committed event；Host baseline 由 `session.list` 拥有 |
| S3-G07 | Gate | Agent Loop 不依赖 transport、driver、vendor 或 Deferred adapter | Completed | Go Verified：`agentloop` 只依赖 Agent、Session、System Prompt、Tools、LLM 与 Plugin capability；Echo、Connection、DeepSeek、persistence 和 SDK 不在依赖闭包 |

## 7. 阶段 4：LLM Contract 与 DeepSeek Provider

唯一 `llm` owner 已迁移到 Harness-compatible contract，旧 `Model`、`APIAdapter`、generic OpenAI SDK facade 和平行 factory 入口已删除。独立 `llmretry` Plugin 已沿 `agent/request-error` 执行 Provider-owned RetryPolicy，并把 retry action 交回 Agent Loop；离线 fixed-source differential 与真实 DeepSeek credential/endpoint 已分别验收。

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S4-D01 | 交付 | Harness-compatible Message、Content、ToolSchema、ContextSnapshotSection、StreamChunk、finish、usage 和 options | Completed | Contract Verified：固定源与 Go 的 wire message/request、chunk、assembly 和 core discriminant 一致；unknown extension 使用 opaque variant |
| S4-D02 | 交付 | LLM Adapter Registry 与 Runtime | Completed | Go Verified：route/directory/discovery、model resolution、prepared call、replacement、replay-state filter、waterfall 与 terminal normalization；`5fd5602` 进一步分离公共 value/codec、Runtime state 与 Adapter registration，未增加平行入口 |
| S4-D03 | 交付 | DeepSeek adapter | Completed | Contract Verified：direct chat-completions serialization、SSE translation、usage、finish 与默认 policy 同固定源一致；`5fd5602` 分离 Adapter 入口、model catalog、lazy stream、HTTP request/error、config resolve/codec 与双向映射职责 |
| S4-D04 | 交付 | 建立可注入 outbound transport、迁移调用者并删除旧入口 | Completed | Go Verified：assembly 已提供 `llm` 并注册 `deepseek-official`；旧 OpenAI SDK/factory/client/example 入口已移除 |
| S4-D05 | 交付 | retry、error classification、partial stream、usage 和 cancellation | Completed | Contract Verified：RetryPolicy、HTTP/provider/stream error、partial stream terminal、usage、idle timeout、cancel 和 Agent request-error attempt 已覆盖；`a9189fb` 增加独立 `llmretry` Consumer、durable schedule/start、history invariant、policy budget、Retry-After/backoff、取消/drain 及固定源差分 |
| S4-D06 | 交付 | fake stream 与录制响应 fixtures | Completed | Go Verified：`NewSliceStream` 提供 deterministic fake；`5731251` 把完整 SSE、结构化 429 和 partial transport failure 抽为 package-local 原始 HTTP recordings，并由既有 Adapter 端到端断言直接重放 |
| S4-G01 | Gate | 从源 fixture 建立目标类型和 codec | Completed | Contract Verified：`TestPinnedSourceLLMDeepSeekMatchesGo` |
| S4-G02 | Gate | 在唯一 `llm` owner 内完成新 Runtime | Completed | Go Verified：没有平行 Harness LLM package |
| S4-G03 | Gate | 迁移 adapter、composition、example 和调用者 | Completed | Go Verified：DeepSeek Factory 已进入默认 composition，stale example 与旧调用入口已删除 |
| S4-G04 | Gate | 删除旧 `Model`/`APIAdapter` 重复入口 | Completed | Implemented：旧 public types、client/factory 与 OpenAI adapter tree 已删除 |
| S4-G05 | Gate | 迁移后运行 AST 命名审计 | Completed | Go Verified：`tests/architecture` 随 race suite 通过 |
| S4-G06 | Gate | TS/Go 双向 fixture 与全部 failure/cancel 场景通过 | Completed | Contract Verified：source differential 通过；Go 覆盖 HTTP classification、invalid credential、cancel、timeout、mid-stream read failure、malformed/truncated/empty stream |
| S4-G07 | Gate | 真实 DeepSeek smoke 独立验收 | Completed | Environment Verified：默认服务加载本地 `.env` credential reference，内嵌 UI 经真实 DeepSeek endpoint 完成一个无 Tool 文本轮次；secret 未进入输出、fixture 或提交 |

## 8. 阶段 5：Session 持久化与重连恢复

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S5-D01 | 交付 | backend-neutral `Persistence`、`SessionLogStore` 与 storage-only `Backend` | Completed | Go Verified：有状态 `SessionLogStore` 实现完整服务、per-ID gate 与 reservation；SQLite adapter 只实现存储 port |
| S5-D02 | 交付 | SQLite/sqlc Session fact Backend | Completed | Go Verified：schema/query/config/generated code 位于 `session/persistence/sqlite`，默认使用 `modernc.org/sqlite` |
| S5-D03 | 交付 | write-behind、显式 flush、dispose/shutdown drain | Completed | Go Verified：失败 batch 保留、取消等待与最终 drain 由 `write_behind_test.go` 覆盖 |
| S5-D04 | 交付 | cold validation、torn-tail 与开放 Turn/Step/Tool recovery | Completed | Go Verified：`SessionLogStore` 与 recovery tests 覆盖 repair planning/commit |
| S5-D05 | 交付 | cold `session.list/history/create(resume)` 与 Agent publication handoff | Completed | Contract Verified：默认 composition 重启 E2E 完成 list、history 和 exact Session resume |
| S5-D06 | 交付 | reconnect baseline、pending replay 和 queue snapshot | Completed | Contract Verified：subscribed/high-water、stable interaction replay 与 queue snapshot 已覆盖；live callback 不作为 durable fact |
| S5-D07 | 优化 | source-style bounded prepared cache 与 revision reuse | Planned | None：当前 reservation 保证唯一 unpublished Session，但 cold inspect/prepare 仍可重复读取 |
| S5-D08 | 优化 | `ReadFrom` 使用 Backend seek 而非完整 prefix scan | Planned | None：公开后缀语义已实现，尚未消费 `LoadStoredFrom` 快路径 |
| S5-D09 | 交付 | request/turn 完成边界的显式 durability checkpoint | Completed | Go Verified：`TestTurnEndAwaitsSessionDurabilityBeforeAgentBecomesIdle` 将 write-behind 延迟设为一小时，证明 Agent 在 committed `turn/end` 后调用 `session.Store.Flush`、SQLite 已可直接读取完整边界，且 flush 结束前不进入 idle |
| S5-G01 | Gate | Header/首批 Event/seq/revision transaction 原子 | Completed | Go Verified：`TestAdapterMaterializesHeaderAndBatchAtomically` |
| S5-G02 | Gate | torn tail、未知事件和开放状态 repair 不变量 | Completed | Go Verified：`TestAdapterMarksAndRepairsOnlyATornTail`、`SessionLogStore`/recovery tests |
| S5-G03 | Gate | 背景写失败与取消不丢 batch、不伪成功 | Completed | Go Verified：`TestLiveWriterRetainsFailedBackgroundBatchForExplicitFlush`、`TestLiveWriterFlushCanCancelWhileBackgroundWriteRemainsOwned` |
| S5-G04 | Gate | 进程重启后 cold list/history/resume | Completed | Go Verified：`TestDefaultCompositionListsHistoryAndResumesAColdSQLiteSession` |
| S5-G05 | Gate | 固定 TS Client 与 SQLite-backed Go Gateway contract | Completed | Contract Verified：contract suite 使用 in-memory SQLite composition |
| S5-G06 | Gate | sqlc generation 可重复且配置位于 owner 目录 | Completed | Go Verified：`sqlc generate -f session/persistence/sqlite/sqlc.yaml` 后生成结果无差异 |
| S5-G07 | Gate | sqlc/driver 类型不泄漏到 Session/Application contract | Completed | Go Verified：生成包仅被 SQLite adapter 导入，全仓 build/test 通过 |
| S5-A01 | 验收 | 固定源 `WebApiClient` 经默认 composition 完成主聊天闭环 | Completed | Contract Verified：默认 `DefaultSpecs` + DeepSeek Adapter + 离线 HTTP oracle 同进程完成 create、prompt、`turn/end`、idle 与 history；极简 UI 在同一测试中完成选择与历史恢复 |

## 9. 阶段 6：按客户端需求扩展服务端能力

本阶段保留历史完成项，但当前目标已冻结为[21](./21-web-agent-main-flow.md)的主会话闭包。未开始项统一 `Deferred`，不再因完整 Web 产品存在调用点而进入实现。

| ID | 类别 | 子目标 | 执行状态 | 证据或缺口 |
| --- | --- | --- | --- | --- |
| S6-W01 | Workspace | Registry、Entity、canonical path、order/archive 与 Session accounting | Completed | Go Verified：`workspace` 领域实现及 `TestRegistryPersistsCanonicalWorkspacesAndSessionAccounting` |
| S6-W02 | Workspace | 历史 Session Header bootstrap 与 initialized-empty 语义 | Completed | Go Verified：`TestRegistryBootstrapMatchesHistoricalSessionAndWorkspaceOrder`、`TestRegistryDoesNotReadHeadersAfterInitializedEmptyBootstrap` |
| S6-W03 | Workspace | SQLite/sqlc storage-only Backend 与独立数据库 | Completed | Go Verified：schema/query/sqlc 配置位于 `workspace/persistence/sqlite`；reopen、order/archive 和 transaction 由 Registry tests 覆盖 |
| S6-W04 | Workspace | 七个 `workspace.*` method、typed decoder 与稳定业务错误 | Completed | Contract Verified：固定源 `WebApiClient.workspace` 直接调用 Go Gateway |
| S6-W05 | Workspace | 四个 Host frame 与 `workspace.list` reconnect baseline | Completed | Contract Verified：Workspace client store 收到 change/remove/order/archive，list 返回完整 baseline |
| S6-W06 | Workspace | `session.create({workspaceId})` 与 cwd/accounting 集成 | Completed | Contract Verified：固定源 Client 验证 canonical cwd、attach 及 `workspaceId + cwd` 拒绝 |
| S6-W07 | Workspace | 并发 mutation、post-commit publication 与失败边界 | Completed | Go Verified：`TestRegistrySerializesConcurrentSessionAccountingAtCommit` 及 Registry failure cases |
| S6-W08 | Workspace | 能力插件与 SQLite adapter 装配边界 | Completed | Go Verified：Catalog 只注册 `@deepseek-ai/dsh-workspace`；SQLite 无 Factory、Manifest、Service key；`af33afd` |
| S6-WEB01 | Web UI | 内嵌主会话页面、Session 选择/history、prompt 与实时回复 | Completed | Contract Verified：`@gorenx/dsh-web` 默认装配 React/Vite/Tailwind 构建的 `web.Site`；JSDOM 对真实 Host 完成发送、新建、切换和历史恢复；未引入源 Client plugin runtime |
| S6-WEB02 | Web UI | DeepSeek API Key 首次设置、替换、删除与环境只读提示 | Completed | Implemented：`CredentialDialog` 只持有未提交 draft，通过 `credentials.*` API 读 metadata/单向写值；TypeScript 检查和生产构建通过，交互式浏览器验收仍归阶段 8 |
| S6-WEB03 | Web UI | `ask_user_question` 的 requested/respond/resolved 与 Turn continuation | Completed | Contract Verified：QuestionCard 支持 option、multi-select、custom 与取消；default composition oracle 验证浏览器回答形成 Tool message，并继续 DeepSeek 请求到最终输出 |
| S6-L01 | LLM Catalog | `llm.providers` 合并 configurable directory 与 active route | Completed | Contract Verified：声明顺序、active/dormant、undeclared active route 和 `declared` omission 由固定源 `WebApiClient` 验证 |
| S6-L02 | LLM Catalog | `llm.models` Host catalog 与 `session.models` 共享投影 | Completed | Contract Verified：provider-local failure containment、reasoning metadata 与固定源 response schema 通过；目录逻辑统一在 `LLMGateway.Catalog` |
| S6-P01 | Agent Preset | `agentPreset.list` absent-roster 合法部署分支 | Completed | Contract Verified：默认组合无 `AgentPresetRoster` 时返回 non-nil empty presets、`authorable:false`、`hasDocument:false`，固定源 `WebApiClient` 接受 |
| S6-P02 | Agent Preset | Preset discovery、Agent child-scope composition、select 与 authoring | Deferred | 当前主会话不依赖；不伪造源内置 preset，`session.create.agentPreset` 仅保留持久化 identity/冲突 contract |
| S6-S01 | Settings | `settings.describe` absent-provider 协议分支 | Completed | Contract Verified：method 注册且固定源 `WebApiClient` 接受 canonical `internal` absent-service business failure；未以 404 或空 success 冒充 Provider |
| S6-D02 | 能力 | Filesystem | Deferred | 当前主会话不依赖 |
| S6-D03 | 能力 | Shell | Deferred | 当前主会话不依赖 |
| S6-D04 | 能力 | PTY | Deferred | 当前主会话不依赖 |
| S6-D05 | 能力 | LSP | Deferred | 当前主会话不依赖 |
| S6-D06 | 能力 | Sandbox | Deferred | 当前主会话不依赖 |
| S6-D07 | 能力 | Guard | Deferred | 当前主会话不依赖 |
| S6-D08 | Credentials | Provider/Manager/Store、local owner-only JSON、DeepSeek 请求时解析与 Host API | Completed | Contract Verified：`credentialsFactory` 提供能力，local 只实现 `Store`；环境优先且只读，`credentials.describe` 不含值，固定源 Client differential 已通过 |
| S6-D09 | 能力 | Attachment | Deferred | 当前主会话不依赖；Tools 只保留已被 image content 消费的稳定 `ImageAttachmentRef` metadata contract |
| S6-D10 | 能力 | Spill | Deferred | 当前主会话不依赖 |
| S6-D11 | 能力 | Settings Provider、typed namespace、file persistence 与 mutation API | Deferred | 当前只有既有 API absent-provider 分支；完整 Web profile onboarding 不作为主流程依赖 |
| S6-D12 | 能力 | Goals | Deferred | 当前主会话不依赖 |
| S6-D13 | 能力 | Session Projection Framework | Completed | Contract Verified：通用 Unit/Registry、live fold、whole-value change、versioned checkpoint/restore、list/history baseline 与 `session/projection` frame 已实现；固定源 schema 与客户端 projection store 通过 |
| S6-D14 | 能力 | Session Title Core 与 `session.rename` | Completed | Contract Verified：fallback、规范化、source union、user pin/refresh、Provider 调度 seam、`title` projection、`title-invalid`、九个 Session method 与固定源 `WebApiClient`/`Session.rename()` E2E 已通过 |
| S6-D15 | 能力 | Session Title First-Prompt LLM Provider | Deferred | core 已提供 `AutomaticFirstPrompt` Provider seam；当前主会话只要求稳定 fallback/rename |
| S6-D16 | 能力 | Session Title All-Prompts LLM Provider | Deferred | core 已提供 `AutomaticAllPrompts` Provider seam；当前主会话只要求稳定 fallback/rename |
| S6-D17 | 能力 | Session Query/Search | Deferred | 当前主会话不依赖 |
| S6-D18 | 能力 | Session Fork | Deferred | 当前主会话不依赖 |
| S6-D19 | 能力 | 其他经 capability matrix 纳入的服务端能力 | Deferred | 当前主会话不依赖 |
| S6-G01 | Gate | 确认真实 Consumer 后才进入实现 | Deferred | 扩大当前目标时重新启用 |
| S6-G02 | Gate | 保留源 Definition、Provider 和 Consumer owner | Deferred | 扩大当前目标时重新启用 |
| S6-G03 | Gate | effect-time enforcement 归 permission/guard/sandbox owner | Deferred | 扩大当前目标时重新启用 |
| S6-G04 | Gate | 覆盖 success、failure、cancel、shutdown 和平台限制 | Deferred | 扩大当前目标时重新启用 |
| S6-G05 | Gate | credential 不进入日志、Session、错误或 fixture | Completed | Contract Verified：Host response 只含 metadata，启动输出只含路径/ref；测试使用临时假值并断言 `describe` 不回显 |
| S6-G06 | Gate | 首次实现前完成技术依赖准入 | Deferred | 扩大当前目标时重新启用 |

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
| S8-D02 | 交付 | 跨语言 replay/differential suite | In Progress | Contract Verified：Connection/schema/client、Host LLM Catalog、absent Agent Preset/Settings 分支、Session core/API、Approval/Question interaction、System Prompt、Native Tools、Agent Inbox、Agent Loop happy/failure/reconstruction、LLM/DeepSeek 与 LLM Retry differential slice 已建立；其余 included capability 仍按矩阵进入 |
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
| S8-G05 | Gate | TypeScript Host Client 与 DeepSeek Provider 分别验收 | Completed | Environment Verified：固定源 `WebApiClient`、默认 composition/离线 oracle 和真实 DeepSeek Provider 分层通过；极简 UI 主流程已验收，原版完整 Web 产品不在当前 Gate |
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
| Approval policy、paired audit、scope answerer、取消和 fail-closed | `approval/service_test.go` |
| UserQuestions Provider 生命周期、typed validation 与 exact root Agent attestation | `userquestions/service_test.go` |
| `ask_user_question` schema、answer projection、typed error 与 disposer | `toolaskuser/tool_test.go` |
| Interaction requested/respond/resolved、坏响应重试、并行 audit matching、reconnect replay 与 shutdown | `apiproxy/interaction_gateway_test.go` |
| `/api/respond` accepted/技术失败、privileged loopback 与 Echo Recover | `internal/connection/http_test.go` |
| 命名规则与审计器自测 | `tests/architecture/naming_test.go` |
| manifest 与 Go path/message/frame surface 一致 | `TestPinnedManifestMatchesGoSurface` |
| Go envelope/frame 与固定 TypeScript schema 向量一致 | `TestGoAgreesWithPinnedSourceVectors` |
| 固定源 schema 可重复生成 committed fixture | `TestPinnedSourceGeneratesCommittedVectors` |
| 固定源 `WebApiClient` 调用 Go HTTP/WS/respond | `TestPinnedSourceWebApiClientTalksToGoHost` |
| 固定源 Approval/UserQuestions/Ask Tool 行为与 Go 一致 | `TestPinnedSourceInteractionsMatchGo` |
| 固定源 `WebApiClient` 通过真实 HTTP/WS 回答 Go Approval/Question | `TestPinnedSourceWebApiClientAnswersGoInteractions` |
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
| 内嵌 Web 内容哈希、cache policy、SPA fallback 与 API route 隔离 | `web/site_test.go`、`TestFrontendHandlesOnlyUnownedBrowserRoutes` |
| 默认 composition 经 DeepSeek Adapter 完成固定 Client、UI 会话与 Question continuation | `TestDefaultCompositionServesFixedTypeScriptClientThroughDeepSeekAdapter`、`web-ui-main-flow.ts` |
| CLI `--data-dir` 默认数据库解析与具体路径覆盖 | `cmd/goren/main_test.go` |
| composition bind failure 无 declaration/contribution 遗留 | `TestCompositionFailureRollsBackEarlierDeclarations` |
| Session payload snapshot、seed 连续性、surface 原子 replace 与负零拒绝 | `session/session_test.go` |
| Session create/append/flush/dispose、rollback、observer containment、重入拒绝与 publication 后 follow-up append | `session/store_test.go`、`session/title/service_test.go` |
| 固定源与 Go Session Header/Event/seed/surface 行为一致 | `TestPinnedSourceSessionCoreMatchesGo` |
| Session -> API Proxy -> `host.describe.attachedSessions` 实时 projection | `TestConnectionCompositionSettlesDependenciesAndServesHostDescribe` |
| 九个 `session.*` request union 的 strict decode 与负向组合 | `TestSessionRequestDecodersMatchIncludedUnionShapes`、`TestSessionRequestDecodersRejectNullAndInvalidCombinations`、固定源 rename vectors |
| history message-boundary pagination、durable queue fold 与宽 text extension 保真 | `TestHistoryPageCutsAtAppendMessageGroup`、`TestProjectQueueFoldsDurableSplicesAndUserPlacement`、`TestReplaceMessageContentPreservesLooseTextExtension` |
| Mux baseline high-water 与 frame ID failure 不丢 committed event | `TestMuxBaselineHighwaterSuppressesLateCommittedCallback`、`TestSessionFrameHubSurfacesMintFailureWithoutAdvancingHighwater` |
| live default 到显式选择的优先级与 prompt/request snapshot | `TestModelSelectionReadsLiveFallbackUntilExplicitlySelected`、`TestModelSelectionSnapshotsPromptAndRequestTogether` |
| 固定源 `WebApiClient` 调用 Go Session Gateway 完成 prompt/queue/cancel 双流会话 | `TestPinnedSourceSessionWebApiClientTalksToGoGateway` |
| Session Projection late fold、whole-value change、registration refcount 与 versioned checkpoint/restore | `session/projection/registry_test.go` |
| Session Title fallback、规范化、rename、projection、Provider supersession、refresh、JSON array wire 与 drain | `session/title/service_test.go` |
| `SessionLogStore` 的 cold repair、exact preparation reservation 与 live write handoff | `session/persistence/session_log_store_test.go`、`recovery_test.go` |
| SQLite Header/首批 Events 原子 materialization、torn tail repair 与 foreign schema 拒绝 | `session/persistence/sqlite/adapter_test.go` |
| write-behind 失败 batch 保留、显式 flush 与取消 ownership | `session/persistence/write_behind_test.go` |
| Agent `turn/end` 到 SQLite durable boundary，再到 idle convergence | `TestTurnEndAwaitsSessionDurabilityBeforeAgentBecomesIdle` |
| 默认 composition 重启后的 cold list/history/create(resume) | `internal/assembly/session_persistence_e2e_test.go` |
| Workspace canonical identity、accounting、bootstrap、initialized-empty 与并发 commit | `workspace/registry_test.go` |
| Workspace SQLite/sqlc、能力插件边界与默认 composition | `workspace/persistence/sqlite`、`internal/assembly/assembly_test.go` |
| 固定源 `WebApiClient.workspace`、四个 Host frame 与 `session.create({workspaceId})` | `TestPinnedSourceWorkspaceWebApiClientTalksToGoGateway` |
| 固定源 `WebApiClient` 与 `Session.rename()` 调用 Go rename，并折入 list/history/frame/client store | `TestPinnedSourceSessionWebApiClientTalksToGoGateway` |
| System Prompt built-ins、scope shadow、snapshot、suppression、complete、tool order、插值与 invariant | `systemprompt/systemprompt_test.go` |
| 固定源与 Go System Prompt assembly/render/failure 行为一致 | `TestPinnedSourceSystemPromptMatchesGo` |
| Native Tool scope view、restriction/guard、schema cache、执行/取消、finalizer 与 detached result | `tools/runtime_test.go` |
| 固定源与 Go Native Tools config/visibility/policy/result/cancel 行为一致 | `TestPinnedSourceNativeToolsMatchesGo` |
| LLM/DeepSeek/System Prompt/Tools Factory strict config、shipped Catalog 与 Service composition | `internal/assembly/assembly_test.go` |
| core LLM content block variant clone 与 extension panic containment | `llm/harness_contract_test.go` |
| Harness Message/StreamChunk 严格 round-trip、opaque extension 与 provenance | `llm/message_test.go` |
| LLM route、prepared call、replacement、waterfall、replay state 与 terminal normalization | `llm/runtime_test.go` |
| RetryPolicy 默认值、tagged union、safe range 与 detached snapshot | `llm/retry_policy_test.go` |
| LLM Retry normal/always、budget、Retry-After/backoff、durable history、取消与 drain | `llmretry/consumer_test.go`、`llmretry/policy_test.go`、`llmretry/history_test.go` |
| StreamChunk 增量组装、first-close 和 max-token tool-call 处理 | `llm/assembler_test.go` |
| DeepSeek typed config、环境优先级和 immutable snapshot | `internal/llmdeepseek/config_test.go` |
| DeepSeek message/request serialization 与 image/reasoning/stop 语义 | `internal/llmdeepseek/serialize_test.go` |
| DeepSeek SSE framing、translation、usage、finish、empty/malformed/timeout | `internal/llmdeepseek/stream_test.go` |
| DeepSeek HTTP headers、metadata、错误分类、credential、cancel、中途失败与可复用 response recordings | `internal/llmdeepseek/adapter_test.go`、`internal/llmdeepseek/testdata/recordings/` |
| Credentials precedence、local owner-only JSON、atomic write 与 value-free description | `credentials/local/store_test.go`、`apiproxy/credentials_gateway_test.go` |
| 固定源 `WebApiClient.credentials` 经真实 Go Host 完成 describe/set/unset | `TestPinnedSourceCredentialsWebApiClientUsesGoProvider` |
| anonymous Harness user identity 的持久化与损坏恢复 | `internal/anonymoususerid/store_test.go` |
| 固定源与 Go 的 DeepSeek request、stream assembly 和 retry default 一致 | `TestPinnedSourceLLMDeepSeekMatchesGo` |
| 固定源与 Go 的 provider-routed retry delay、schedule/start、chain 与最终成功一致 | `TestPinnedSourceLLMRetryMatchesGo` |
| Agent Registry publication、rollback、reentrant detach、ownership 与顺序 | `agent/registry_test.go` |
| Agent scoped emit/waterfall/serial 的 subject isolation | `agent/events_test.go` |
| durable Inbox replay、commit/notification 顺序、clear、duplicate 与并发 append | `agent/inbox_test.go` |
| initiator context 与 prompt/request model selection snapshot | `agent/initiator_test.go`、`agent/model_selection_test.go` |
| 固定源与 Go Inbox event、list、splice/claim/clear 和 notification 顺序一致 | `TestPinnedSourceAgentInboxMatchesGo` |
| Agent Loop fresh lifecycle、Turn/Step、Tool continuation、request reconstruction 与 teardown | `TestLoopPublishesDrivesAndDisposesOneAgentLifecycle` |
| bounded parallel Tool body 与 model-order result/context commit | `TestParallelBodiesCommitResultsAndContextsInModelOrder` |
| scheduler failure 停止补充、drain 已启动 dispatch 且不伪造 result | `TestSchedulerFailureStopsReplenishmentAndDrainsStartedDispatch` |
| maintenance wake、`WhenIdle` successor convergence 与 first typed cancel cause | `TestMaintenanceLatchedWakeKeepsWhenIdleBehindSuccessorTurn`、`TestFirstCancelCauseSurvivesDisposalRace` |
| failed model attempt 通过 `agent/request-error` 在同一 Step retry | `TestRequestErrorRetryRepeatsAttemptInsideOneStep` |
| 固定源与 Go Agent Loop event、request 和 derived message projection 一致 | `TestPinnedSourceAgentLoopMatchesGo` |
| 固定源与 Go pre-step reject、模型 terminal failure 和 scheduler failure/drain 一致 | `TestPinnedSourceAgentLoopFailuresMatchGo` |
| 固定源与 Go 从 dispatch 日志前缀重建初始及 seeded resume/compaction request | `TestPinnedSourceRequestReconstructionMatchesGo` |

## 13. 当前验证结果

本次在固定 Web Agent 主调用链上补齐 Question 浏览器交互、消息可见性边界、内容哈希 Web 发布，以及带 Web 构建和数据目录配置的 `make run`；没有按完整原版 WebUI 的 method 清单逐个扩展 API。当前在 Go 1.26.6、`darwin/arm64` 执行并通过：

- `pnpm -C web run build`
- `go test ./...`
- `go test -tags=contract ./internal/assembly -run TestDefaultCompositionServesFixedTypeScriptClientThroughDeepSeekAdapter -count=1`
- `make -n run`
- `make run DATA_DIR=/tmp/goren-make-run.LqyhZv LISTEN=127.0.0.1:3089`，并以 HTTP GET 验证 Web shell；临时目录随后删除
- 使用独立 3089 listener 验证新 HTML 引用 `/assets/app-<hash>.js`、HTML `no-cache`、哈希 asset `immutable` 与旧 `/app.js` 404；临时数据目录随后删除
- `git diff --check`

固定源码 `WebApiClient` 已通过真实 HTTP 完成 Credentials describe/set/unset，并通过真实 HTTP/WebSocket 创建 Session、选择模型、提交 prompt、驱动 Go Agent Loop、接收 `turn/end`、修改 queue、cancel、rename 和 respond。默认 composition contract 使用 deterministic DeepSeek HTTP oracle，在同一进程验证固定 Client 和内嵌 UI；UI 自动化完成发送、回复、新建/选择 Session、history 恢复、Question 回答和 Agent continuation，并断言 plugin runtime-context 不进入用户消息投影。API Key dialog 已通过 TypeScript 检查和生产构建，但尚未单独执行交互式浏览器行为验收。

既有真实环境验收显式加载本地 `.env`，启动默认 `cmd/goren` 后由同一 UI contract 调用真实 `https://api.deepseek.com`，结果为 `booted/prompted/selected/history = true`，最终 `runningCount = 0`。既有本机 Chrome headless 证据覆盖桌面与窄屏渲染；本次 in-app browser 不可用，因此不新增 Question 的交互式浏览器或 Chrome/Safari 人工验收声明。

## 14. 安全与依赖状态

- Echo 固定为 `github.com/labstack/echo/v5 v5.3.1`，`coder/websocket` 固定为 `v1.8.15`，准入记录见[04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)；
- 初次扫描在 Go 1.26.5 发现 6 个可达标准库漏洞；module 已提升到 Go 1.26.6 并复扫通过；
- 当前 listener 默认只绑定 `127.0.0.1:3080`；非 loopback deployment 的 TLS、认证和授权尚未进入范围；
- DeepSeek 配置只保存 `apiKeyEnv` reference，请求开始时才通过 Credentials Provider 解析；启动环境优先且只读，local Store 使用 `0700` 目录和 `0600` JSON 文档；`.env`、真实 credential 和 secret 未进入变更。

## 15. 下一实现切片

Agent Loop core、九个 Session method、Session Persistence/SQLite、live Mux/Host projection、queue、cancel、rename、Approval/Question、Credentials 与主会话 Web UI 已组成可运行主流程。正常轮次的 `turn/end` 也已成为显式 durable boundary。当前主会话已到暂停边界，后续只保留以下非阻塞项：

1. 以 bounded prepared cache 和 Backend suffix seek 优化 cold read；二者不改变主流程正确性或公开协议；
2. 有可用交互式浏览器时补键盘操作、API Key dialog 与 Chrome/Safari 人工验收；该项不阻塞当前 Host/UI 主流程 contract。

完整 Settings、Preset、Filesystem、Shell、Attachment、Search、Fork、Typert Remote、Approval 浏览器控件和完整原版 Web product 均保持 Deferred；当前不为它们增加 handler、service 或测试占位。Credentials watcher、`credentials/updated` 与跨进程 writer lock 也未进入当前闭包。

Session Persistence/SQLite 已负责 cold facts、repair 与 Agent resume；它不恢复进程内 pending callback、socket subscriber 或未完成 retry timer。默认 RetryPolicy Consumer 仍沿 `agent/request-error` 作为独立 Plugin 进入，没有回填 DeepSeek Adapter。Agent instance 继续消费既有 Child Scope 与 scoped listener isolation，不另建第二套 Registry。
