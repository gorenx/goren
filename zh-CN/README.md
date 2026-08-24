# DeepSeek Harness Go 复刻中文详设

状态：Accepted / Implemented（当前主线能力以 `08` 验证状态为准）

本目录是 Goren 当前主线设计的唯一入口。设计以 DeepSeek Harness TypeScript 基线 `47f943859bef60e4160492346772ded9b24f765a` 为源代码证据，首要目标是让现有 TypeScript 客户端与 Go Agent 服务端保持通信协议兼容。默认服务额外内嵌自有的极简主会话 UI；原版完整 Web 产品、SDK 和 Python 不进入实现。

## 阅读顺序与职责

- [01 复制范围与兼容基线](./01-porting-scope-and-baseline.md)：目标、非目标、纳入与排除范围、需求、不变量和源代码基线。
- [02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)：模块边界、依赖方向、Plugin interface、typed config、Service/Event Registry、Scope 与生命周期。
- [03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)：Client Connection、API Proxy、RPC/stream、Session/LLM/Tool 契约、Deferred adapter 边界和跨语言兼容验证。
- [04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)：Connection carrier、typed config、标准库、第三方依赖、持久化、PTY、沙箱与可观测性决策。
- [05 复制路线图与验收](./05-porting-roadmap-and-acceptance.md)：分阶段交付、源包映射、测试策略、完成定义和未决事项。
- [06 Connection Host 模块设计与实现](./06-connection-host-module.md)：Connection wire contract、Echo inbound adapter、信任边界、验证所有权、取消与生命周期。
- [07 API Proxy 模块设计与实现](./07-api-proxy-module.md)：typed method Catalog、Provider 边界、`host.describe` 纵向切片与结果/错误映射。
- [08 实施进度](./08-implementation-progress.md)：阶段完成度、当前代码/测试证据、验证结果、阻塞项和下一步。
- [09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)：Goren Server Assembly、源插件映射、Factory Catalog 与 Connection 插件装配。
- [Go Cordis 风格通用 Plugin 事件领域框架设计](./Go_Cordis_风格插件事件领域运行时设计方案.md)：可复用 Plugin Runtime 的目标、Go 类型身份、Manifest、Service/Event/Waterfall、Scope、调用准入与 `RunRetained` 契约。
- [10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)：Header/Event、内存 append-only log、surface、LiveStore 生命周期、订阅与 persistence 边界。
- [11 System Prompt Registry 与 Assembly 模块设计](./11-system-prompt-registry-and-assembly.md)：scope overlay、注册生命周期、deterministic assembly、tool schema 排序、严格插值与上下游边界。
- [12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)：Tool Definition、Root Registry + Child Overlay、restriction/guard、policy Waterfall、一次性执行状态机、结果物化与 System Prompt 投影。
- [13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)：provider-neutral LLM Service、Adapter Registry、模型路由、流组装、RetryPolicy，以及 DeepSeek typed config、HTTP/SSE 和错误映射。
- [14 Agent Registry、Inbox 与实时事件模块设计](./14-agent-registry-inbox-and-events.md)：live Agent registry、Factory seam、durable Inbox projection、Agent-scoped events、initiator attribution 与 model selection snapshot。
- [15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)：concrete Agent lifecycle、Turn/Step 状态机、请求重建、模型 attempt、Tool-call 调度、取消与 runtime-context projection。
- [16 Session API Gateway 与实时 Frame 投影](./16-session-api-gateway-and-live-frames.md)：根 wire contract 与 `apiproxy/session` 实现边界、十个 `session.*` method、主/搜索 Gateway、默认模型选择、Mux baseline/live 与 Host edge。
- [17 Approval、UserQuestions 与 Interaction Gateway](./17-approval-user-questions-and-interaction-gateway.md)：Approval policy/audit、UserQuestions Provider、`ask_user_question` Consumer，以及 requested/respond/resolved/replay 闭环。
- [18 Session Projection 与 Session Title 模块设计](./18-session-projection-and-title.md)：通用 projection unit/registry/checkpoint、`session/title`、fallback/Provider 调度、rename 与客户端 higher-seq-wins。
- [19 Session Persistence 与 SQLite 事实存储设计](./19-session-persistence-and-sqlite.md)：durable facts、LiveStore/Persistence/Backend 边界、write-behind、cold recovery/resume、SQLite/sqlc schema 与事务。
- [20 Workspace Registry、SQLite 与 API Gateway](./20-workspace-registry-and-api.md)：Workspace identity/order/accounting、历史 bootstrap、SQLite/sqlc Backend、七个 API、Host frame 与 `session.create({workspaceId})`。
- [21 Web Agent 主会话闭环与能力边界](./21-web-agent-main-flow.md)：极简内嵌 Web UI、Question 回答与固定 TypeScript Client 到 Go Agent Loop 的纵向能力矩阵、排除面、依赖方向与分层验收。
- [22 Credentials 与 API Key 管理](./22-credentials-and-api-key-management.md)：Credentials Provider/Manager/LiveStore 边界、local JSON LiveStore、Host write-only API、DeepSeek 请求时解析与 Web API Key 设置。
- [23 Session Query 与 Search](./23-session-query-and-search.md)：live-preferred corpus、exact read/filter/trace、语义文档、可重建 SQLite FTS5 index、cursor 与 `session.search`。
- [24 Context Compaction](./24-context-compaction.md)：Compaction Service Definition / Provider / Consumer、Token Meter、Surface replacement 事务、自动压力与 overflow recovery、Tool Result Pruning 及上下游交互。
- [25 Context Compaction 实现进度](./25-context-compaction-implementation-progress.md)：Compaction 专用依赖就绪度、交付矩阵、验收 Gate、实施切片和阻塞项；`08` 只保留总体状态。

## 模块内运行说明

- [`plugin/README.zh-CN.md`](../plugin/README.zh-CN.md)：Plugin Runtime 职责边界、依赖结算、Scope 路由、统一 Fiber Effect 生命周期与按需 API 示例。
- [`session/README.zh-CN.md`](../session/README.zh-CN.md)：Session append-only log、LiveStore、publication、`DeferAfterEvent` 与生命周期。
- [`agentloop/README.zh-CN.md`](../agentloop/README.zh-CN.md)：Agent driver、Turn/Step、请求/Tool 调度、durability checkpoint 与 idle convergence。
- [`apiproxy/README.zh-CN.md`](../apiproxy/README.zh-CN.md)：typed method adapter、Session/Interaction Gateway、live frame、correlation 与背压。
- [`apiproxy/session/README.zh-CN.md`](../apiproxy/session/README.zh-CN.md)：Session API façade、读取/生命周期/模型/对话/Search 用例与 Agent activation 状态。
- [`internal/assembly/README.zh-CN.md`](../internal/assembly/README.zh-CN.md)：Factory Catalog、typed config、依赖结算与 composition rollback。
- [`internal/connection/README.zh-CN.md`](../internal/connection/README.zh-CN.md)：Connection Host Plugin、Echo carrier、监听端口与 WebSocket 排空。
- [`systemprompt/README.zh-CN.md`](../systemprompt/README.zh-CN.md)：System Prompt root/overlay、精确 Handle 与 snapshot assembly。
- [`workspace/README.zh-CN.md`](../workspace/README.zh-CN.md)：Workspace Registry、Session accounting、SQLite adapter 与 API/Host 交互。
- [`llm/README.zh-CN.md`](../llm/README.zh-CN.md)：provider-neutral LLM Runtime、typed Service/Event/Waterfall、Adapter registration 与流归一化。
- [`llm/factory/README.zh-CN.md`](../llm/factory/README.zh-CN.md)：LLM 领域 typed config、严格构造与 Assembly 注册边界。
- [`llm/retry/README.zh-CN.md`](../llm/retry/README.zh-CN.md)：默认 RetryPolicy Consumer 的职责、normal/always 决策、durable retry events、历史投影和取消/卸载流程。
- [`llm/deepseek/README.zh-CN.md`](../llm/deepseek/README.zh-CN.md)：DeepSeek direct Provider adapter 的 typed config、lazy request、HTTP/SSE、流转换、错误/取消和 response recordings。
- [`llm/tokenmeter/README.zh-CN.md`](../llm/tokenmeter/README.zh-CN.md)：单例 replay Token Meter 的 Service 边界、固定 estimator、usage anchor、三个 Projection 与 Consumer 交互。
- [`compaction/README.zh-CN.md`](../compaction/README.zh-CN.md)：Compaction Service Definition、事件、checkpoint provenance、结果和人工失败分类。
- [`compaction/basic/README.zh-CN.md`](../compaction/basic/README.zh-CN.md)：Basic Provider 的 Plugin/业务分离、自动策略、区间事务、overflow retry 和人工 maintenance。
- [`compaction/toolresultpruner/README.zh-CN.md`](../compaction/toolresultpruner/README.zh-CN.md)：可选无模型 Pruner、Unicode budget、Token Meter 依赖和 Surface replacement 边界。
- [`web/README.zh-CN.md`](../web/README.zh-CN.md)：内嵌主会话 UI、浏览器状态对象、Host RPC/WebSocket/respond 交互与边界。
- [`credentials/README.zh-CN.md`](../credentials/README.zh-CN.md)：Credentials 能力、Manager precedence、storage-only LiveStore 与 local owner-only 文件实现。

首次阅读按 `01`–`05` 顺序理解全局设计；进入实现时读取对应模块文档，再从 `08` 查看当前进度。实现单个能力时，先从 `01` 确认范围，再读其拥有契约的文档。DeepSeek Harness 的 Service Definition / Provider / Consumer、事件 owner 和生命周期是默认职责边界；Go 包不机械复制每个 npm 包，但没有明确证据时也不另起一套领域切分。

## 权威关系

- 本目录 `01`–`05` 拥有全局设计，`06`、`07`、`09`–`24` 拥有已进入实现的稳定 Harness 模块设计；`Go_Cordis_风格插件事件领域运行时设计方案.md` 单独拥有可复用 Plugin Runtime 的重构目标与插件作者契约。`08` 拥有全仓总体实施状态与公共验证证据，`25` 单独拥有 Compaction 的子项进度和 Gate，`08` 只引用其总体状态。
- 子模块 `README.zh-CN.md` 解释贴近代码的职责、工作原理和交互流程；跨模块契约仍由本目录对应编号文档拥有。全仓公共实施证据由 `08` 拥有，Compaction 专项子项证据由 `25` 拥有并向 `08` 汇总。
- 根目录 `README.md` 与 `README.zh-CN.md` 只说明项目背景。
- TypeScript 的行为证据来自固定 commit；源仓库后续变化不会自动成为 Go 需求。
- Go 代码证明当前实现，跨语言 fixtures 和测试证明兼容性；设计状态不能代替实现或验收证据。

## 隔离的历史设计

[`memory-design/`](./memory-design/) 保存此前的 Memory Agent 设计。它不属于当前 Harness 复刻主线，不应被默认加载、引用或用来决定 Harness 的 Agent、Session、Tools、Workflow 和 Knowledge 边界；只有明确处理该历史主题时才读取。

## 文档规则

- 详设使用简体中文，两位数字文件名和一级标题表达阅读顺序。
- 公共名、事件名、JSON 字段、配置键和既有领域术语保持 TypeScript 的 canonical form。
- 一个事实只在拥有该职责的文档中定义，其他文档只链接。
- 全仓阶段汇总、公共验证命令、阻塞项和下一步只更新 `08`；Compaction 的子项完成度、专项 Gate 和切片顺序只更新 `25`，`08` 仅保留一条总体状态。模块设计文档不重复日期性进度。
- 只为已经出现真实职责与代码边界的模块增加实现文档；不为规划目录或空 package 建文档占位。
- 模块文档至少记录职责/非职责、源 owner、依赖方向、上下游流程、生命周期、失败/取消语义、验证所有权和后续能力进入规则；代码/测试证据与实施缺口写入其进度 owner，默认是 `08`，Compaction 专项为 `25`。
- 已实现的独立子模块在代码旁维护 `README.zh-CN.md`，流程与交互图使用 Mermaid；暂不增加子模块英文 README，也不为 helper、generated 或 test-only 目录批量创建模板文档。
- 未确认的行为保留为显式未决事项，不写成已经实现或已经验证。
- 新增、删除、重命名、重排文档或改变文档职责时同步更新本索引。
