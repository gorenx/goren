# Agent 重构实施方案

状态：执行完成，保留为实施基线

制定日期：2026-08-25

进度记录：[Agent 重构进度矩阵](Agent重构进度矩阵.md)

设计依据：

- [Agent 构造与父子生命周期重构方案](Agent构造与父子生命周期重构方案.md)
- [Agent 构造事务与调用流程设计](Agent构造事务与调用流程设计.md)
- [Agent 生命周期与运行期父子所有权设计](Agent生命周期与运行期父子所有权设计.md)

## 1. 文档职责

本文只规定实施顺序、模块边界、删除项和验证 Gate，不记录完成比例。当前状态和证据只写入[进度矩阵](Agent重构进度矩阵.md)。

实施范围包括：

- `session` 与 `apiproxy/session.AgentSessions`；
- `agent` Registry、Factory、Handle 和统一 lifecycle；
- `agentloop` Factory、Agent Scope、AgentLoop 和 Plugin adapter；
- `subagent` one-shot、continuable、Provider、residency 和关闭请求；
- 直接调用方、架构约束、包内文档和验收。

不改变 Host wire contract、Session 持久化格式、Session Event 名称、模型可见日志或 durable lineage 字段。

## 2. 强制约束

### 2.1 破坏性替换，不保留过渡层

- 不保留新旧 `agent.Factory`、Handle 或 Registry 双入口；
- 不保留 Context Custody、Registry owner 查询或 Subagent 第二套 Agent graph；
- 不保留兼容 wrapper、双写状态、feature flag 或旧类型别名；
- 不让业务 Store、Service、Coordinator、Factory、Agent 或 Provider 实现 Plugin lifecycle；
- 一个模块开始迁移后，必须同步更新该模块的真实调用方、fake、fixture 和测试；
- 允许按模块形成本地 checkpoint commit，但每个 checkpoint 都必须是目标结构，不能提交兼容层或临时双轨实现；
- 未经用户明确要求，不自动提交。

### 2.2 必须保持的不变量

- 普通 Agent 与子 Agent 只通过 `agent.Constructor.Create/Resume`；
- Session Store 决定 `session/created`、`session/disposed` 和 append publication 的时点；
- Agent Registry 决定 `agent/created`、`agent/disposed`、exact epoch 和 runtime parent-child 生命周期；
- Session 与 Agent publication 同步、有序、可回滚；
- Session Header 和 append-only log 仍是 durable lineage 与模型上下文的事实来源；
- one-shot、continuable 和未来 parent-bound 只改变 Subagent residency policy，不产生不同 Agent lifecycle 接口；
- 不增加 `session/prepared` 或用 Session Event 隐式触发 Agent 创建；
- Plugin topology 与 Agent runtime parent graph 相互独立。

### 2.3 统一术语

- `AgentEpoch`：Registry 创建并拥有的一个精确 Agent 生命周期实例；
- `AgentTeardown`：Scope 结构销毁向同一 epoch 回报开始/完成的接口；
- `AgentScopeRuntime`：`agent` 消费的单 Agent 运行期端口；
- `agentScopes`：AgentLoop 内部持有所有 Agent Scope Plugin Handle 的结构 owner；
- `Activation`：Subagent continuable 的单次进程驻留业务状态；
- `FactoryRegistration`：一条精确 Agent Factory 注册的生命周期。

不再使用 Agent tree、Provider 代指 Factory、Attachment、Reservation、通用 Lifecycle、Scope host 或 HandleTransferred 描述这些对象。

## 3. 最终职责与调用方向

### 3.1 Session

`memoryStore` 是普通 Go 业务对象，拥有 Prepare、Enter、Announce、Release、Flush 和 Session lifecycle publication 决策。`session.Plugin` 只发布 `LiveStore` 并实现 event dispatch adapter。

`AgentSessions` 是 API Session 到普通 Agent 的用例协调者：根据 durable Session Header 判断 ordinary/subagent，调用 `agent.Constructor` 创建或恢复 root Agent，并安装 API 所需的 model selection；它不保存 Agent lifecycle graph。

### 3.2 Agent

`RegistryService` 是唯一 Agent lifecycle application service：

- 原子检查 Registry admission、选择 exact Factory registration 并创建 exact epoch；
- 调用 Factory 构造或恢复；
- 控制 Agent publication；
- 暴露 Registry、Constructor、ScopeProvisioning、RuntimeDescendants 和 FactoryRegistrar 的最小 consumer view；
- `Shutdown` 永久关闭创建/恢复准入，取消构造并按 child-first 收敛全部 epoch。

`LifecycleCoordinator` 保存 exact epoch、runtime parent-child relation、publication axis、descendant admission、close transaction 和稳定关闭结果。`RegistryPlugin.Dispose` 调用 `RegistryService.Shutdown`；AgentLoop Plugin 不调用 Registry shutdown。

Factory 注册与 Registry 关闭是两条不同边界：`FactoryRegistration.Close` 只撤销该 Factory，并取消/等待由它接纳但未完成的构造；既有 live Agent 仍由 Registry 管理，也允许随后注册替代 Factory。

### 3.3 AgentLoop

`agentloop.Factory` 是普通 Go 构造服务，只实现 `agent.Factory`。`ReactLoopAgent` 是普通 Agent 业务对象。根 `agentloop.Plugin` 解析依赖、创建 Factory、路由 Session runtime-context Event，并持有：

- commit 阶段的 `registrationPlugin`，拥有 exact `FactoryRegistration`；
- `agentScopes`，拥有所有 Agent Scope Plugin 的 exact Handle。

每个 `agentScopeRoot` 是单 Agent Scope Plugin 和 `AgentScopeRuntime` adapter。它持有本 Scope resources、event/waterfall source 和 Agent runtime adapter，但不保存父 Plugin/Handle，不保存 runtime parent graph，也不执行自卸载。

### 3.4 Subagent

Subagent Provider、one-shot Service、continuation Service、Manager 和 Activation 都是普通业务对象。Plugin adapter 只解析依赖、发布 Service、注册 Provider/Tool/Extension 和桥接事件。

Subagent 只保存业务 residency：child ID、exact parent、descriptor、accepted message、settlement 和 managed `agent.Handle`。runtime parent-child relation、descendant admission、child-first 关闭和 exact epoch 均由 Agent LifecycleCoordinator 拥有。

### 3.5 Agent Scope 关闭链

```text
Subagent policy / ordinary caller / Registry shutdown
  -> agent.Handle.Dispose 或 RegistryService.Shutdown
  -> RegistryService / LifecycleCoordinator 关闭 exact epoch
  -> AgentScopeRuntime.Teardown 提出结构关闭请求
  -> agentScopeRoot 通知 AgentLoop.agentScopes
  -> agentScopes 以 AgentLoop Plugin 结构 owner 身份卸载 exact isolated Scope
  -> Plugin Runtime 递归 Dispose Scope subtree
  -> agentScopeRoot.Dispose 清理本 Scope并回报 AgentTeardown.FinishTeardown
```

关键约束：Subagent 不操作 Plugin Handle；Coordinator 不依赖 Plugin；Scope Plugin 不自卸载；只有 `agentScopes` 执行结构命令。

### 3.6 Plugin 回调边界与两条停止路径

Plugin Runtime 保持原有不变量：Apply、Dispose、Event 和 Waterfall 回调都不能同步修改同一个 Runtime 的拓扑，不为 Agent/Subagent 增加例外。

- 普通 managed close 在 Plugin 回调外执行，第 3.5 节调用链同步完成。
- 单独卸载 Subagent Plugin 时，`Service.Disable` 关闭业务准入，为每个 Activation 启动 managed close，并等待 exact Agent 进入 `Closing`；随后返回 Plugin Runtime。Agent lifecycle 在外层 Runtime 操作结束后继续调用 `agentScopes` 完成结构卸载，失败通过独立 failure reporter 汇报。
- Runtime 整体关闭时，Runtime 按结构先卸载 Agent Scope；`AgentTeardown` 把结构事实回报 Registry。Subagent Plugin 随后只收敛尚未由这些结构通知移除的 Activation。

这三条路径共用同一个 Agent close transaction，但不在 Plugin Dispose 中重入 Runtime topology。

## 4. 按模块实施顺序

### M0：不变量与架构 Gate

先锁定 publication、失败回滚、exact Handle、parent closing、child-first close、Plugin/业务分离和旧标识符零引用。测试只断言目标长期语义，不保留旧 Custody、owner graph 或 Subagent graph。

### M1：Session 与 AgentSessions

1. 把 Session Store 与 Plugin adapter 分离；业务 Store 通过 consumer-owned event publisher 请求 dispatch。
2. 保持 Prepare、Enter、Announce 的事务顺序和配对 Disposed。
3. `AgentSessions` 仅通过 durable Header 判断 Session 类型，通过 Constructor 创建/恢复 root Agent。
4. 增加 Session core 不依赖 `agent`/`agentloop`/`subagent` 的架构测试。

Gate：`go test ./session ./apiproxy/session ./tests/architecture`。

### M2：Agent

1. 建立普通 `RegistryService` 与 `LifecycleCoordinator`。
2. 定义 `AgentEpoch.Attach`、`AgentTeardown` 和 `AgentScopeRuntime`。
3. Create/Resume 原子创建 exact epoch，并显式接收 `RuntimeParent`。
4. 实现 publication 与 structural 两条状态轴、parent admission、构造 join、child-first close 和稳定 close result。
5. Handle 直接引用 exact epoch；删除 raw lifecycle 构造与 Session ID 重查。
6. Factory registration 用明确状态字段管理 registered/closing/closed。
7. Registry shutdown 用明确状态字段管理 accepting/draining/closed。
8. Registry Plugin 只发布独立 capability view，并在 Dispose 调用 Service Shutdown。
9. 删除 Custody、owner、Roots、IsOwnedBy、SubagentDepth 和旧 lifecycle 类型。

Gate：`go test ./agent ./tests/architecture`，并对并发 close、旧 Handle、新 epoch 和 publication veto 执行重复/race 测试。

### M3：AgentLoop

1. 抽出普通 `Factory`，实现 CreateAgent/ResumeAgent 的 unpublished construction transaction。
2. 把 `ReactLoopAgent` 与 Plugin lifecycle 分离。
3. 用 `agentScopeRoot` 表达单 Agent Scope Plugin，用 `agentScopes` 表达 AgentLoop 拥有的 Scope 集合。
4. preparation 顺序固定为：Session prepare/restore、Scope mount、Provisioning、Session enter/announce、AgentEpoch Attach、teardown adapter mount、Registry publication。
5. `agentScopeRoot` 不保存父 Plugin/Handle；Teardown 只通知 `agentScopes`。
6. `registrationPlugin` 在 commit phase 注册 Factory，Dispose 只关闭 exact registration。
7. configured Agent 统一经 Constructor。

Gate：`go test ./agentloop ./agent`，覆盖每个失败点回滚、重复 Teardown、Runtime 结构卸载和 Factory replacement。

### M4：Subagent

1. spawn/fork Provider 与 Plugin adapter 分离。
2. one-shot 与 continuable 创建显式传入 exact `RuntimeParent`。
3. 删除 Subagent-owned runtime ancestry、child set、recursive teardown 和 materialization lifecycle。
4. Activation 只保存 residency 与 settlement 状态；Manager 通过窄化的 `RuntimeDescendants` 查询 settlement 条件，关闭时只释放 exact managed Handle。
5. Plugin Dispose 先关闭 Subagent admission，为 resident Activation 提交 managed close 请求，并等待每个 exact Agent 建立 Closing cutoff；它不能接触 Plugin Scope Handle，也不在当前 Runtime 操作中等待结构卸载。
6. 保持 durable descriptor、lineage、message delivery、flush 和 terminal settlement 语义。

Gate：`go test ./subagent/... ./agent`，覆盖普通关闭、parent close、Plugin unload、Runtime shutdown、嵌套 child 和 flush failure。

### M5：跨模块收口

1. 保持 Plugin Runtime 不变，不增加 Dispose topology 例外、Quiesce、PreDispose 或每事件 publish 方法。
2. 迁移 `userquestions` 和所有 owner 查询调用方。
3. 更新真实包 README、三份设计文档、本实施方案和独立进度矩阵。
4. 设计确认前不更新 `zh-CN/README.md` 和 `zh-CN/08-implementation-progress.md`。

## 5. 删除清单

以下标识符不得存在于生产代码：

```text
agent.Custody
agent.NewCustody
custodyFrom
Registry.Roots
Registry.IsOwnedBy
HandleTransferred
Reservation
public generic Lifecycle
BeginShutdown
CloseAll
agent.Options.SubagentDepth
continuation.closingRoots
continuation.ownedChildren
Subagent-owned recursive Agent teardown
agent.Agent embeds plugin.Plugin
agent.Agent.RuntimePlugin
agentloop.Plugin implements agent.Factory
ReactLoopAgent implements plugin.Plugin
session memoryStore implements plugin.Plugin
spawn.Provider implements plugin.Plugin
fork.Provider implements plugin.Plugin
Quiesce / PreDispose transition hook
```

`HandleTransferred`、`session/prepared` 和被删除术语只允许出现在解释其不存在的设计或架构 Gate 中。

## 6. 验证顺序

1. 模块 focused tests；
2. 重复并发关闭测试；
3. `go test ./tests/architecture`；
4. `go test ./...`；
5. `go test -race ./...`；
6. `go vet ./...`；
7. `go build ./...`；
8. `git diff --check`；
9. Markdown 链接检查和旧术语零引用审计。

没有实际运行的 Gate 不得记为通过。文档检查不能替代 Go 行为验证。

## 7. 完成定义

- Session、Agent、AgentLoop 和 Subagent 均符合第 3 节职责，Plugin Runtime 保持既有回调与拓扑不变量；
- 所有 Agent 创建路径共用 Registry、Factory transaction 和 LifecycleCoordinator；
- 所有关闭来源汇合到 exact epoch close transaction，再由 AgentLoop 结构 owner 卸载 Scope；
- 没有 Scope Plugin 自卸载、Subagent 操作 Plugin Handle 或 Coordinator 依赖 Plugin；
- 删除清单在生产代码中零引用；
- focused、full、race、vet、build、architecture 和文档 Gate 全部通过；
- 包内 README 与实现一致；
- 进度矩阵中所有非 Deferred 项完成。
