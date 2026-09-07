# 生命周期、迁移与验收

状态：创建/关闭顺序及工具更新屏障已确认；失败恢复和实施细节仍为建议。已确认边界见[总览](../actor-runtime-draft.md)，通信契约见[02](./02-capability-protocols.md)。

实施批次以[12 实施计划](./12-implementation-plan.md)为准；本文保留责任映射与实验记录。

## 1. 当前代码与迁移责任

| 当前入口 / owner | 目标职责 | 必须保留或显式评审 |
| --- | --- | --- |
| [BuildServer](../../internal/assembly/server.go)、[Catalog](../../internal/assembly/catalog.go) | Server 显式装配、typed config、启动事务 | 静态链接、严格校验、启动失败逆序回滚 |
| [Plugin](../../plugin/plugin.go) 与 Require/Provide | GoAkt 运行时 消息寻址、具名 owner | 依赖在对外接纳前就绪，不用 GoAkt Extensions 建第二个 service locator |
| [RegistryService](../../agent/registry_lifecycle.go) | Agent Registry actor | 同 ID 并发创建、未发布实例、exact incarnation、child-first 关闭 |
| [agentHost](../../agentloop/agent_host.go)、[agentScope](../../agentloop/agent_scope.go) | 移除 Scope，将职责迁给创建用例和各 actor/Runtime | 单一资源 owner、Session 绑定、创建时快照继承、取消与资源静止；明细见[04](./04-subagent-configuration.md#4-当前-scope-职责的去向) |
| [ReactLoopAgent](../../agentloop/rla.go) | AgentLoop | Send/Followup/Steer/Inject、KeepInbox、Turn/Step、WhenIdle、maintenance |
| [tools.ToolRuntime](../../tools/contract.go)、[executionRuntime](../../tools/runtime.go) | 每 Agent Tool Runtime + ToolActor | Prepare/Dispatch/Finalize、approval、结果 schema、取消错误码、并发策略 |
| [ToolLayer](../../tools/layer.go)、[ToolSetHandle](../../tools/layer_tool_set.go) | 共享 Tools Registry 的带 Agent 作用域目录 | 注册身份、scope、精确注销、工具集合原子换代 |
| [PromptLayer](../../systemprompt/layer.go)、[Prompt 契约](../../systemprompt/types.go) | 共享 Prompt Registry + 每 Agent Prompt Runtime | 覆盖、顺序、变量、suppressor、工具排序、完整 assembly |
| [LiveStore](../../session/live_store.go) | Session Registry 的身份与目录部分 | Session identity、live/cold、进入/退出与 metadata 投影 |
| [Session Commit](../../session/session_context_commit.go)、[Flush](../../session/memory_store_lifecycle.go) | SessionActor 的写入与持久化协调 | FIFO、commit/barrier、flush failure、release retry |
| [MCP 方案](../MCP客户端桥接设计方案.md)、[协议适配](../../mcpclient/protocol_session.go) | 每 Agent MCP ToolActor 与独立连接 owner | 已计划 wire、MRTR 关联、raw 工具名、generation、重连与取消 |

当前工具 Execute 接口同步返回结果，AgentLoop 在 goroutine 中 Dispatch 并通过 channel 收集结果；
目标 actor 路径改用异步 Request + Then，详见[通信协议](./02-capability-protocols.md#1-消息分类与-pubsub)。

这是目标职责映射，不表示当前 API 已兼容 actor，也不按 TypeScript 文件或 Go package 数量机械拆 actor。
当前 ToolLayer/PromptLayer 已按 Agent 隔离，是迁移依据；需要移出其中的 Plugin service/event 锚点。
Subagent 不再沿 parent Scope 动态读取配置；“继承”的目标语义是创建时复制有效配置并应用覆盖。
这是已确认的配置模型方向，父子 membership、结果回报及 child-first 关闭关系保持不变。

### Event / Waterfall 的兼容性差异

当前 `systemprompt.Changed.EventDelivery` 明确使用同步有序交付以支持失败回滚；
`ToolLayer.AddTool` 在 `publishChanged` 失败后撤销注册；Session `Created` 是可否决的发布边界，
`FlushRequested` 是需要完成结果的持久化请求。这些不能全部等同为 post-commit PubSub 通知。

迁移时逐个监听器分类：

- 参与接纳/校验/提交成功判定的行为进入显式 prepare/validate 阶段；跨 actor 使用异步 Request + Then，保留业务先后及失败语义，不保留同步阻塞调用。
- 仅消费事实的行为进入提交后的 PubSub；消费者失败不回滚注册或日志。
- Waterfall 中有返回值、短路、重试或错误恢复语义的阶段，保留 typed policy 或定向请求。
- 对外 canonical event、wire envelope 和错误码保持既有映射；新内部消息名称不自动替代它们。

准确监听器清单、失败映射与 golden 证据是生产迁移前置工作。若无法在新协议保持原语义，
必须记录具体源符号、Go caller、observable change 和迁移方式，不能仅声明“actor 化后天然等价”。

## 2. 创建、就绪与关闭

正常生命周期顺序已确认，具体 GoAkt parent/child supervisor 行为及异常恢复仍需实验验证。
以下“等待”“排空”“join”均通过状态机与异步完成响应推进，不在 Receive 内同步等待其他 actor 或任务。

启动：严格解码配置 → 构造并启动共享 Registry/必要能力 → 建立 PubSub 通知路径 → 开放创建与客户端接纳。
发生失败时只回滚本次获得的资源，按依赖逆序取消、等待和释放，不能留下可见的半启动系统。

收到消息时查找或创建 Agent：

1. Agent Registry 按消息的 SessionID 查找 Agent，AgentID = SessionID；已有可接纳实例直接复用。
2. 不存在时原子预留身份与 incarnation，先构造尚未对外发布、不能处理业务的 AgentLoop。
3. 已创建的 AgentLoop 经 GoAkt 运行时请求各组件配置和版本，读取阶段不获取业务资源；并发同 ID 创建合并到同一创建事务。
4. 配置解析完成后，请求 Session Registry 创建或恢复同 ID SessionActor、完成绑定，并初始化必要 Tool/Prompt 能力；AgentLoop 汇总就绪状态。
5. 全部就绪后公布 Agent，并按顺序接纳消息；消息不能在 Session/能力就绪前执行。
6. 失败时关闭候选 Runtime/ToolActor、精确撤销注册与订阅，关闭候选 Agent，再释放绑定并关闭本次获得的 Session 实例，最后释放身份预留。保留持久化历史。

Subagent 创建固定一次 parent 有效配置快照和 child 覆盖输入；子 AgentLoop 创建后经 GoAkt 运行时
请求各组件解析并校验所属配置，初始化自己的 Tool/Prompt Runtime 与 ToolActor。parent 关系仍由 Agent Registry 管理，发布前需检查 parent
没有进入关闭状态。具体配置规则与 Scope 职责迁移见[04](./04-subagent-configuration.md)。

Agent 正常关闭：

1. Registry 与 Agent 建立拒绝新业务的 cutoff；后来的消息等待当前关闭事务，不进入旧实例。
2. 排空已接纳的用户消息及工具更新操作，等待其任务与结果结算。正常关闭不默认取消当前消息。
3. Agent 收齐 pending commit，完成必要 Session flush；关闭业务子 Agent 时保持既有 child-first 语义，不能提前终止前序消息仍需使用的子 Agent。
4. 撤销本 Agent 的目录条目和订阅，关闭 ToolActor、Tool Runtime、Prompt Runtime，Agent 确认静止后完成自身关闭。
5. Registry 协调显式解绑并关闭同 ID SessionActor，保留持久化历史。
6. 只有 Agent 与 SessionActor 均完成关闭后才释放该 ID 占用；等待中的新消息再创建/恢复一对实例并接纳。

Session 必须在 Agent 完成处理和结算/flush 前可用。处理完单条消息不自动关闭，显式关闭或未来明确的空闲策略才触发此流程。
显式取消与异常停止另走取消、join 和结算路径，不能把取消意图等同所有已接纳消息已处理完成。
正常业务 Close 先返回 业务已排空，再进行框架 Stop。不能只在 PostStop 中决定业务持久化是否成功。
上下文超时只表示调用者停止等待；关闭事务是否仍在进行必须能查询，不能重复构造同 ID 实例绕过未完成关闭。
无法停止的外部 I/O、flush 失败和进程退出预算须显式报告，不能声称强制终止 goroutine 或副作用回滚。

### 工具更新的生命周期位置

普通注册、更新和撤销是 Agent 业务队列中的有序操作：前序消息完整结算 → 进入更新屏障 → Tool Runtime
在稳定 ToolActor 内准备候选实现与资源 → Tools Registry 原子发布 → 切换实现并清理旧资源 → 直接确认 Agent → 后续用户消息继续。
工具调用和 Prompt 组装不能在屏障期间启动新的用户执行，但完成响应、取消和关闭控制仍可处理。
失败处理和 PubSub 通知边界统一见[工具更新协议](./02-capability-protocols.md#2-每-agent-的工具注册与换代)。
原先建议的 Session lease 和跨更新多代可执行工具快照不属于首期方案，不能作为迁移前置依赖。

## 3. 迁移阶段

本节 A–F 保留阶段目标；具体实施批次与模块落点见[09](./09-module-implementation.md#7-实施批次与出口)，
运行载体提案和整体架构见[05](./05-implementation-overview.md)。不以实验通过替代设计评审。


| 阶段 | 工作 | 出口证据 |
| --- | --- | --- |
| A：边界整理 | 完成本组方案，确认 owner、Registry/Runtime 分工，梳理 Scope 职责去向 | 明确已确认/建议/未验证，不改生产 |
| B：行为与框架契约 | 盘点监听器与 Plugin effects，扩展 GoAkt 实验 | Request/Then 与 Reentrancy、同 ID 创建关闭、更新屏障、Tool Runtime 隔离、取消、PubSub 恢复用例 |
| C：最小运行链路 | 显式装配 → 创建 Agent/Session → Prompt → LLM → 一个 ToolActor → Session → 取消/关闭 | 一条可运行链路及协议 golden，不仅是 actor demo |
| D：迁出其余 owner | 逐项迁移 Tools/Prompt 动态变化、交互、Credentials、Workspace、Compaction、Subagent、Query/Projection | 每个 owner 的调用链、生命周期、失败与测试完整 |
| E：删除旧 Plugin/Scope 路径 | 清除 Base/Manifest/Require/Provide、Catalog/PluginSpec、Scope/ScopeEditor 及旧装配 | 原职责全部有 owner，单一生产路径，无换名 Scope actor、兼容 facade 或第二套 locator |
| F：MCP 闭环 | 在新运行时完成自注册、原子工具代、MRTR、重连、卸载 | 配置到 Agent 可观察结果的完整验收 |

每步保持可编译、可测试；迁移过程不得让同一 Agent/Session 同时由新旧 runtime 接纳与修改。
确有过渡 adapter 时必须单向且有删除出口，不让 Plugin Runtime 成为 actor 上层的永久执行入口。
保留现有外部协议/存储测试；不因删除旧内部 API 而一并删除行为回归证据。

Go 包继续按业务能力划分，actor 实现归所属 owner；框架地址或生命周期细节不进入 Session persisted shape、
HTTP/WebSocket wire 或 SDK DTO。具体包迁移以真实 public extension 需求为依据，本轮不生成占位目录。

正式实施前更新 AGENTS.md 中“必须使用 Plugin”等与已确认方向冲突的规则，并按批准的最终方案更新权威设计。
当前只是独立设计文件，未改全局索引。实现、测试与依赖一起提交，设计与索引单独提交。

## 4. 验证状态

已存在的[独立实验](../../experiments/goakt/README.zh-CN.md)固定 GoAkt v4.5.3，记录了五个用例的普通测试、
race 和 vet 通过：取消等待提交、取消 join 与旧 generation、提交失败、合作任务关闭、standalone 双订阅者通知。
原五项实验本轮随新增 Request 实验重新运行。实验不包含 Tools Registry、Tool Runtime、ToolActor 或 Prompt Runtime
的完整目标设计，不能作为新拆分的实现证据。

2026-09-07 补充五项 [Request 实验](../../experiments/goakt/request_test.go)：普通 Request/Then、
请求取消与业务取消分离、超时后查询，以及两项延迟回复限制复现。十项实验的普通测试、race 和 vet 通过。
两项限制复现以“原请求超时”为预期，不能计作目标链路验收通过。
固定版本普通 Actor 未提供公开延迟回复句柄；ReceiveContext 禁止跨 Receive 保存，完成消息上的 Response
不关联原请求。Agent → Tool Runtime → ToolActor 的最终回复链路尚未通过，业务更新队列及 Session
flush/关闭的新 Request 协议也未验证。详细证据与边界见[实验记录](../../experiments/goakt/README.zh-CN.md#request-契约验证与阻塞项)。

后续新增三项 [Grain 实验](../../experiments/goakt/README.zh-CN.md#grain-验证记录)：三层延迟回复成功/失败、
取消与迟到回复、框架停用消息 停用与再激活。完整模块 13 个顶层测试及 race、vet 通过。
GrainReply 解决了该实验的最终回复链路；但 Request(框架停用消息) 超时，TellGrain 才触发停用，
且停用后旧身份能够重新激活实例。测试驱动观察的停用顺序不等于生产关闭协调已实现。
业务更新队列、Session FIFO/flush、父子生命周期等仍待验证。

本轮进一步新增六项 [业务协议实验](../../experiments/goakt/README.zh-CN.md#pipetoself业务关闭代际与-flush-验证)，
完整独立模块 19 个顶层测试、race、vet 通过。覆盖实际文件读取、取消后等待任务完成、业务关闭排空及
flush 确认、A→更新→B 顺序、Session FIFO/Sync 失败重试、同业务 ID 的不同 Grain 类型，以及旧代命令
触发再激活后被 Registry 拒绝。更新仍以 Gate 模拟准备/发布/退役；不代表完整注册事务已实现。
最终框架停用确认、Registry 恢复与退休竞争、父子关闭及生产 Session 持久化恢复仍待验证。

| 必须补齐 | 验收内容 |
| --- | --- |
| 异步 Request | 发出请求后 Receive 返回；Then 在 actor 消息处理上下文推进状态；发起失败安全处理；等待期间取消/关闭可达，下一条用户业务仍排队 |
| 请求与执行结算 | 超时/RequestCall.Cancel 不冒充执行停止；原请求结束后能查询最终状态；不丢失已提交事实，不重试未知工具副作用；迟到结果不重复结算 |
| Agent 隔离 | 同名工具在两个 Agent 中实例、权限、并发、取消和关闭互不干扰 |
| Subagent 配置与生命周期 | 创建时 parent 快照加 child 覆盖；双方更新不相互污染；父子关系与 child-first 关闭不变，创建失败不伤及 parent |
| Scope 移除 | 每个安装、校验、资源、订阅及策略入口有明确承接者，保持回滚、精确释放和错误/取消语义 |
| 消息与更新顺序 | A、B、更新、C 按接纳顺序执行；A/B 全部结算前不更新，更新确认和旧实例释放前不执行 C |
| 注册与退役 | 初始化失败不发布；批次原子；旧注销不删新实例；目录发布时路由已就绪；正常更新不保留跨屏障的可执行旧代 |
| 版本快照 | Tools/Prompt 更新不改已发请求；缺通知/重新订阅后补齐，不能停留旧版本 |
| 调用关联 | 更新等待期间及异常失效时取消仍找到原实例；重复完成、重建后的旧 incarnation 不污染 Session |
| Session | AgentID = SessionID，同 ID 并发创建唯一；正常排空后先关闭 Agent 再关闭 Session；新消息不创建重叠实例；FIFO、commit/flush 和失败重试 |
| PubSub/背压 | 慢消费者、溢出、控制命令接纳、订阅释放竞争、Session replay |
| 兼容与权限 | 现有 TS→Go fixture、错误码/事件顺序、效果处策略、敏感数据不进入消息日志 |

根模块 checkpoint 已知有五处命名冲突，详情见保存提交 `c359000`；不在本轮文档工作中修改。
文档验收只证明链接与文本格式，不声称 Go 行为、生产 migration 或外部 provider 已验证。

## 5. 待决项

| ID | 事项 | 风险 / 下一步 |
| --- | --- | --- |
| O1 | 同 ID 创建/关闭失败恢复 | 顺序和显式绑定已确认，不采用 lease；需明确关闭失败时的 ID 占用、等待消息上限与超时 |
| O2 | 更新结果未知、发布后清理失败 | 消息间屏障与原子发布已确认；需明确状态查询、清理重试及何时报告终止 |
| O3 | 完整消息边界与特殊失败 | 普通更新后释放旧实例已确认，不维护多代执行快照；需核对 Followup/Steer/Inject，及 MCP swap 冲突与普通更新保旧规则的差异 |
| O4 | Prompt 全局默认/Agent override 与共享定义引用 | 创建时继承已确认；需明确条目覆盖、排序，以及快照与共享定义 revision 的关系，不恢复 parent 动态查找 |
| O5 | 有界 mailbox 与控制/完成消息 | 取消/关闭不能因队列满而丢失，完成丢失不能造成永久 running |
| O6 | Supervisor、incarnation、重复操作与重启恢复 | 不自动重放外部工具副作用；需要明确 crash 与 recover 语义 |
| O7 | MCP 连接共享范围 | 区分 credential、工作目录、权限与 connection generation，不能跨 Agent 泄漏状态 |
| O8 | 动态配置替换与 Plugin 扩展迁移 | 不隐式删除 mount/replace/unload 对应的实际需求，不重建 service locator |
| O9 | 其余能力 actor 映射 | 沿真实 owner/caller 逐项设计，不预设包等于 actor |
| O10 | Subagent 字段级覆盖与配置恢复 | 快照继承及父子生命周期不变已确认；需明确未设置/零值、工具/Prompt 集合操作、权限上限、credential 引用及恢复依据 |
| O11 | 运行载体与延迟回复 | 普通 Actor 缺口仍在；Grain 的三层 Request + DeferResponse 已实验通过。是否采用 Grain 仍需验证关闭确认及旧身份重新激活防护，不自动改写全部 owner 为 Grain |

下一步基于已通过的 Grain 业务实验补齐最终停用确认、同 ID 关闭/重建竞争及实际 Tools Registry 发布/退役；O1–O3 只补齐失败恢复和兼容性细节，
不重新讨论是否采用 Session lease 或多代工具快照。Prompt 定义是否采用与工具相同的业务屏障仍未确认，不在本轮自动扩展。
进入 Subagent 实现前，先完成 O10 的配置规则和[Scope 调用者迁移清单](./04-subagent-configuration.md#4-当前-scope-职责的去向)，
不将“移除 Scope”理解为删除资源回滚、策略或父子生命周期管理。
