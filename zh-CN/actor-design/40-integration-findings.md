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
