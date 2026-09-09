# Actor 重构集成问题记录

范围：除 Subagent 外的服务器重构、集成测试与修复。实施归属见 [API Proxy](./38-api-proxy-module.md)、[生命周期](./39-agent-activation.md) 和 [实施计划](./12-implementation-plan.md)。不以测试夹具、单包通过或排除编译失败包的结果宣称整仓已完成。

| 编号 | 发现与影响 | 根因 | 修复与验证状态 |
| --- | --- | --- | --- |
| INT-01 | `go test ./apiproxy/... -run '^$'` 报两个 invalid field name；审批和提问 API 无法构建 | 将 Outcome 类型移至 rpc 包时误把结构体字段名也限定为 rpc.Outcome | 已恢复字段 Outcome；Interaction 独立包的真实 Catalog 响应测试和四类帧精确 JSON 测试通过 |
| INT-02 | Session API、LiveFrameSource 引用已删除的 LiveStore、Context；生产 Host/assembly 引用旧 Factory，整体无法构建 | 核心模块已经改为 Actor 所有权，API 仍依赖原可变业务对象和 Plugin 组装 | 进行中：按领域拆分、替换为消费方端口与 Actor 客户端，最终必须运行普通会话生产集成 |
| INT-03 | 并发 API 激活同 Session 仍依赖旧 AgentSessions 的 map/mutex；直接改为不同 CreationID 的 CreateAgent 会让后到请求失败 | 原 API 自己归并创建请求，Registry 原 Resolve 不支持等待已有创建 | 已增加 Resolve.WaitReady，复用在途创建回复；真实 Actor race 测试覆盖成功、初始化失败、关闭、缺失 ID 和默认非等待查询；Gateway 接入待完成 |
| INT-04 | 同时关闭 Interaction 时，后到 Close 在终态帧尚未发布完成前返回 nil，组合根可能提前关闭下行通道 | 原 closed bool 同时表示停止准入和关闭完成；重复 Close 只检查 bool，未等待 settlement | 用单一 closeDone 区分未开始/正在关闭/完成；重复 Close 等待相同结果并响应其等待上下文取消。TestConcurrentCloseWaitsForSettlement 修复前稳定失败（返回 nil），修复后已与 Interaction/stream/rpc 的 -race -count=1 测试通过 |

## INT-05 Workspace 重命名规则遗留在 API Gateway

- 发现方式：迁移 Workspace API 时沿 Gateway → Registry → Actor 调用链检查。
- 根因：旧 Gateway 自行加锁并遍历重名，`DurableRegistry.SetTitle` 直接写入标题；Actor 入口不经过 Gateway 的检查。
- 影响：绕过旧 Gateway 的调用可提交同名 Workspace；简单替换为 Actor 客户端会丢失既有 API 语义。
- 修复：标题裁剪、重名检查和无变化短路收回 `SetTitle`，API 只把 `NameConflictError` 映射为 `workspace-name-conflict` 对应协议错误常量。Gateway 删除锁和可变 Workspace 引用。
- 契约补齐：Rename、InsertSessionBefore 返回同一次操作的提交快照，ArchiveSession 返回本次提交的归档集合；API 不再另发读取拼接结果。
- 验证：`apiproxy/workspace/actor/directory_test.go` 经 RPC Catalog → Gateway → Actor 客户端 → Workspace Actor → SQLite，并发竞争同一标题，断言恰好一个成功、另一个返回名称冲突，最终集合只有一个目标标题。新测试最初因遗漏必需的 SessionHeaders 依赖而初始化失败，补齐空安装夹具后 fresh race 测试通过；这次夹具失败不是生产缺陷。
- 边界：此验证不是生产 HTTP/WebSocket 集成，旧 Host 装配仍需移除。

## INT-06 首次外部 Ask 与 Registry 延迟回复

- 复现：`apiproxy/session/events/actor/snapshots_test.go` 首次直接 AskGrain 创建未预配重入的 Session Registry，返回 `session: registry initialization requires an asynchronous request`。
- 根因：GoAkt 4.5.3 的 `actor/grain_engine.go:localSend` 只对已启用重入的 Grain 走 `envelopeAsk`；`grain_context.go:DeferResponse` 要求已有 requestID。在当前 Receive 内调用 EnableReentrancy 无法给已送达的普通 Ask 补上请求信封。
- 影响：组合入口若漏配重入，首次外部生命周期调用失败。不能据此推断所有 AskGrain 都不支持延迟回复。
- 修复方案：启动 Registry 时配置 `WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))`。外部请求 goroutine 可 AskGrain 等结果；Actor 内仍使用 RequestGrain。无须新增桥接 Actor。
- 验证：相同真实 Session Registry、Session Grain、SQLite 测试改为启动时配置重入后，创建和关闭成功；框架替代桥接夹具曾因未启用 requester 重入而超时，该夹具已删除。
- 当前状态：测试和设计已验证，生产 Host/assembly 迁移时必须落实该启动配置，不能将局部修复视为生产接线完成。

- 静态检查补充：Endpoint 初版复制了 GrainIdentity 值，vet 报告其内部 sync.Once 不可复制。已改为共享框架声明为不可变的地址指针；业务 Header 和日志数据仍独立复制。

## INT-07 旧队列提升操作分成两次修改

- 发现方式：迁移 `session.updateQueue` 时核对旧 SteerQueueAction 分支。
- 根因：API 先读取 Pending 与 Running，随后 Remove，再单独 Steer。将调用直接变成独立 Actor 消息后，其他请求或 Step 推进可进入两次操作之间。
- 影响：原消息已移除后 Steer 失败会丢失待处理输入；API 的前置检查也不能代替 owner 提交时检查。
- 方案：消费方 Agent 端口提供 Promote，要求 AgentLoop 在同一业务决策中检查正在运行及 NextTurn 归属，生成一个完整 InboxEdit；Actor 提交一次 Session 事务。API 不再 Remove 后 Steer。
- 当前状态：旧 API 分步操作已删除，Promote 业务和 Actor 命令已实现，Actor Client 接入待完成，不能据接口存在宣称端到端修复。

## Session Gateway 当前迁移证据

Session API 请求、响应、解码已移入子模块；子模块不再导入根 API Proxy。旧 AgentSessions 与 model_selection_setup 生产文件删除。
Gateway 改为消费独立 Snapshot、History、Agents 和 Workspaces 端口，模型选择调用 Configuration/Configure，不再安装 Plugin。
原历史分页、投影缓存及搜索测试已改用新读取契约；新增激活测试验证历史子 Session 可作为独立根恢复、当前父子关系限制、并发创建胜出后的逐请求 CWD 检查。
Actor Client、原模型恢复优先级的生产集成、队列 Promote 和 Host 装配仍需完成。局部构建通过不代表这些端口已有完整生产实现。


### INT-07 补充验证

`TestPromoteCommitsOneCompleteMove` 验证提案不改变 Inbox、两个 splice 一次提交、重复提升和缺失消息被拒绝。
`TestPromoteAfterCancellationKeepsInputForNextTurn` 验证取消后的输入仍排入 next-turn，保留基线 remove 后 steer 的行为。
Agent、AgentLoop 及 Actor fresh race 测试通过。API 生产 Client 尚未连接 Promote，因此完整 RPC → Actor 集成仍待完成。

## INT-08 实时控制误走冷恢复入口

固定基线 API Proxy 的 updateQueue/cancel 仅查询 ctx.agents，不恢复冷 Session；旧 Go sessionConversation 却复用 ordinaryAgent 的自动恢复路径。
已改用 attachedAgent，仅解析当前创建；未知队列返回 queue-item-not-found，取消未知 Session 返回 session-not-found。非文本编辑按照基线先返回 attachment-error。
`TestLiveControlsDoNotRecoverColdSessions` 经真实 RPC Catalog 验证这些错误和未发生创建／恢复。

## INT-09 AgentLoop 重复持有和覆盖 LLM 配置

根因：AgentLoop 保存初始化配置，并在每次模型请求前执行 SaveConfiguration；后续请求又从 Session Header 恢复配置。这使 API 写入的 LLM 配置可能被旧值覆盖。

确定方案：配置统一由 LLM 管理。移除 AgentLoop 配置字段、Configuration/Configure、ResolveConfiguration/SaveConfiguration 和对应 Actor 路由。AgentLoop 不读取 LLM 配置，只提交 AgentID 与请求内容；实际参数由 GenerationStarted 返回，用于 Session 调用事实和当前失败诊断。

初始化：Agent Registry 读取 Session 历史并发送 InitializeAgent，LLM 的 Catalog 恢复缺失配置；已有配置不会被重新激活覆盖。失败发生在 AgentLoop 激活前，Registry 仍清理已启动组件并保留 Session 持久化事实。

API：Session.Models/SelectModel/Prompt 的配置读写走 Models 端口，由 LLM Directory Client 对接 LLM Grain。取消 Agent 接口上的配置方法，无兼容别名。

验证：AgentLoop 请求直接启动 Attempt；真实 Actor 重试与取消测试、Registry 失败清理测试、配置默认/历史恢复测试、无 AgentLoop 的 API Client 配置更新与 ReadCall 测试通过。完整 API 生产组装和普通对话集成仍待完成，不将本条修复视为全项目验收。

本轮验证命令：`go test -race -count=1 ./agent/... ./agentloop/... ./llm/... ./apiproxy/session/... ./apiproxy/llm/...`、同范围 `go vet` 和 `go build` 通过；最后一次缩小 ModelRequest 诊断数据后，重新运行 AgentLoop 全子树非缓存测试通过。

全项目编译审计 `go test ./... -run '^$'` 仍失败：生产 assembly 引用已删除的 agent/factory、agentloop/factory、commands/factory、LLM/Compaction Factory 等。根因与 INT-02 相同，必须用实际 Actor 组合替换，不能恢复兼容 Factory。该审计不等于完整运行测试。


## INT-10 Session API 客户端聚合了不同领域

根因：初版 Agent Client 同时读取 Session、Projection 并修改 Title，导致 Agent 接口和客户端同时承担多领域职责。

方案：拆分 Agents、Agent、Logs、Projections、Titles 端口，各由独立 Actor Client 实现；API Reader 组合日志、投影和执行状态。Projection.Compute 只折叠传入前缀，不用旧前缀替换实时缓存；当前每次重建，性能尚未优化。Title 结果来自本次提交事件。

验证：`TestDomainClientsUseIndependentTitleAndAgentOwners` 使用真实 Agent Registry、Session、Projection、Title 及 SQLite，验证关闭、同 ID 恢复、独立标题修改和旧日志前缀投影。该测试的模型与 Prompt 初始化使用测试端点，未覆盖真实生成与 HTTP/WebSocket。

## INT-11 Agent 查询缺少创建身份校验

根因：旧 GetInbox 查询没有 CreationID，旧客户端在同 SessionID 新建后可能读到新实例输入。

方案：改为 ReadActivity / Activity，校验 CreationID 后从业务对象读取 Pending 和 Status；客户端固定原创建身份，不自行保存执行状态。无身份查询删除，旧名称不保留别名。

验证：AgentLoop Actor 测试以及 INT-10 的同 ID 恢复测试拒绝旧客户端查询。

## INT-12 Title 组件缺少生命周期装配入口

根因：Title Grain 和订阅者已有实现，但 API 没有由生命周期 owner 发布的就绪组件地址，导致此前客户端设计依赖未实现的组合。

方案：Title Registry 实现 StartComponent / StopComponent；初始化 Title 并等待订阅确认后回复 Runtime，Agent Registry 统一准入。部分启动失败保留已获得的地址，由关闭流程回收；关闭旧创建不能影响新创建。Subscriber 的 Ready 通道替代额外启动观察者，不承载业务状态。

验证：`TestTitleComponentRecoversAfterSessionBindingFailure` 覆盖取得 Title 地址后 Session 拒绝绑定、重复关闭、旧身份拒绝及随后新创建成功。正常关闭与恢复见 INT-10。生产组合根仍需注册该组件；Directory 的初始化失败也仍需生产关闭策略，不能把返回错误描述为自动回滚。

本批验证：`go test -race -count=1 ./agentloop/actor ./apiproxy/session/actor ./agent/registry` 通过。全项目装配缺口仍为 INT-02，未把局部验证当作项目完成。

提交证据：代码 `bfcdd2a`。仅导出 Git 暂存快照后运行 `go test -race -count=1`、`go vet`、`go build`，范围为 `./agent/... ./agentloop/... ./apiproxy/session/... ./session/registry ./session/title/... ./session/projection/...`，全部通过，不依赖剩余工作区修改。


## INT-13 API 和 Connection 仍通过已删除的 Plugin 能力装配

根因：领域实现已迁到 Actor，但 host.Plugin、API Factory 与 Connection Plugin 仍解析旧 Registry、Scope、ToolRuntime 等接口，生产入口无法使用新客户端。

方案：根包 apiproxy.Server 直接构造领域 Gateway、Catalog、Feed、Broker、Interaction；Service 不嵌入 plugin.Service。Connection 直接使用现有 HTTPHost，无需新的包装对象。删除旧 API Host/Factory、Connection Plugin/Factory 和仅余测试引用的 EventStreams。

验证：TestServerRegistersDomainsAndDispatchesThroughHTTP 经过真实 Echo 路由，核对各领域方法注册及 host.describe 的 RPCID/结果；TestServerCloseEndsMuxAndRejectsRequests 验证已有 Mux 退出和关闭后的拒绝。这里使用消费方端口测试替身，不能据此宣称 Actor 到模型的完整链路已接通。进程启动仍需替换 internal/assembly；与 INT-02 属于同一生产装配缺口。

代码提交 `ab34830`。独立暂存快照运行 `go test -race -count=1 ./apiproxy/... ./internal/connection/...`、同范围 vet 和 build 全部通过；不依赖剩余 assembly 或其他工作区修改。


## INT-14 生产 API 缺少成员与搜索端口实现

根因：Gateway 已声明 Workspaces 和 Search 领域接口，但 Workspace Actor Client 未实现 Session 用例需要的 Read/AttachSession，Query Actor 也没有 API 侧搜索客户端。组合根无法只通过领域端口装配。

方案：workspace/actor.Directory 实现 Session 的 Workspaces 接口；session/actor.Search 仅将 SearchSessions 转成 Query Actor 请求并映射具名回复。客户端不重复 Workspace 成员规则，不负责索引观察或启动 Actor。

验证：TestDirectoryAttachesSessionThroughWorkspaceOwner 通过真实 Workspace Actor 和 SQLite 验证重复附加仅一项、未知 Session 拒绝与持久化；TestSearchClientUsesObservedQueryIndex 通过真实 Query Actor 和 SQLite 验证已观察消息搜索与取消。两个客户端所在包的非缓存 race 测试通过。Collector 与完整生产组合仍未接通。

代码提交 `b9becf4`，独立暂存快照对两个客户端包运行非缓存 race 测试、vet 与 build 均通过。进程迁移执行表见 [Server 装配方案](41-server-composition.md)。

## INT-28 ToolActor 初始化回复丢失后无法确认原绑定（已修复）

生命周期接线前的契约检查发现 Initialize 无条件拒绝已初始化对象。`TestInitializationRetryConfirmsBindingWithoutReplacingImplementation` 先重复发送相同绑定，修复前失败于 `tools: Grain is already initialized`。这会使调用方在首次回复丢失后无法通过重试确认已成功的绑定。

Initialize 现在比较 CreationID、AgentID、工具名称和 Registry 身份，相同则确认原绑定，不创建新的 Tool；不同则拒绝。不可变名称通过 Tool.Name 读取。绑定确认不代表配置就绪，不改变关闭状态，也不允许重新打开已释放的资源。测试还验证配置后重试不重开 Provider、四种绑定冲突均被拒绝、关闭后重试不使执行恢复。

`go test -race -count=1 ./tools/... ./toolaskuser/... ./systemprompt/... ./agentloop/...` 通过。此修复不替代 Agent Registry 的创建准入或同名 ToolActor 移除后重建的代次保护，生产工具生命周期与 INT-23 仍待完成。

## INT-27 配置登记后候选可见性仍可被覆盖（已修复）

本轮配置登记接入后，`TestRegistryRetainsMergedConfigurationUntilSettlement` 新增隐藏配置检查，修复前失败：配置 Visible=false 的 ask 候选以 Visible=true 登记仍成功。原目录登记只有名称、schema 和重试冲突校验，尚未约束新加入的配置事实；因此 Agent 覆盖隐藏的工具可能错误进入模型工具目录。

Registry.Register 现在按候选名称定位该批次的配置，拒绝可见性不一致，校验失败不写候选。此规则由业务实现，Grain 不重复判断。业务回归及真实配置 Grain、ToolActor 执行链路均通过：`go test -race -count=1 ./tools/... ./userquestions/... ./toolaskuser/... ./systemprompt/... ./agentloop/...`。

配置消息现已实现 InitializeDefaults、RegisterConfiguration、ReadConfiguration，事件为 ConfigurationRegistered，明确表示登记事实。PubSub 不可用时配置保持已登记且可精确查询。ToolActor 测试已由手工 Begin 改为真实配置登记、读取 Provider 后再构造与发布；生产配置事件消费、动态全局更新及 Agent 生命周期接线仍未完成，INT-23 保留。

## INT-26 执行只读视图仍具有调度权限（已修复）

复现：`tools/policy_test.go` 的 `TestPolicyExecutionViewCannotDispatchPreparedCall` 在前置策略中保存 Execution() 返回值，正常 Prepare 完成后将该视图交给 Scheduler.Dispatch。修复前测试失败：工具体被调用一次且返回 nil error。

根因：cloneExecution 仅复制参数字节，仍保留指向 scheduledExecutionState 的私有 state；该指针同时满足 Scheduler 的归属和阶段校验。字段不可导出并不意味着持有整个值的扩展无法把它交回公开调度接口。

影响：扩展可用声明为只读的视图消费原本交给执行编排方的阶段，导致非预期工具执行或合法调用方随后收到阶段错误。这不是审批前自动绕过审批的证据：复现发生在 Prepare 已允许派发之后。

解决：视图统一清除 state，Pre/Execute/Post 请求构造时也直接存放脱离状态的副本；调用关联 Token 不变。真实 Scheduler 句柄、内部执行终点仍保留必要状态，没有引入第二套权限表或 Actor 状态。

验证：回归测试同时证明视图派发被拒绝且工具体未执行、真实句柄仍能派发并完成 Finalize。`go test -race -count=1 ./tools/... ./toolaskuser/... ./systemprompt/... ./agentloop/...` 和 `go vet ./tools/...` 通过。此结果不覆盖生产生命周期接线，INT-23 仍未解决。

## INT-25 ToolActor 客户端向自己同步投递导致请求超时（已修复）

复现：新增真实 Grain 客户端测试 TestClientReceivesActualBodyResultAndBusinessFailure 与 TestClientDoesNotTreatInvalidReplyAsCompletedBody，均失败为 request timed out。最初的 Then 回调在调用方邮箱轮次中通过 ActorSystem.TellGrain 给自己投递结果。

根因：本地 TellGrain 等待目标处理消息并确认；调用方正占用同一邮箱轮次，无法处理自己正在等待确认的消息。影响：即使工具已经返回，调用方也迟迟收不到最终结果，进而触发外部超时。

修复：先用 PipeToSelf 启动回复等待任务，Then 只向有缓冲的内部结果通道交付一次结果，由框架任务回送具名消息。当前轮次不等待自身邮箱。Client.Dispose 终止客户端等待任务，不表示远端工具体已关闭。工具业务 error 保留在 CallResult.Body；框架错误或无效回复使用 CallResult.Err，避免把未确认的执行状态当成已结束。

验证：两项原失败测试及新增取消测试通过；取消确认后，原调用仍等待工具体退出。`go test -race -count=1 ./tools/... ./toolaskuser/... ./systemprompt/... ./agentloop/...`、Tools vet 与差异检查通过。该结果只覆盖工具客户端及相关模块，INT-23 的生产初始化缺口仍未解决。

## INT-24 候选撤销失败后仍可提交（业务测试已修复）

复现：新增纯业务测试 TestFailedAbortCannotCommitPartiallyReleasedCandidate，Resource.Close 返回部分释放错误后调用 Commit，测试失败 `partially released candidate was committed`。这是新单工具业务实现中的缺陷，尚未进入生产链路。

根因：原实现只保留候选资源，没有保存已开始撤销的业务决定；资源释放返回 error 不保证资源毫无变化。影响：后续提交可能激活一个已经部分关闭的连接或进程。

修复：Abort 开始时标记候选不再允许提交，失败与取消都保留待清理资源，只允许继续清理。Close 仍尝试所有资源，成功项不重复释放，失败项可重试。业务对象独立于 Actor。

验证：Tools、ToolAskUser、SystemPrompt 和 AgentLoop 的非缓存 race 测试通过；新增测试覆盖部分释放失败后拒绝提交、取消后的资源保留、关闭失败重试、旧版本拒绝和定义字节隔离。该结果不证明 ToolActor 调度与生产工具初始化已经完成，INT-23 仍未解决。

## INT-23 实际配置 owner 下 Agent 创建失败（未解决）

复现：新增 internal/server/agent_creation_test.go，使用真实 ModelConfig、Prompt Registry、Tools Registry、Session Registry、Agent Registry 和 SQLite，不使用 configurationOwner。运行 `go test -race -count=1 ./internal/server -run '^TestAgentCreationWithProductionConfigurationOwners$'` 明确失败：`production Agent creation failed: systemprompt: tool definitions are not ready`。失败后的 closeAgents 清理成功。

根因：Agent Registry 的启动阶段直接从 Session 初始化进入 Prompt.InitializeRuntime；后者要求 Tools Registry.Read 返回已经发布的该 Agent 工具目录。真实 Tools Registry 仅实现目录发布事务，没有按全局／Agent 配置准备 Tool Runtime 和 ToolActor 的生命周期接线。既有 agent/registry 创建夹具直接返回 Found=true、Version=1，掩盖了实际配置 owner 之间缺失的先后依赖。

影响：此前局部测试通过不代表真实 Agent 可以创建；生产入口即使完成 HTTP 装配也会在创建时失败。问题超出 API 客户端本身，需要补上工具生命周期和启动顺序。此验收测试保持失败，不 Skip，也不把预期改成 ErrToolsNotReady 来伪装通过。

修复方向：按 [工具实现方案](08-tools-implementation.md)，由工具生命周期 owner 根据全局配置与 Agent 覆盖准备 Tool Runtime／ToolActor，完成目录发布后再初始化 Prompt；Agent Registry 统一启动、失败回滚和关闭。不得直接在 API／组合根发布空工具目录，也不得让 Prompt 忽略缺失定义。后续须同时验证真实默认工具、启动失败清理、工具执行和配置更新屏障，而非只使该创建测试变绿。

已完成前置拆分：ToolRunContext.Execution 原先含 scheduledExecutionState，并可达 executionRuntime 和注册项，不能直接跨 Actor 传递。改为独立 Invocation，工具体输出使用 BodyResult 返回值、error 和效果；Runtime 再采纳效果并保持原流水线行为。新增参数／结果隔离、调用间效果隔离和晚到回调测试。`go test -race -count=1 ./tools/... ./toolaskuser/... ./agentloop/...` 通过；该结果不包含且不解决上述生产 Agent 创建失败。ToolActor 与 Tool Runtime 的生命周期仍待实现。

单工具业务已增加 Schema 和 Provision：Schema 提供候选定义副本；Provision 通过 Provider.Open 构造实现，负责校验、接管、取消后的清理和清理失败时的资源保留。纯业务集成测试验证 Schema → Registry.Register → Publish → Tool.Commit 的先后契约；资源测试验证部分构造失败、校验失败、取消及重复准备不会丢失资源或重复构造。此阶段的纯业务测试不包含生产配置装配，不能据此关闭 INT-23。

ToolActor 后续已实现 Provision → Tool.Provision → Schema → Registry.Lookup/Register，登记确认后才回复；Lookup 验证更新所属 Agent。同一 Revision 的登记重试复用候选，CreationID 拒绝旧创建请求。真实 Grain 测试验证构造一次、候选登记、跨 Agent 拒绝、取消等待清理、任务 panic 回复与重复关闭。`go test -race -count=1 ./tools/... ./toolaskuser/... ./systemprompt/... ./agentloop/...` 及 Tools vet 通过。工具体执行消息已接入 Tool.Execute，真实 Grain 测试验证取消等到工具体退出、在途调用禁止资源释放、并发取消隔离和版本拒绝。调用 policy／中间件的 Actor 接线及 Agent Registry 启动顺序仍未完成，INT-23 继续保留。

补充集成验证：`TestToolActorUsesQuestionActorForAnswerAndCancellation` 已接通实际 ToolActor → UserQuestions Client → UserQuestions Grain，正常答案及取消等待 Provider 退出均通过；资源不能在问答尚未退出时关闭。`go test -race -count=1 ./tools/... ./userquestions/... ./toolaskuser/...` 通过。本次重新运行真实配置 owner 的生产创建测试，仍失败于 `systemprompt: tool definitions are not ready`；根因仍是配置与初始化顺序未接入，没有用空目录基线绕过。

Registry 配置业务进展：InitializeDefaults 显式登记启动基线；Configure 将全局默认值与 Agent 完整条目覆盖合并到同一批次；ReadConfiguration 提供指定更新快照。测试验证未初始化拒绝、配置登记不发布目录、切片隔离、未结算限制及结算清理。`go test -race -count=1 ./tools/registry/... ./tools/tool/...` 通过。尚未增加配置消息/事件、动态全局更新或生产创建接线，INT-23 仍未解决。

## INT-22 Workspace 的 Session Header 端口只有测试实现

缺失路径：Workspace Registry 的初始化、成员附加和 CWD 验证消费 SessionHeaders，但现有接线仅由测试夹具提供 Header。生产组合根无法提供实时与持久化 Session 的统一元数据来源。

方案：workspace/actor.Headers 通过现有 Session Registry 查找实时 Header，通过 persistence.Reader 读取持久化 Header；Get 优先实时，List 分页读取持久化结果再覆盖实时同 ID，排序并复制返回。适配器不做成员归属判断、不创建 Session，不增加反向 owner 引用。它由 Workspace 的后台业务任务调用，读取错误不得当作不存在吞掉。

验证：`go test -race -count=1 ./workspace/... ./apiproxy/workspace/...` 通过。新增实际 Session Registry／SQLite 集成测试验证未持久化空 Session、Header 副本隔离、关闭后持久化读取、恢复后的实时与持久化去重、缺失和取消。生产 Server 的具体注入仍待完成，测试不宣称整个进程已启动。

## INT-21 进程关闭缺少稳定的 Agent 清理入口

缺失路径：Registry 只提供单创建 CloseAgent，生产组合根没有取得全部剩余根创建并停止新准入的契约。仅查询当前就绪 Agent 会遗漏未完成初始化及失败后仍保留资源的创建；边查询边允许新建则无法确定关闭集合。

方案：业务 Registry.StopAdmission 永久停止本次目录的新注册及普通准入，保留成员关系；Registry Grain 同一轮次返回剩余根 Creation。internal/server.closeAgents 仅调用这些根的现有 CloseAgent，子树关闭和 Session flush 继续由 Registry 编排；部分失败合并返回，重试再次读取剩余根，不另存 Agent 表、不提前停用框架和数据库。

验证：`go test -race -count=1 ./agent ./agent/registry ./internal/server` 通过。新测试用真实 Registry、父子登记和执行关闭故障夹具验证根顺序、禁止新根及子注册、其他根独立关闭、失败根保留、重试后子树移除及重复关闭。原 Registry 测试覆盖已初始化组件和 Session 持久化关闭；新测试不冒充进程 HTTP 停止或活跃 turn 取消的完整验收。

## INT-20 LLM 配置初始化没有注册默认 Provider

缺失路径：internal/server 已能安装 ModelConfig，但没有构造并注册 DeepSeek Adapter。配置存在不代表 worker 能选中 Provider，因此仅配置初始化测试无法证明模型调用可用。

方案：initializeDeepSeek 使用现有 Connection、Identity、Adapter 构造契约，向同一个 LLM Grain 注册 deepseek-official；保留 RegistrationID 与 error，既不注入密钥值，也不保存 Agent 配置。组合根继续使用已有 SpawnPool 初始化 worker router。

测试中首次未启用 PubSub，Registry 返回非空注册 ID 和“registration committed but PubSub is unavailable”错误。根因是测试 ActorSystem 不满足生产方案的 PubSub 前置条件，不是注册未发生；修正测试装配启用 PubSub，未把已生效错误吞掉或盲目重试注册。

复验：`go test -race -count=1 ./internal/server` 通过。新集成测试使用真实 Credentials Actor、LLM Grain、两个 workers、真实 DeepSeek Adapter 和本地 HTTP SSE 服务；验证无密钥初始化不发请求、后写凭据能用于调用、所有流读取经相同 CallID 路由、仅产生一次 HTTP 请求且返回 hello，最终收到成功 CloseCall 确认。测试不覆盖远端 DeepSeek 可用性或 AgentLoop 到 Session 的完整对话链。

## INT-19 Session 提交订阅与关闭竞态

缺失路径：Feed 已能转换提交事件，但没有 PubSub 订阅者；因此现有 Feed 单测不能证明 Session 发布的通知会到达 API。新增 Subscriber 将 CommitNotice 交给 Committed 消费接口，异步任务串行执行，后续通知使用框架 stash，启动等待 SubscribeAck。

首次 race 测试在 Subscriber.PostStop 与 Receive 之间失败：前者读取 active，后者处理任务完成时清空 active。GoAkt v4.5.3 外部 PID.Shutdown 的 PostStop 路径与 dispatcher 的 Receive 可重叠，不能把 PostStop 当作同一邮箱中的下一条消息。影响是关闭可能竞争当前任务的取得与释放。

修复：关闭边界加锁，先取消生命周期 Context，再等待已登记任务退出；Receive 在同一保护范围内检查取消，停止后不新增消费。工作线程只持捕获的消费者、事件和完成通道，不访问 Actor 状态。未通过增加自管消息队列解决排序，也未修改持久化或下行协议。

复验：`go test -race -count=5 ./apiproxy/session/events/actor` 通过，包含真实 PubSub 到 Feed、阻塞首事件时后续事件不能先发送、Shutdown 取消并等待消费退出。Session 创建／移除、Agent 状态及生产端到端连接仍待实现；这不是完整下行验收。

## INT-18 Commands API 缺少生产目录解析

根因：API 只定义了 Resolver，测试替身同时承担准入和能力返回；实际 commands/actor.Client 虽可执行，但没有实现从 Agent Registry 获取已就绪 Commands 地址的 API 客户端。Session 普通 Agent 准入封装在私有流程中，不能直接绕过它读取 Registry。

影响：生产组合根无法提供 Commands 依赖；直接按服务名构造客户端还会绕过持久化恢复和当前父子归属检查。

方案：Session Gateway.ActivateAgent 复用原普通准入及错误映射。Commands Gateway 先调用 Access，再调用 Resolver；API Server 连接两个 Gateway。新增 commands/actor.Directory 查询 Registry、核对身份与当前归属，返回固定 CreationID 的现有命令 Client，不启动 Actor、不维护路由缓存，也不增加 AgentLoop 到 Commands 的依赖。

验证：`go test -race -count=1 ./apiproxy/... ./internal/connection/... ./internal/server ./commands/actor`、`go vet ./apiproxy/...` 通过。新增测试覆盖持久化恢复、live child 拒绝、准入失败不访问目录、路由身份校验及客户端使用正确 CreationID；目录映射使用协议测试 Grain，命令执行与取消由 commands/actor 原有真实 Grain 测试覆盖。Markdown 本地链接与 `git diff --check` 通过。尚未验证真实 HTTP 到 Compaction 的完整链路；进程组合根仍需注入 Directory。

## INT-16 暂缓 Subagent 阻断主线 API 编译

根因：根 `apiproxy` 仍保存 Bound API，而其契约导入暂缓迁移的 `subagent/bound`；Plugin 删除后，该依赖使不使用 Bound 的 API 主线也无法构建。

影响：`go test ./apiproxy/...` 在加载根包时失败，无法独立验证已完成的 Session、LLM、Workspace 与 Connection 迁移。

方案：删除根包 Bound API 和对应测试，不创建不可编译的迁移副本，也不保留别名。默认 `apiproxy.Server` 本来没有注册 Bound，因此实际主线方法集合不再变化。Bound wire 能力随 Subagent 重构暂缓。

验证：代码提交 `0a889b1` 后，`go test -race -count=1 ./apiproxy/... ./internal/connection/... ./internal/server ./web` 全部通过。此结果不覆盖 Subagent。

## INT-17 共享资源初始化仍归属旧 Assembly

根因：Credentials、LLM 配置与 Prompt 已有 Actor owner 和客户端，但构造函数仍放在待删除的 `internal/assembly`，名称和错误也继续表达旧 Plugin 装配职责。

影响：新生产组合根无法逐步取得共享 Actor 资源；继续复用旧包会把 Factory Catalog 生命周期带入新入口。

方案：将三项初始化迁入 `internal/server` 私有函数。函数构造并验证纯业务 owner、初始化共享 Grain、返回领域客户端；不启动 ActorSystem、不开放网络、不提供 Assembly 转发。模型配置初始化不等于 provider worker 已可调用。

验证：代码提交 `6e64148`。独立暂存快照运行 `go test -race -count=1 ./internal/server`、`go vet ./internal/server` 和 `go build ./internal/server` 通过；无密钥测试验证 Credentials 缺失按业务错误返回，Prompt 初始化不依赖密钥且重复初始化被拒绝。


## INT-15 Plugin 框架退役节点

按用户要求单独删除 Plugin Runtime、Factory、示例和对应旧框架测试，同时移除 Web 的 Plugin 集成，代码提交 `ec70b99`；不保留兼容层。`actor-plugin-removed` 是重构节点标签，不是生产可运行版本。

编译审计 `go test ./... -run '^$'` 未通过：旧 internal/assembly、cmd 和暂缓迁移的 subagent 仍有 Plugin 引用。API 根包保留的 Bound API 也通过 subagent/bound/inbox.go 间接依赖 Plugin，所以此前 API 子树通过的结果不能用于证明删除后的 API 可编译。

后续需完成生产入口替换，并将暂缓的 Bound/Subagent 入口与当前 API 主线隔离；不恢复旧 Plugin 来绕过编译失败。移除前 Web 的非缓存 race 测试已通过，本次删除后只完成差异与依赖审计，不宣称全项目测试通过。


## INT-29 工具移除失败后仍被重复创建复用（已修复）

发现：新增 Agent Registry → ToolActor 生命周期集成时检查 RemoveTool 与 CreateTool 的交错。资源 Close 失败后原实现仍保留 toolInitialized，CreateTool 因此可能返回已经开始关闭的实例。

根因：已初始化同时被当作可复用和可重试关闭两个状态；清理失败没有保留不可逆的移除意图。

方案：增加 toolClosing，仅表示已开始移除但资源关闭尚未确认；CreateTool 拒绝复用，RemoveTool 继续清理原实例。资源关闭确认后进入 toolReleased，仅重试框架停用。创建、移除结果分别使用 toolCreationResult、toolRemovalResult，避免把任务返回误称为整个生命周期完成。

验证：grain_tools_test.go 的 TestToolRemovalFailureRejectsReuseUntilCleanupSucceeds 使用真实 Registry、ToolActor 和可注入关闭错误的资源，覆盖失败后拒绝复用、重试成功及同名新工具获得新 CreationID。TestAgentCloseRetainsToolCleanupFailureAndRetries 验证 Agent 不会在工具关闭失败后释放身份。强制 PoisonPill 的测试明确预期后续关闭报错：新激活不能冒领旧工具登记，测试不把系统停止当作业务清理成功。

初始化首次外部 Ask 也复现 INT-06 的请求信封问题；ToolActor 在 GrainOf 时配置重入，Initialize 通过 RequestGrain 向 Agent Registry 声明激活身份。延迟回复不再依赖 Receive 内临时启用重入来改变已收到的请求。

本轮验证：go test -race -count=1 ./tools/... ./agent/registry ./agentloop/... ./systemprompt/... ./toolaskuser/... 通过；go vet ./tools/... ./agent/registry 与 git diff --check 通过。新增测试最初因重复声明 askLifecycle 无法编译，已删除重复夹具并复用既有函数。这些检查不证明生产 CreateAgent 已接通 Tools Runtime，INT-23 和完整生产调用链仍未完成。


### INT-23 后续进度：实际工具更新端口

已实现 tools/runtime/actor.Instances，工具创建和移除交给 Agent Registry，配置应用及候选清理交给 ToolActor；业务 Update 不依赖 GoAkt。TestUpdateUsesToolLifecycleForReplacementRollbackAndRemoval 经真实 Actor 验证首次发布、替换、构造失败回滚、删除及资源释放次数。新增测试最初误用 RegisterConfiguration.Tools，已按现有 Overrides 契约纠正。go test -race -count=1 ./tools/runtime/actor 与 go vet ./tools/runtime/... 通过。

生产初始化仍未接通。最近一次 go test -race -count=1 ./agent/registry ./agentloop/... ./apiproxy/... ./internal/server 中前三个范围通过，internal/server 的 TestAgentCreationWithProductionConfigurationOwners 仍报 systemprompt: tool definitions are not ready。下一步需实现 Runtime 的 Actor 编排及其生命周期接线，不能以手工预发布目录替代实际生产初始化。


## INT-30 Runtime 生命周期接线的反向实现依赖（已移除）

发现：Instances 客户端直接导入 agent/registry 来使用四类工具生命周期消息；后续 Agent Registry 若直接启动 Runtime Grain，会形成 registry → runtime/actor → registry 包依赖环。

修复：CreateTool、ToolInstance、RemoveTool、ToolRemoved 移入既有 agent/actor 契约包；父 Agent 身份由 AgentID、AgentCreationID 表达，独立工具身份不变。Registry 与 Runtime 客户端分别依赖契约，删除旧声明并迁移调用和测试，无兼容别名。未更改 wire 或持久化。

验证：go test -race -count=1 ./agent/registry ./tools/runtime/actor ./agentloop/actor 通过。TestUpdateUsesToolLifecycleForReplacementRollbackAndRemoval 增加删除失败后重试、目录版本和资源次数断言，通过真实 Actor 验证原身份复用与更新保留。此变更消除了接线障碍，尚未实现 Runtime Grain，生产初始化失败仍归 INT-23。


## INT-31 Session 登记回复无法确认时遗漏清理（已修复）

复现：TestUnconfirmedSessionRegistrationStillClosesItsReservation 将请求转发到真实 Session Registry，登记成功后仅损坏回复。修复前 Agent 关闭返回成功，但新 CreationID 登记报 session: identity is reserved or retired，证明旧登记泄漏。

根因：activation.sessionRegistered 只在成功回复后置位，关闭又仅按该标记执行 Session 清理；创建操作同时承担临时执行状态与长期组件登记。

修复：installation 独立保存组件地址和 Session owner。发送登记前记录清理责任；无法识别或传输错误的回复保留责任，正常拒绝回复撤回本次责任。关闭从 installation 取得资源，与启动游标分离。原创建身份的关闭成功后，新身份能够重新登记。

验证：专项测试修复前稳定失败、修复后通过；go test -race -count=1 ./agent/registry ./agentloop/... ./tools/runtime/actor ./apiproxy/session/actor 全部通过，go vet ./agent/registry 和 git diff --check 通过。此变更未完成创建依赖顺序调整，不表示 Runtime Grain 与生产工具初始化已经接通。登记未到达与其他身份占用的进一步查询契约仍需明确，不吞掉关闭错误。


### INT-23 后续进度：工具集合状态归业务 owner

新增 toolruntime.Runtime，保存每 Agent 当前集合和版本、未完成 Update、最近一次完成结果。真实 Actor 更新集成改为从 Runtime.Apply 驱动，覆盖结算后重复请求、不重复 Open、未完成清理阻止后续更新、未完成期间拒绝快照、回滚保留版本和快照切片隔离。go test -race -count=1 ./tools/runtime/... 通过。该业务对象不含框架引用，尚未实现 Runtime Grain 和生产生命周期初始化。


### INT-23 后续进度：Runtime 关闭业务

Runtime.Close 已实现关闭准入、未完成更新清理和工具逐项移除。新增业务测试验证发布前回滚与发布后继续清理，失败保留责任，重试和重复关闭不重复已确认操作。go test -race -count=1 ./tools/runtime/...、go vet ./tools/runtime/... 与 git diff --check 通过。测试使用 Registry 业务和故障端口；Runtime Grain、目录关闭清理及生产接线仍未完成。


## INT-32 同一 Session 重建工具实例时拒绝保留目录版本（已修复）

复现：`TestNewRuntimeRebuildsInstancesAgainstRetainedCatalog` 先发布工具目录、关闭旧 Runtime，再用新 Runtime 应用基于保留版本的配置。修复前首次更新报 `configuration does not match this Agent and current version`，导致关闭后的 Session 无法按已有目录版本重建工具实例。

根因：Registry 保留已发布目录及版本，新 Runtime 却将零值 Snapshot 当作自己的有效版本，强制要求首次配置基于版本 0。保留目录只能证明过去的发布事实，不能证明新 Runtime 已经持有对应实例。

修复：Runtime 在首次完整更新成功前没有当前快照；首次更新采用 Registry 已登记配置的 ExpectedVersion，原子发布仍由 Registry 校验。首次准备失败并完成回滚后仍不能读取快照；后续成功重建才提供新版本及工具集合。已有有效快照时继续要求版本匹配，不放宽并发发布条件。

Catalog 客户端增加 Version 和 Configure：无目录返回版本 0，已发布空目录返回真实版本；配置已登记但通知失败时同时返回登记事实和错误，调用方不能把通知错误当作登记失败。

验收覆盖重建失败回滚、再次重建成功、实际新实例准备/提交、空目录版本和 PubSub 失败保留配置。该修复是 Runtime 业务与 Actor 客户端契约修复；Runtime Grain、生产创建链和 AgentLoop 更新屏障仍未接通，不代表 INT-23 已解决。


## INT-33 工具目录版本与单个 Schema 版本混用（迁移中）

用户确认 Definitions 去掉嵌套、每个 ToolSchema 独立维护 Version，不保留目录 Version/Revision。原实现使用整套目录版本同时承担 Schema 识别、发布校验和 Prompt 去重，使单个工具变化影响所有工具的版本表达。

Registry 已移除 ExpectedVersion 与目录版本字段，使用原有的每 Agent 一个未结算更新约束原子发布。Schema 版本按 SessionID 和名称独立分配，全部校验成功后才提交；删除重加递增，隐藏恢复保留未改变 Schema 的版本。Definitions 查询和发布使用同一扁平值，模型 JSON 不包含内部 Version。没有旧类型别名。

验证：`go test -race -count=1 ./tools/registry/...` 通过，包含真实 Registry Grain 发布、查询及通知失败后保留提交事实，以及单个 Schema 版本业务测试。

边界：随后执行 `go test -count=1 ./tools/runtime/... ./systemprompt/... ./agentloop/actor` 编译失败，明确定位 Runtime 对 ExpectedVersion、Prompt 对目录 Revision 的剩余依赖。迁移尚未完成，不以静默默认值、兼容字段或 Schema 最大版本修复。INT-32 的目录版本续接机制随本次确认废止，其“新 Runtime 必须重新获得实际实例后才可提供快照”的业务约束仍保留。


### INT-33 后续：Catalog 契约与执行选择依赖

Catalog.Configure 已移除目录版本参数，Version 查询方法已删除；Actor 测试改为检查空集合的 Found、通知失败保留登记、未结算更新排他及结算后允许下一次登记。未提供兼容字段或替代版本。

进一步调用链核对发现 Tool.Revision.Version 同时用于 Runtime 更新关联和 Tool.Definition/Execute 的实现选择。Schema 不变的 Provider 更新仍会替换执行实现，故不能把此数值机械改成 Schema.Version。这个依赖需要连同旧更新迟到防护一起迁移。当前仅完成代码和契约调整，Runtime 原 ExpectedVersion 依赖仍导致整包编译失败，不能声明 Catalog 新测试已通过。差异检查通过。


### INT-33 后续：工具实现身份迁移通过，Prompt 尚未完成

Runtime Snapshot 移除目录 Version，返回已结算 UpdateID。tool.Revision 改为不可排序的具名字符串，Prepare/Commit/Retire/Abort/Definition/Execute 使用准确更新身份。相同 Schema 更换 Provider 时 Schema.Version 不变，但旧实现请求被拒绝，新实现请求成功。

ToolActor 在 Open 前核实有效更新，阻止已结算更新和其他 Agent 的更新构造资源。查询回复经邮箱继续，移除旧的 Then 内使用保存 GrainContext 发起后续请求。新增迟到更新测试保证旧请求不干扰当前实现及资源释放。取消测试补齐有效批次；panic 测试增加 Open 次数断言，避免请求被前置拒绝却误判已验证 panic 清理。

验证：`go test -race -count=1 ./tools/tool/... ./tools/runtime/... ./tools/registry/...`、对应 `go vet`、`git diff --check` 通过。Prompt 仍依赖旧目录版本，跨模块初始化尚未通过；扩大检查中的 Agent Registry 测试超过预期时间后主动中断，另用 20 秒超时定位。未声明全项目可运行。


### INT-33 提交前扩大检查的准确结果

`go test -count=1 -timeout=20s ./agent/registry ./systemprompt/... ./agentloop/actor` 未通过：Prompt.Initialize 仍调用要求非零目录版本的旧业务 ApplyTools，输入已不再提供目录版本，报 `systemprompt: tool definition version mismatch`。Agent 创建和 AgentLoop 初始化因此提前失败。Registry 的 TestComponentFailurePreservesSessionAndRetriesReverseShutdown 在等待尚未到达的组件阶段通知时触发 20 秒超时；这是上游初始化失败后夹具无界等待，不能解释为 GoAkt dispatcher 整体阻塞。后续应完成 Prompt 契约迁移并将该夹具等待改为有界断言。该失败不通过填入版本 1 或保留兼容字段绕过。


### 测试专用入口清理与 Prompt 业务迁移

引用审计确认 Tool.Prepare 仅测试调用、Update.Revision 无调用方，已删除。Tool 测试改经生产 Provision 入口，复用 Provider 夹具，保留切换、撤销、关闭和资源释放断言；无效实现由 Provision 在返回前清理，后续 Close 不得重复释放。`go test -race -count=1 ./tools/tool ./tools/runtime ./tools/registry` 与对应 vet 通过。

Prompt 纯业务 Runtime 已删除整套 ToolsVersion，用缺失工具基线与已安装空集合区分未就绪；ApplyTools/Assemble 不再接收目录版本。修复 detachToolSchema 和组装时丢失 Schema.Version 的问题，`go test -race -count=1 ./systemprompt` 通过。Actor 消费和 AgentLoop 调用仍需迁移，业务测试不能证明整条链路可用。


### INT-33 Prompt 与 AgentLoop 消费契约迁移通过

Prompt Actor、RuntimeInitialized/RuntimeView/Assemble、AgentLoop Initialize/Preparation/Step/ExecuteTools 已移除 ToolsVersion，不保留兼容值。Prompt Runtime 缺失基线时拒绝组装，空集合成功安装后可组装；Step 仍冻结整个 Prompt 快照并拒绝改变既有提案。

直接消费旧通知的回归测试先复现旧成员覆盖当前集合，修复为只用通知触发 Registry 重读；订阅器串行执行读取及应用，并合并期间收到的重读请求。测试覆盖删除后迟到通知不复活工具、在途应用期间多次更新后消费最新空集合、取消订阅先于在途结果返回，以及 Schema.Version 在 Prompt 中保留。

扩大 race 检查暴露 Registry 关闭夹具的另一处时序假设：组件事件先到达不证明 Registry 已消费失败回复，显式关闭可能仍共享自动清理。夹具改为收到失败回复后明确重试，并将阶段事件等待改为有界断言，没有改变生产关闭语义。

`go test -race -count=1 -timeout=30s ./systemprompt/... ./agentloop/... ./agent/registry ./tools/...` 通过；补充订阅串行化测试后重新运行 systemprompt/actor 和 agent/registry 的 race 检查通过，对应 vet 与差异检查通过。仍不表示 Tool Runtime Grain、生产工具初始化和执行接线已完成。


后续扩大验证：`go test -race -count=1 -timeout=30s ./apiproxy/... ./internal/server` 中 API Proxy 全部通过；internal/server 的 TestAgentCreationWithProductionConfigurationOwners 仍返回 `systemprompt: tool definitions are not ready`。Prompt 旧版本契约问题已解决，此失败仍是 INT-23 的生产 Tool Runtime 初始化缺失，不能用预置空目录替代。


### INT-23 初始化登记的业务责任

新增 Runtime.Initialize，将首次配置登记、准备、发布、切换及结算归入业务对象。发送登记前保留更新 ID，登记回复丢失时关闭按准确 ID 查询并撤销；查询或撤销失败不能清空责任，初始化失败禁止继续应用新更新。

TestInitializationRetainsUnconfirmedRegistrationUntilClose 覆盖未受理与已登记但回复丢失，故障端口连接实际 Registry 业务；同时注入查询和撤销失败，验证重试完成后批次不存在且从未构造工具。原真实 Actor 实例更新测试首次创建改用 Initialize，验证默认覆盖登记到 ToolActor 初始化和实际实例切换，不再由测试提前登记首次配置。

`go test -race -count=1 ./tools/runtime/...`、对应 vet、差异检查通过。Runtime Grain 和 Agent 生命周期启动尚未实现，生产创建失败仍未解除。


### INT-23 生产 Tools 初始化接线及后续故障

Tool Runtime Grain 接入 Agent Registry 的组件依赖：Session → Tools → Prompt；关闭先确认执行结束，再关闭 Runtime 的批次和实例，剩余单工具责任仍保留在 Registry 账本。Grain 仅调用业务 Runtime 的 Initialize/Close；组件首次激活通过 ClaimTools 固定身份，同名重新激活不能接管旧资源。

真实配置验收依次暴露两处问题：

| 问题 | 影响与根因 | 解决 |
| --- | --- | --- |
| tools: default configuration is not initialized | 只有 Registry 地址，没有显式初始化配置；新 Runtime 无法 Configure | 组合入口增加 initializeTools，空配置也必须明确登记；不提前伪造 Agent 定义 |
| reentrancy is disabled | 真实 LLM InitializeAgent 会请求 Session；initializeModels 初次创建 Grain 未配置重入，替身测试直接返回而隐藏缺陷 | 模型初始化与 provider 注册入口在首次激活时配置 AllowAll，随后由业务对象处理配置规则 |

Agent Registry 创建测试移除 Tools ReadDefinitions 替身，改为真实配置登记和发布。新增真实 Provider 验证资源打开、关闭计数以及构造失败后的资源清理。创建错误先返回，自动清理异步继续；测试通过 CloseAgent 等待清理确认后再请求新生命周期，不将错误回复等同清理完成。新增强制停用 Runtime 后重新激活的测试，检查旧身份不能被接管，关闭不确定时不得释放 Agent 身份。

当前这些变更仅补齐初次配置和关闭；动态工具更新屏障、执行策略迁移、AgentLoop 工具执行和完整服务入口仍未完成。

强制重新激活测试还暴露 GoAkt 自动激活不保留首次 GrainOf 重入选项：直接 Ask 无法取得延迟回复句柄，真实 RequestGrain 则需接收端启用重入才能继续请求 Registry。Runtime 在接收初始化请求时启用 AllowAll，再核对原激活身份；测试使用与生产相同的 RequestGrain，最终得到 ErrInstance，随后 CloseAgent 失败并保留资源责任。没有绕过身份检查或让新实例假称清理成功。

验证：`go test -race -count=1 -timeout=30s ./internal/server ./agent/registry ./agentloop/actor ./tools/...` 通过。真实配置验收覆盖显式空工具、非空 Provider、Provider 返回资源同时失败后清理并重建；未调用真实外部工具或真实模型，不证明完整对话执行链已完成。


### INT-34 工具执行业务仍依赖本地目录

源码核对发现旧 executionRuntime 读取分层 registry 并直接调用 Executor，真实 ToolActor 只有独立调用协议；生产 Tool Runtime 尚未贯通这段业务。迁移为 Scheduler，固定描述及 Guard、policy 快照，通过 Invoker 分离工具体调用。ToolActor Describe 返回不含 Executor 或资源的 Descriptor。

调用结果未确认和工具体正常返回错误不能混淆：前者表示仍需保留执行责任，不能写入普通失败结果并继续对话。新增 Scheduler 测试验证两种错误的分离，未知工具在派发前拒绝。原 Tools 行为测试及 `go test -race -count=1 -timeout=30s ./tools/...` 通过。本步骤仅迁移同一个执行算法与契约，Runtime 执行请求、实际 Invoker 接线和旧 ToolLayer 删除仍待完成。


### INT-34 Runtime 执行接线与快照身份修正

新增 Call 业务对象承接实际准备、分派和最终结果，Runtime Grain 提供具名执行请求，不能由调用方提交伪造结果。Instances 的 Scheduler/Invoker 适配实际 ToolActor，工具体 context 取消不提前结束回复等待。真实调用首次揭示测试 Questions 夹具返回 nil Answers：实际输出校验拒绝 null 数组，已将夹具改为明确的空数组，没有放宽工具输出 Schema。

Snapshot.UpdateID 混合了配置事务与执行视图职责，现已删除。独立 Tool Identity 在真实 Commit 切换时生成，重复 Commit 不改变；Describe 返回 Identity 与不含 Executor 的 Descriptor。配置更新仍以 UpdateID 关联，执行不再复用它。真实替换测试检查 Schema.Version 保持、旧 Identity 被拒绝且新 Identity 可执行。

工作区删除 ToolExecution.Parent/Token 后存在悬空引用；清理剩余导出 token 与构造引用，内部规范化结果用既有执行状态识别原调用，顶层输出以 CallID/RootCallID 判断，不新增进程内 token。原 Tools 行为测试通过。

验证：`go test -race -count=1 -timeout=30s ./tools/... ./internal/server` 通过。生产配置验收增加 Runtime 准备、提前释放/最终处理拒绝、实际工具分派、最终处理及释放，使用真实 Registry 与 ToolActor。尚未证明 AgentLoop 工具批次、审批审计、工具更新屏障或完整对话已接通。


### INT-35 Runtime 取消与异常投递确认

生产配置验收增加可控工具体：进入后等待取消，收到取消后仍等待测试释放。取消返回确认时，原分派请求保持未完成，ReleaseCall 拒绝且 Provider 资源未释放；工具实际退出后再执行 FinalizeCall、ReleaseCall 和 Agent 关闭，最后资源只释放一次。测试验证取消意图与执行完成是两个边界。

调用 Invoker 的 panic 如果被外层中间件恢复成普通 error，会被 Dispatcher 转成工具失败，掩盖执行可能仍在进行的事实。现于投递边界恢复为未确认错误，再由 DeliveryError 传回，Call 保留责任并拒绝最终处理、结果读取和重复派发。Runtime 框架任务也保证 panic 返回关联的完成消息，不遗失请求回复或策略请求占用；不制造 ToolExecutionResult。

验证：`go test -race -count=1 -timeout=30s ./tools/... ./internal/server`、对应 `go vet` 与差异检查通过。Call 回归验证未知结果不能最终处理、不能读取虚构结果且重试 Dispatch 不再投递工具体。AgentLoop 批次、审批审计和动态更新屏障仍待接通。

## INT-36：Tools 根目录仍保留旧注册机制和执行状态

问题与影响：根包同时保存 ToolLayer、分层 Registry/Store、注册句柄和 Scheduler。公共 ToolExecution 内嵌执行状态并引用 Scheduler，使公共策略输入具有隐式调度耦合，执行实现无法直接分包。删除旧入口后，toolaskuser 测试也暴露对旧注册构造的依赖。

根因：Actor Runtime 已接入执行业务，但旧层构造和测试仍未迁移；公共调用数据与包内调度句柄没有分开。Resolve* 同时命名策略评估、执行委托和审批结果应用，无法表达具体职责。

解决：删除旧层、Store、句柄和专属配置；根包保留业务契约与共享校验。Scheduler、Call、策略评估、工具派发和结果处理聚合到 tools/execution。公共 ToolExecution 无状态字段；包内准备句柄与状态只经 Call 使用，底层阶段方法不公开。删除测试专用 Scheduler.Execute，行为测试驱动真实 Call。ToolAskUser 的执行与真实 QuestionActor 测试同步迁移，移除旧注册卸载断言。EvaluateCall、ReviewResult、ApplyApproval、ExecuteBody 按具体职责命名，不保留 Resolve* 别名。

验证：Tools、ToolAskUser、Agent、AgentLoop、Prompt、server 和 API Proxy 的定向 race 测试通过；共享 Schema 契约调整后重新验证 Tools/ToolAskUser，并继续执行受影响路径检查。验证不包括尚未迁移的 subagent/旧 assembly，也不代表 AgentLoop 主驱动或动态更新屏障已经完成。

后续审查项：Runtime Grain 目前保存完整 InitializeRuntime 并用 reflect.DeepEqual 检查重复初始化；该消息包含 Provider/策略接口，比较范围不够精确。本次仅将 input/business 改为 initialization/toolRuntime 并说明职责，没有更改重复初始化协议。

## INT-37：扩大验收范围暴露旧生产入口与架构检查

执行 go list ./... 时，cmd/goren → internal/assembly 仍引用已删除的 agent/factory、agentloop/factory、apiproxy/factory、apiproxy/host、Plugin 及其他 Factory 包，生产入口无法构建。根因是领域迁移已删除旧构造契约，而 CLI/assembly 的整体装配尚未替换。不能通过重新引入兼容 Factory 或获取同名模块解决。

随后选择 97 个包执行 go test -race -count=1 -timeout=60s，明确排除暂缓的 subagent 以及已确认不可构建的 cmd/goren、internal/assembly。选中范围内各业务包通过，tests/architecture 失败。失败包含要求 plugin 目录存在、要求 agentHost.Close/Factory/RegistryService 等已删除对象，以及把已采用的业务 DTO 依赖当作非法导入；还包含有效的命名冲突和常量说明检查。不能将所有失败都当作过时测试删除。

影响：领域定向测试通过不足以证明可运行服务器；旧架构守卫也无法正确描述 Actor 目标。后续须用目标边界替换 Plugin 专用守卫，保留无框架业务依赖、无循环对象关系、命名和注释等仍有效约束，并处理真实违反项。Server/CLI 的最终替换和完整链路验收仍未完成，不把这次排除后的测试称为全项目通过。

## INT-38：生产创建缺少 Title 生命周期和共享标题投影

核对 API Titles.Rename 调用发现，它通过 Agent Registry.Ready.Components 获取已有 Title Runtime；原生产配置集成创建没有任何 Title 组件，因而无法证明标题 API 可用。根因是模块端口迁移已完成，但共享生命周期配置和 Agent 创建模板未纳入该依赖。

增加生产装配函数配置 Title Registry（首次请求前启用 AllowAll）及 Projection 的 title.Unit，真实创建验收将 Title owner 加入 Components。通过 API Actor 客户端执行标题重命名，读取已提交日志和对应投影，再由 Agent Registry 关闭 Title 与 Session，使用 persistence.Reader.Inspect 验证 SQLite 中标题及事件序号。关闭后再次重命名返回错误。覆盖空工具、正常工具、工具初始化失败清理后重建、工具中途取消四种创建路径。

同时在装配边界拒绝“已配置 LLM 标题但 Provider 缺失”，防止无声禁用模型标题。测试验证错误不消耗 Title Registry 的一次性配置权，修正配置后仍可成功初始化。本次配置验收使用显式无模型标题配置；实际默认模型标题 Provider 和 HTTP 主入口仍未接通，不作完成声明。

## INT-39：Workspace Header 查询返回值收敛

Header 查询原来同时返回值、found 和 error，用于区分缺失与读取失败。改为可选 Header 指针和 error；nil、nil 表示不存在，错误不能被当成不存在。返回的 Header 是副本，调用方修改 CWD 不影响实时 Session 或持久化记录。Workspace 的归档与关联分别保留 UnknownSessionError、AttachError 映射，外部业务行为不变。

同步迁移 Workspace 端口、Actor 适配器、两个业务调用点及 API Workspace 测试实现；不保留旧签名。go test -race -count=1 -timeout=45s ./workspace/... ./apiproxy/workspace/... ./internal/server 与对应 go vet 通过。该检查证明 Header 契约及已有 Workspace 集成，不能代替尚未完成的生产入口验收。

## INT-40：失效 Plugin 装配与 CLI 启动链

- 根因：internal/assembly 仍构造已删除 Factory 的 Plugin 树，CLI 仍调用已不存在的初始化函数和 plugin.Runtime；它不是可运行的备用入口。
- 处理：删除旧装配和对应旧协议夹具，删除 CLI Plugin 启停代码；保留 config.go 的参数、路径规则与测试。ProjectionCache 依赖守卫改为禁止依赖当前 internal/server。
- 影响：Actor Server.Start/Close 和 main 尚未完成，CLI build 仍不通过；不增加占位 main。旧端到端测试中的持久化重启、HTTP、压缩和 Provider 覆盖仍须迁移，Bound/subagent 暂缓；详见 41-server-composition.md。
- 复验：待提交代码快照的 Tools、ToolAskUser、Agent、AgentLoop、Prompt、Workspace、UserQuestions、Server、API Proxy 和 CLI 配置通过 `go test -race -count=1 -timeout=60s` 及 `go vet`；Credentials/ProjectionCache 两项边界测试通过。`go build ./cmd/goren` 明确失败于缺少 main；全量架构检查尚有 INT-37 记录的问题，未宣称全项目通过。

## INT-41：Workspace 提交通知未连接 API 下行

- 根因：Workspace Actor 已向 workspace/notices 发布成功提交后的 Notice，Gateway.ObserveEvent 已实现帧转换，但 API Actor 适配包只有操作客户端，没有通知订阅者。
- 影响：仅连接 unary API 时 mutation 能成功，Host 下行不会更新；领域单测或直接调用 ObserveEvent 的测试无法证明 PubSub 接线。
- 解决：新增 workspace API Subscriber，以 Notices 窄接口投递，等待 SubscribeAck 后返回；缺少 PubSub 或启动上下文已取消时拒绝；停止 PID 后由框架清除订阅，不回滚已提交变更。
- 验证：真实 Workspace Actor、SQLite、PubSub、Gateway、Broker 集成覆盖创建、重命名、显式排序、删除、停止后不再下行以及启动拒绝。创建和删除只期待 owner 实际发布的对应事件，排序通知由显式排序触发。`go test -race -count=1 -timeout=20s ./apiproxy/workspace/... ./workspace/...` 与同范围 vet 通过。
- 剩余：最终 Server 启动必须调用 SpawnSubscriber 并等待确认，关闭时在 Broker 之前停止订阅者；生产 HTTP/WebSocket 端到端验收仍未完成。

## INT-42：Workspace 生产初始化缺少 Session 来源连接

- 根因：API Directory 已实现成员操作，Headers 已实现在线/持久化合并，但新组合根尚未构造 Workspace Actor；旧测试使用 Header 替身，不能证明 CWD 和关闭后的成员恢复能从生产 Session 来源取得。
- 影响：即使直接暴露 API 操作接口，仍可能缺少未落盘 Session 的归属信息，或无法验证冷启动成员/归档恢复。
- 解决：initializeWorkspaces 在打开数据库前连接真实 Registry 和 persistence.Reader 并验证 PubSub；成功后 Workspace Actor 取得存储生命周期，失败关闭未移交数据库。
- 验证：TestWorkspaceCompositionRecoversMembershipFromDurableSessionHeaders 使用真实 Registry、两套 SQLite 和 API Directory，覆盖在线空 Session 附加、不同 CWD 拒绝、归档、Session flush、关闭及重新打开后的成员/归档保留。`go test -race -count=1 -timeout=30s ./internal/server` 和 `go vet ./internal/server` 通过。
- 剩余：生产 Server.Start/Close、CLI、Query 来源刷新与完整 HTTP/WebSocket 对话链仍未完成；本项是共享资源组合验收，不代表完整进程可运行。

## INT-43：Query 只在测试中刷新来源

- 根因：Collector 由测试 Grain 启动，生产 API Search 直接请求旧观测；未建立每次查询与刷新之间的串行关系。
- 解决：同一 Query Actor 用框架 stash 保留原请求，框架任务先读取完整 Sources 再 Observe 和查询；删除单次 Collector/启动 Grain，新增 Logs 数据获取适配器。业务 Service 保留来源合并和索引规则。
- 验证：Query/SQLite/API race 测试通过，覆盖真实在线日志、129 条离线分页及源读取失败时返回错误而非旧观测。取消/关闭并发的更广故障覆盖仍需继续。

## INT-44：Server 缺少入口及 ProjectionCache 接线

- 根因：删除 Plugin 装配后 main 尚未重建；初次接线漏掉 Sessions.Cache，导致真实 HTTP 重启测试虽然日志中有标题，但 session.list 未恢复标题视图。ProjectionCache 原本已经实现读写与冷重建，不需要再造无缓存重建分支。
- 解决：补齐 Server Start/Close 和 CLI 信号路径；连接原缓存 Grain、独立 SQLite 与 LoadAll 基线，新增缓存生命周期 Registry 与 API Client，注册 title 和 SessionListMetadata 投影。没有新增重放算法。
- 验证：真实 HTTP 创建/重命名、Workspace WebSocket、关闭监听、持久化重启标题、占用端口失败回滚与重新启动通过；缓存 16 并发取得、写入、重复关闭、保留共享数据库测试通过；相关包 race/vet 与 CLI build 通过。独立 CLI 无 API key、host.describe、SIGTERM 退出 0。
- 环境问题：首次 Python localhost 探测经过系统 HTTP 代理返回 502，关闭探测端代理后直连成功；没有为此修改应用信任协议。
- 边界：这证明控制面入口恢复，不证明完整模型对话、Compaction、默认工具、问答审批、自动 checkpoint 已接通；强制关闭缓存独立读者的底层 I/O 排空仍需验证。

## INT-45：辅助配置失败阻断 Web 聊天初始化

- 现象：历史不自动加载；点击新建对话后列表出现，但输入框仍被禁用。
- 根因：ConversationStore.start 在读取 Session 前等待 credentials.describe 和 bound.list；任一失败进入全局 failed。新建对话独立刷新列表，却不能解除该失败状态。
- 修复契约：Host 与 Session 决定聊天初始化状态；凭据及子 Agent 定义独立加载，失败保留错误提示，不阻断历史、下行连接或输入。不能把失败的配置查询伪装成成功空集合。
- 验收：辅助查询拒绝或未完成时，已有 Session 自动选择、历史加载且页面 ready；主链失败仍按原错误机制处理。构建嵌入的 Web 产物。
- 验证结果：Web 全部 34 项测试、TypeScript 检查、Vite build、Go Web 包测试通过。独立真实 Server 浏览器中确认 bound.list 404 不阻断会话；刷新自动选中原 Session，输入框可点击输入、发送按钮启用。没有以此声称模型生成链路通过。

## INT-46：AgentLoop 生产执行主驱动仍缺失

- 源码证据：grain_inbox.go 确认输入后仅回复 Changed；模型结束后的 CompleteModel、CompleteTools、ConfirmStepEnd 原先仅在业务测试中调用。API 接纳成功不能证明模型和工具执行已发生。
- 影响：页面输入恢复后，仍可能停在已接纳状态；局部中间件、模型流和工具测试不能替代完整 Followup 验收。
- 本次修复：补齐 Step 收尾请求，业务 EndStep 保存私有身份与事件；Session 在 Commit 内做幂等查找，Actor 只发送和确认。PrepareStepEnd 的 Go 返回值及调用测试同步迁移，未改变 step/end 持久化格式。
- 验证：AgentLoop 全部包 fresh race 测试和 vet 通过；Actor 确认丢失重试仅写一个 step/end，并保留原执行错误；业务测试拒绝错 Session、不完整日志、重复边界和已被后续 Step 越过的关闭。
- 未完成：自动 Turn/Step 调度、工具批次调度和完整 HTTP/WebSocket 对话仍需接线，不将本次补齐一个边界记为完整 AgentLoop 完成。

## INT-47：整体测试约束未全部迁移

- 当前 go test ./... 的 subagent 构建失败属于明确排除范围，未为其恢复已移除 Plugin 代码。tests/architecture 有 17 项失败，包括读取已删除 Plugin 包、旧 rla 文件前缀和 agentHost 关闭入口等旧约束，也有常量缺少注释及声明命名问题。
- 处理原则：逐项核对 Actor 目标后迁移架构检查，保留业务与 Actor 解耦、无循环所有权和命名约束，不能通过删掉全部检查获得假绿。本次未把这些失败当作已解决。

## INT-48：提交输入后 Ctrl-C 关闭超时

- 用户现象：聊天无响应，反复 Ctrl-C 后 close Agent context deadline exceeded、request timed out，退出码 1。
- 根因：Followup 已提交 Inbox 并保留 execution activity，生产主驱动未启动 Turn；PrepareClose 将未开始活动当作必须等待的执行，idle 永不触发。不是扩大关闭超时能解决的问题。
- 修复：业务 PrepareClose 冻结准入后，只撤销没有 Turn、Step、准备过程、模型请求或收尾的执行活动，保留已提交 Inbox；存在 proposed Turn 或实际维护/执行时仍按原收尾条件等待。Actor 不自行改状态，也未跳过 Session flush。
- 验证：业务关闭/恢复测试，Server 真实 HTTP prompt 后关闭及 SQLite 重启输入保留，AgentLoop/Agent Registry/Server fresh race 测试通过。独立 CLI 临时数据库：HTTP create→prompt→一次 SIGINT，退出码 0、stderr 为空，实测关闭约 3ms。这个结果只覆盖尚未开始 Turn 的场景。
- 剩余：模型无响应的 INT-46 尚未解决；已开始模型/工具时的 SIGINT 取消和完整收尾还需随主驱动接线一起验收。

### INT-46 补充：初始化缺少执行引用

生产 Initialize 原来仅包含 Session/Prompt，无法发起模型和工具请求。CreateAgent 现要求共享 ModelRouter 与 Middleware，安装对象将已启动的 Tool Runtime、上述共享引用和请求超时交给 AgentLoop；Server 负责启动共享中间件执行 Grain。没有把 Actor 启动下放到 AgentLoop。缺失引用在身份保留前拒绝；再次提供完整引用可以正常创建。

已验证 Registry/Server fresh race：真实创建路径与缺失依赖拒绝。AgentLoop 旧手工初始化夹具仍需迁移到完整依赖；自动执行驱动尚未接入，不能把“引用已提供”记为聊天恢复。

### INT-46 补充：自动执行与下行验收

新增驱动后，`TestHTTPPromptDrivesModelAndPersistsCompletedTurn` 通过真实 HTTP 接纳输入、调用本地 SSE 模型服务，并通过真实 WebSocket 收到递增序号的 Session 事件。测试检查流式片段、包含预期文本的完整回复、Step 结束和 Turn 结束，同时检查 SQLite 中的完整 Turn。`go test -race -count=1 ./internal/server -run TestHTTPPrompt -timeout=25s` 通过。该证据仅覆盖文本回复，不能证明工具、取消、维护和恢复路径完整。

## INT-49：AgentLoop 业务流程分散在业务对象与 Actor

- 源码证据：ReactLoopAgent 的 CompleteModel、ContinueTurn 决定业务分支，但 grain_execution.go 的 advanceExecution 另行串联整个执行周期；grain_step.go 负责输入、PreStep、上下文、Prompt、消息提交之间的流程衔接；grain_tools.go 在 Actor 中创建并推进 ToolBatch、保存调用游标，而且直接选择审批不可用结果。
- 根因：此前拆分为许多 Prepare/Confirm 边界，只把局部校验与状态转换放入业务对象，流程整体仍依赖 Actor 调用顺序。新增驱动又增加一层对自身的请求与回复，放大了这种职责分散。
- 影响：业务测试不能独立证明完整执行流程；修改流程需要同时修改业务对象和 Actor。工具审批、取消与清理存在由 Actor 决策的风险。文本对话集成通过不能抵消此架构问题。
- 已确认边界：Actor 不知道 AgentLoop 内部执行顺序；业务流程直接调用能力接口，由通信适配异步交付原请求结果。不能改成业务返回内部流程指令、Actor 继续解释的另一套驱动。具体设计见 [能力接口驱动的优化方案](./30-agentloop-business.md#能力接口驱动的优化方案2026-09-09)。异步交付、立即失败及无环引用仍待调度验证，当前不记为已修复。

### INT-49 实现进度：业务能力接口与失败边界

- 生产文本路径已切换到 ReactLoopAgent.Run 和异步能力接口；Actor 内部 Turn/Step/模型流程驱动已删除。模型结果直接交给业务对象，删除仅转发的 generation 接收者。
- 业务故障测试证明 Turn start、Step end、Turn end 回复丢失时重试同一事实，日志中各边界只有一个；flush 失败只重复 flush，原模型错误保留且不重复生成。旧开始和 Step 收尾 Actor 测试已迁入业务流程测试。
- 同时发现并修复两处遗漏：开始前取消原来仅报告错误，导致活动停留在 running；维护完成原来丢弃 replay 返回值，导致维护期间接纳的输入不执行。现在前者释放尚未开始的活动并保留 Inbox，后者由业务 MaintenanceCompleted 推进后续输入。
- 验证：`go test -race -count=1 ./agentloop ./internal/server -run 'TestBusinessExecution|TestHTTPPrompt' -timeout=30s` 通过。前者为可控异步能力与真实 Session.Log，后者为真实 HTTP/WebSocket、本地 SSE 提供者及 SQLite；不代表 Actor 测试包全部通过。
- 未完成：其他旧私有流程消息测试仍需迁移；工具审批、并发、取消及清理恢复尚未完整。请求暂存解除到业务结果再次进入邮箱之间，输入与未确认提交的交错仍待验证和修复。没有为中间编译结果恢复旧流程或兼容入口。

### INT-49 补充：旧测试协议清理和中间件接线

旧 Turn/Step/模型私有命令测试已迁移，AgentLoop 全部包、中间件和 Agent Registry 非缓存 race 测试通过。过程中发现生产适配未传递已选中间件，实际调用的是空集合；现在从生命周期接纳 Handlers 并校验、冻结，再由能力适配传递。真实自定义恢复和 Retry actor 都会参与完整 Turn，停止钩子允许 Steer 与取消重试。

模型关闭未确认时，原流程调用 failStep，但状态校验拒绝继续且没有保留恢复入口。现在由业务保留只重试关闭的责任；完整业务测试确认不重复生成、不提前写入结束事件、原错误保留。此项不代表生产自动恢复策略、工具释放及关闭交错已经全部完成。

### 关闭接线补充验证（2026-09-09）

发现并修复：Registry 已发送 PrepareClose，但 AgentLoop 原先仅等待 idle，没有将关闭意图投递给正在执行的能力。现在业务对象记录 disposed 取消，Actor 复用模型、工具、中间件和维护的取消通道；取消投递失败返回错误，成功后继续等待执行收尾。HTTP 流式关闭测试确认模型取消、Step/Turn 持久化、未消费 Inbox 保留。`go test -race -count=1 ./agentloop/... ./agent/registry ./internal/server -timeout=60s` 通过。

临时目录 CLI 验证：创建 Session 后给编译产物发送单次 SIGINT，退出码 0；相同信号发送到 `go run` 进程组，包装进程退出码 1、无应用错误输出。`make run` 使用 go run，因此仅凭 make Error 1 不能推断业务关闭超时。本次尚未验证真实外部提供方，以及所有工具和中间件的生产接线，不代表 AgentLoop 整体重构完成。

工具批次增量验证：完整 Run 覆盖活动工具取消后的未开始调用配对，以及工具调用/结果回复丢失后幂等重试；生成、工具准备、Dispatch、Release 各一次。回复使用 Session 范围与完整草稿校验。`go test -race -count=1 ./agentloop/... ./agent/registry ./internal/server -timeout=60s` 和相关 vet 通过。剩余包括审批接线、附加上下文恢复、并发调度与更多资源失败边界，整体目标未完成。

### 新 Session 流式事件被过滤

真实浏览器先连接 events.mux 再创建 Session 时，Feed.Mux 用 `!exists` 将不在初始快照中的 Session 事件全部丢弃；先创建后连接的测试无法发现。修正为只有已知 Session 且序号不超过已读边界时才跳过。新 Session 第一条事件建立边界，后续仍按 seq 去重，不改变外部帧或持久化格式。验收覆盖两种连接顺序，并在模型尚未结束时确认页面显示每段文本。

页面验证：使用临时数据库运行完整 Server 与嵌入页面，模型测试端点每段后暂停；Chrome 实际显示第一段、第一加第二段、完整三段，三次均处于“生成中”。随后放行 finish。原测试先创建 Session 再打开 WebSocket 已补为两种连接顺序，保留接收文本后才允许模型继续的门控，避免整段缓冲也能通过测试。该验证使用本地模拟模型，不代表真实外部提供方验收。

本次独立服务退出还复现标题生命周期缺口：标题 worker 已停止后，关闭 Agent 仍尝试关闭该 PID 并返回 actor not found（title-worker）。它与新 Session 实时事件过滤分别属于不同调用链，尚未修复，后续需核对标题资源责任与重复关闭契约。


## 中间件取消与原调用完成交错（2026-09-09）

### 现象与影响

在迁移 `Agent Registry → Tool Runtime → ToolActor` 后运行服务器测试，`TestServerHTTPPersistenceAndShutdown` 返回：

```text
close Agent "integration": agent middleware: cancellation requires a live call and cause
```

该错误从中间件取消确认传播到 Agent 关闭准备，导致本次关闭返回失败。记录时为已复现、待修复问题；随后已完成下述修复与定向验证。

### 原因与证据边界

[中间件 Grain](../../agent/middleware/actor/grain.go) 的 CancelCall 分支把“CallID 不在活动表中”和“Cause 为 nil”合并为同一个错误；release 在原调用收尾时删除活动记录。[AgentLoop 取消投递](../../agentloop/actor/grain_cancel.go) 根据自己尚未完成的请求发送取消，并把中间件返回的错误纳入关闭结果。

两端的活动表不是同一个状态：中间件可能已完成原调用并删除记录，但原回复尚未被 AgentLoop 消费，此时 AgentLoop 仍会发送 CancelCall。单个邮箱顺序处理不能保证两个 owner 同时观察到完成。因此，把取消接收条件限定为“远端调用仍然活跃”存在时序缺陷；合法的迟到取消也会被当作关闭错误。

上述缺陷可由代码路径确认；本轮失败日志不能区分实际命中的是记录缺失还是空 Cause。尚需确定性测试确认此次失败分支，并检查 Cause 在投递前是否可能被业务收尾清空，不能将可能的交错直接记为已验证根因。

### 修复方案

1. 分别校验 CallID 和 Cause，保留无效输入的明确错误。空 Cause 不通过忽略错误来掩盖；若来自调用方收尾时序，应在接受取消时固定本次原因，再完成异步投递。
2. 对合法且已经没有活动记录的 CallID，取消接收端按“无需再投递取消”幂等确认；不恢复记录、不重新执行，也不维护仅为识别迟到取消的历史表。
3. 对仍活跃的调用保持现有取消传播和原请求收尾契约。取消确认只表示取消投递处理完毕；AgentLoop 仍依据原请求结果和业务空闲状态确认执行结束。
4. 实际取消通知失败仍返回错误并保留重试责任，不在 AgentLoop 统一吞掉中间件错误。

### 验收与状态

补充确定性测试：原请求已完成后再取消、完成与取消交错、重复取消、空 CallID、空 Cause、活跃处理者取消失败。前两种情况不应仅因活动记录已删除而使关闭失败；活跃工作仍须回复原请求后才能确认退出。

修复后重新运行中间件、AgentLoop、Agent Registry 与服务器的相关测试及 race 检查，并重新验证 `TestServerHTTPPersistenceAndShutdown`。完成情况见下述验证记录，未覆盖项仍保留为待验证。


### 取消修复验证

在 `TestAroundCancellationWaitsForDownstreamAndUnwinds` 中，对正常返回、主动取消和错误回复三种原调用结果分别增加“原请求返回后再取消两次”的验证。修复前这三个场景均稳定返回 `cancellation requires a live call and cause`；因此已确认活动记录被释放后的迟到取消缺陷，无须依赖服务器调度概率复现。原服务器日志本身仍不能用于区分旧的合并错误分支。

修复将 CallID / Cause 校验与活动记录查询分开，缺少记录时幂等确认，活跃记录继续使用原取消通知路径。测试同时确认缺少 CallID 或 Cause 仍被拒绝；既有活跃取消用例继续确认外层中间件不会在下游实际退出前返回。

`go test -count=1 ./agent/middleware/actor ./internal/server -timeout=45s` 通过；随后 `go test -race -count=1 ./tools/... ./agent/... ./agentloop/... ./systemprompt/... ./internal/server -timeout=60s` 通过，包含服务器关闭测试。本轮未观察到空 Cause 的调用方缺陷，未加入额外取消状态或历史表。取消投递失败的独立故障注入、全部关闭交错组合仍不由这组测试证明。


## Checkpoint Writer 空闲退出导致关闭失败

用户报告关闭时报多个 `checkpoint-writer-<uuid>: actor not found`。Cache Grain 长驻，但 `session/projectioncache/actor/grain.go` 的 Writer Spawn 未设置长驻策略。固定 GoAkt 4.5.3 的普通 Actor 默认空闲两分钟后停用；Cache 没有同步释放 PID，后续写入或关闭会按旧名称查找已经停止的 Writer。

修复在创建 Writer 时设置 LongLivedStrategy，由所属 Cache 显式结束它的生命周期，不吞掉关闭错误或新建替代 Writer。新回归测试修复前失败，修复后通过；ProjectionCache 与服务器相关 race 测试和 vet 通过。完整原因、验收范围见 [ProjectionCache 方案](./24-projection-cache-module.md#关闭时报-checkpoint-writer-不存在)。
