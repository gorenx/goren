# Agent 构造事务与调用流程设计

状态：Final Design，实现与全量验收已完成

上位方案：[Agent 构造与父子生命周期重构方案](Agent构造与父子生命周期重构方案.md)

## 1. 目标

本文只定义 Agent 的统一创建、恢复、发布、回滚和调用关系。完整 epoch ownership、parent close 和 descendant teardown 见 [Agent 生命周期与运行期父子所有权设计](Agent生命周期与运行期父子所有权设计.md)。

构造边界必须满足：

- 普通 Agent 和子 Agent 使用同一个 Registry 与 Agent Factory；
- 调用方不直接依赖 `agentloop`；
- Session 与 Agent 的联合发布是一个同步、可回滚事务；
- AgentLoop 不解释 root、child、lineage 或 Subagent policy；
- 调用方只能得到完整的 managed Handle 或错误；
- 不通过 Session 事件触发 Agent 创建。

## 2. 统一链路

### 2.1 普通 Agent

普通 Agent 经 [`AgentSessions.Ensure`](../apiproxy/session/agent_sessions.go) 调用 `agent.Constructor.Create` 或 `Resume`。`RegistryService` 再调用已注册的 Agent Factory，最终进入 [`agentloop.Factory.CreateAgent`](../agentloop/construction.go) 或 `ResumeAgent`。

```text
Session API
  -> AgentSessions.Ensure
  -> agent.Constructor.Create/Resume
  -> registered agent.Factory
  -> agentloop.Factory
```

`agentloop.Plugin` 只负责构造并接入普通 Go `agentloop.Factory`、提供 Agent Scope 结构宿主，不实现 Factory 业务方法。

### 2.2 子 Agent

one-shot [`inprocess.Driver`](../subagent/internal/inprocess/driver.go) 与 continuable [`continuation.Manager`](../subagent/internal/continuation/manager_materialization.go) 也通过同一 `agent.Constructor` 创建或恢复 Agent。runtime parent admission 和 managed Handle commit 由 `agent` 统一完成；Initiator 不参与 parent 或结构 owner 推断。

## 3. 构造参与者

### 3.1 调用方

调用方只拥有用例决策：

- `AgentSessions` 决定普通 Agent 的 Session ID、Create/Resume、CWD、preset 和 model composition；
- Subagent 用例决定 exact parent、durable lineage、descriptor 和 Agent options，并调用选定的 Subagent Provider 取得或执行委派策略；
- 配置启动决定配置 Agent 的 identity 与 composition。

调用方不选择 AgentLoop 实现，不操作 Registry membership，也不自行构造 Agent Scope。

### 3.2 Agent Registry

Registry Service 是不依赖 Plugin lifecycle 的普通 Go 业务对象，也是唯一公开构造入口，负责：

- Agent Factory 注册和撤销；
- Create/Resume 请求入口；
- 调用 Agent Lifecycle Coordinator 创建 exact epoch；
- 调用当前 Agent Factory；
- 将成功构造提交给 Coordinator；
- live membership 查询；
- 决定 Agent Created/Disposed publication 的业务时点。

Registry 的 Factory routing 不是应删除的间接层。它保证消费者依赖 `agent` 契约，而不是依赖具体 AgentLoop。

Agent Registry Plugin adapter 发布 Registry Service，但不代替 Agent Scope 成为事件来源。Registry Service 通过 exact entry 上的 `AgentScopeRuntime` 请求发布 Created/Disposed，对应 Agent Scope Plugin adapter 执行 dispatch；两类 adapter 都不保存 membership 或 epoch 状态。

### 3.3 Agent Lifecycle Coordinator

Coordinator 在 Factory 调用前创建 exact epoch，并把同一 `AgentEpoch` 传给 Factory；Factory 调用 `Attach` 后，Coordinator 接管已经构造的单体 teardown。构造和 publication 成功后由 Registry 返回 managed Handle。

它不构造 Session、Agent Scope 或 ReactLoopAgent。构造期间只管理 admission、runtime parent 和成功结果的 ownership handoff。

### 3.4 Agent Factory 与 AgentLoop

Agent Factory 是 `agent` 定义、由普通 Go `agentloop.Factory` 实现的内部构造能力。Factory 负责：

- 获取 fresh 或 persisted Session preparation；
- 创建 unpublished Agent Scope 和 ReactLoopAgent；
- 执行 Provisioning；
- 进入 Session 与 Agent membership；
- 按顺序 announce；
- 构造失败逆序回滚；
- 提供单个 Agent 的底层 teardown。

Agent Factory 只由 Registry 调用。配置 Agent 也必须通过 Registry，不再由 AgentLoop 绕过 lifecycle admission 直接发布。

`agentloop.Plugin` 是适配器：它解析 Session、Persistence 和 Registry Service，构造 `agentloop.Factory`，并持有统一的 `agentScopes`；commit 阶段的 `registrationPlugin` 拥有 exact `FactoryRegistration`。Factory 不实现 `plugin.Plugin`，也不调用 `plugin.Require/Resolve/Publish`。

### 3.5 Session Store

Session Store 是普通 Go 业务对象，拥有 Session preparation、membership 和 Session lifecycle publication 的决定权。Session Plugin adapter 只发布 Store Service 并桥接 events。

在 Agent 构造路径中，AgentLoop 调用 Session Store 的 prepare、enter 和 announce；这不表示 AgentLoop 拥有 `session/created`。事件 owner 仍是 Session Store。

### 3.6 Agent Scope Plugin adapter

`ReactLoopAgent` 是业务对象，不实现 Plugin。`agentScopeRoot` 是 AgentLoop Plugin adapter 创建的单 Scope Plugin，实现 `agent.AgentScopeRuntime`，负责：

- 在统一结构宿主下挂载单 Agent Scope；
- 发布 exact Agent Service；
- 为 Agent-scoped events 提供正确的 Plugin event source；
- 持有 Provisioning effects，并把结构 teardown 请求交给 AgentLoop 的 `agentScopes`。

业务 Agent 不暴露 `RuntimePlugin`，exact epoch identity 由 Agent 构造事务分配的进程内 identity 表达。

`AgentScopeRuntime` 不暴露 `plugin.Plugin`、Scope handle 或 registration。它是业务 Agent 和 Lifecycle Coordinator 消费的运行期端口，提供 Agent-scoped event/waterfall、运行期 Provisioning 和幂等单体 teardown；事件语义、publication 时机与关闭顺序仍属于对应业务 owner，adapter 不做决策。`agentScopeRoot` 不保存自己的父 Plugin 或 Handle；`agentloop.Plugin` 持有的 `agentScopes` 才是精确 Handle 与 Runtime 卸载命令的 owner。Scope Plugin 的 `Dispose` 只释放本 Scope 资源并回报完成，不能自卸载。

## 4. 目标 Factory 边界

Factory 的构造输入必须显式包含：

- shared Agent/Session identity；
- Create 或 Resume 所需信息；
- Agent options；
- unpublished Scope Provisioning；
- Registry 创建的 exact `AgentEpoch`，其中包含构造关闭信号和单次 Attach 边界。

runtime parent 不由 AgentLoop 解释。AgentLoop Factory 使用 Plugin adapter 注入的统一 Agent Scope factory，不从 `AgentEpoch`、Initiator、Session Header 或 Subagent 类型选择结构 owner。

Factory 在 publication 前把以下构造结果绑定到 Registry 创建的 exact `AgentEpoch`：

- exact Agent；
- 实现 `AgentScopeRuntime` 的 exact Agent Scope adapter，包含 Agent-scoped event dispatch 和单体 teardown；
- 构造完成所需的不可变 identity。

该结果不是第二个公开 Handle。绑定成功后，Coordinator 在 publication 期间拥有回滚责任；Registry 只有在 Factory publication 与 Coordinator commit 都成功后才向调用方返回 managed `agent.Handle`。

## 5. Fresh Create

### 5.1 调用顺序

```mermaid
sequenceDiagram
    participant U as use-case caller
    participant R as agent.Registry
    participant C as Lifecycle Coordinator
    participant F as agentloop.Factory
    participant H as agentloop Plugin adapter
    participant S as Session Store

    U->>R: Create(identity, options, exact parent)
    R->>C: create exact epoch
    C-->>R: AgentEpoch
    R->>F: Create(AgentEpoch)
    F->>S: prepare fresh Session
    F->>H: prepare unpublished Agent Scope
    H-->>F: scoped dependencies and event port
    F->>F: construct ReactLoopAgent business object
    F->>H: bind exact Agent and mount agentScopeRoot
    H-->>F: AgentScopeRuntime
    F->>F: provision and commit scope effects
    F->>S: enter Session
    F->>C: AgentEpoch.Attach(exact Agent, AgentScopeRuntime)
    F->>S: announce Session
    F->>H: mount Lifecycle adapter
    F-->>R: publication completed
    R->>C: dispatch Agent Created
    C->>C: publication Publishing to Published
    R->>C: dispatch SessionStarted and commit Live
    C-->>R: managed Handle
    R-->>U: managed Handle
```

### 5.2 可见性

Provisioning 完成前，Session、Agent 和 Agent Scope 都不得对 Registry consumer 可见。

进入 membership 后、announce 前可以被事务内部解析，但仍不允许返回调用方。只有以下条件全部满足后，Create 才成功：

- Session 与 Agent exact identity 一致；
- Provisioning 已提交；
- Session membership 已进入；
- Session Created 已完成同步发布；
- Agent Created 已完成同步发布；
- SessionStarted observer 已完成调用，其错误按 observer policy 汇报；
- Coordinator 已从 Publishing 提交为 Live epoch。

`Publishing` 期间 exact Agent 已绑定到 Coordinator，但 managed Handle 尚未向原调用方返回。同步 `agent/created` listener 可以以该 exact Agent 为 runtime parent 创建 child；若 listener 返回错误并拒绝 parent 提交，Coordinator 必须先关闭这些已提交或仍在构造的 descendants，再回滚 parent。

## 6. Resume

Resume 与 Create 共用 Agent 物理构造、Provisioning、membership、announce 和 Coordinator commit，只替换 Session preparation 来源：

```text
Persistence prepare
  -> restored unpublished Session
  -> common Agent construction
  -> common membership publication
  -> common managed epoch commit
```

历史 Session 的调用语义保持不变：当普通或 Subagent 用例需要一个没有 live Agent 的 durable Session 时，通过 Registry Resume 恢复。

恢复不会继承旧进程 epoch 的 runtime parent、Handle、closing、children 或 Plugin installation。调用方必须为新 epoch 显式提供 runtime parent；durable lineage 仍来自 Session Header。

## 7. Session 事件边界

### 7.1 `session/created`

Session Store 发布 `session/created`。AgentLoop 只决定在联合构造事务中的调用时点：

```text
Session Store enter
  -> Session Store announce session/created
  -> Agent Factory return
  -> Agent Registry dispatch agent/created
```

同步 `session/created` listener 返回错误并拒绝提交时，必须回滚 Session membership、Agent Scope 和 Provisioning；已经观察到 Created 的 listener 必须获得配对 Disposed。

### 7.2 不增加 `session/prepared`

Agent 创建需要唯一执行者、返回 exact Handle、同步失败、取消和完整回滚。普通广播事件不能安全表达这些语义。

`SessionStore.Prepare` 还服务于 create、resume、fork、persistence 和测试；把它映射成 Agent 创建事件会误触发无关路径。因此：

- Session 不触发 Agent；
- `session/created` 只表达 Session lifecycle；
- Agent 创建或恢复由应用用例显式调用 Registry；
- 将来若需要“Session 无 Agent 长期存在”，另行设计同步 Agent activation 命令。

## 8. 构造状态与回滚

Factory 内部构造状态是短暂实现状态，不持久化：

```text
Admitted
  -> SessionPrepared
  -> ScopeMountedUnpublished
  -> ScopeProvisioned
  -> MembershipEntered
  -> AgentEpoch.Attach 完成
  -> Publishing
  -> Announced
  -> FactoryReturned
  -> CoordinatorCommitted
```

`AgentEpoch.Attach` 完成之前，AgentLoop 拥有构造回滚责任。绑定成功后，Coordinator 通过 `AgentScopeRuntime` 接管 Agent publication 与单体 teardown 的调用时机；后续 Session announce、Factory return 或 Agent publication 失败都通过 exact epoch abort 关闭嵌套 descendants 和当前 Agent。Factory 返回与 Coordinator 提交之间仍未向业务调用方暴露 Handle。

绑定前任一步骤失败都由 AgentLoop 逆序释放已获得资源：

```text
stop serving if started
  -> remove Agent membership if entered
  -> release Session membership if entered
  -> remove runtime-context routing
  -> dispose Provisioning effects
  -> unload Agent Scope root
  -> dispose unpublished Session preparation
```

绑定后的失败由 Registry 请求 Coordinator abort；AgentLoop 的单体 teardown 保持幂等。调用方不会收到半初始化 Handle，也不负责清理未成功返回的构造。

## 9. 并发语义

### 9.1 exact identity

同一 Session ID 的并发请求可以进行私有准备，但只允许一个 exact epoch 提交 live membership。失败方不能覆盖已发布实例，也不能用旧 disposer 删除新实例。

### 9.2 parent close race

Coordinator 创建 exact epoch 发生在 Factory 调用前：

- parent 已 Closing：立即拒绝；
- epoch 创建后 parent 开始 Closing：parent close 等待本次构造；
- Factory 失败：终止本次 epoch；
- child 进入 Publishing 后：已经纳入 parent ownership，即使尚未向 child 调用方返回；
- publication 成功且 commit 允许：child 转为 Live；
- publication 或 commit 被拒绝：关闭未暴露的 child 及其嵌套 descendants，再终止本次 epoch。

### 9.3 construction cancellation

调用 Context 或构造期信号只取消尚未返回的构造事务。managed Handle 返回后，调用 Context 不再控制 Agent 生命周期；后续通过 Handle、parent close 或 Runtime shutdown 关闭。

## 10. AgentLoop 职责变化

`agentloop.Factory` 保持：

- Session preparation；
- ReactLoopAgent 与 Agent Scope 构造；
- Provisioning；
- membership 与 announce；
- activity、cancel、quiesce；
- lifecycle attach 前的 Factory rollback，以及 attach 后可由 Coordinator 调用的窄 `AgentScopeRuntime`；
- `AgentEpoch.ClosingSignal` 驱动的单次构造取消。

`agentloop.Plugin` 只保留：

- Plugin Service 依赖解析；
- Agent Factory registration effect；
- AgentLoop `agentScopes` 结构 owner 和 Agent-scoped event bridge；
- commit 阶段 exact Factory registration 的建立与撤销。

移出：

- 从 Initiator 推断 owner；
- runtime parent-child ownership；
- descendant admission 与关闭；
- 面向业务调用方的 managed Handle ownership；
- root/child 和 Subagent policy 判断。

Factory、ReactLoopAgent 和 activity coordinator 不嵌入 `plugin.Base`，不实现 Plugin lifecycle。Plugin adapter 不保存 Agent activity、epoch 或 Session 业务状态。

私有 [`agentScopeRoot`](../agentloop/scope_root.go) 只表示一个 Agent 的运行期 Scope adapter，不表示 Agent 父子关系。

## 11. 测试要求

至少覆盖：

- fresh Create 和 Resume 使用同一 publication 顺序；
- 普通 Agent、one-shot 和 continuable 使用同一 Factory；
- 配置 Agent 不绕过 Registry admission；
- Provisioning 每个失败点逆序清理；
- Session Created 或 Agent Created listener 返回错误时配对回滚；
- `agent/created` listener 可以在 parent Publishing 阶段共同激活 child；
- parent publication 被 listener 拒绝时关闭该 listener 已创建的 descendants；
- Coordinator commit 失败关闭未暴露 Agent；
- 同 ID 并发只有一个成功；
- 构造取消不会遗留 Session、Agent、Scope 或 routing；
- Initiator 不改变 runtime parent；
- AgentLoop 不导入 Subagent。
