# Subagent 术语与命名规范

状态：Accepted / Implemented

本文约束 Subagent Go API、设计、事件和持久化数据中的用词。名称必须先回答“这个对象是什么”；当一个名称需要叠加多个关联对象、处理步骤或存储概念才成立时，应拆分职责，而不是继续延长名称。

## 兼容词汇

下列名称可被客户端、Session log、配置或 observer 观察，必须保持 pinned Harness 契约：

- 事件：`subagent/provider-added`、`subagent/provider-removed`、`subagent/start`、`subagent/end`、`subagent/descriptor`；
- Mode token：`one-shot`、`continuable`、`bound`；
- 字段：`version`、`mode`、`provider`、`label`、`agentProvider`、`agentModel`、`persona`、`toolFilter`；
- MessageSource：`coordinator`、`subagent-report`、`subagent-settled`；
- stop reason、错误码、配置 key 和纳入范围内的 Host wire method。

兼容字段 `provider` 保存首次创建 child 使用的 SeedBuilder name，不表示 Go 中存在 Provider-owned execution。

## 领域词典

| 术语 | 是什么 | 不是什么 |
| --- | --- | --- |
| Subagent | 父 Agent 委派、拥有独立 child Session identity 的任务执行者 | Tool DTO、Plugin 或 AgentLoop |
| OneShot | 每次 Start 创建 fresh child，并在一个 Terminal 后释放 exact Handle 的 Mode | 无 Session 的函数调用 |
| Continuable | 以 durable child Session 支持多次 resident execution 和 cold resume 的 Mode | 永久 resident Agent |
| Bound | 由 parent Session Binding 与全局 Definition 管理的 direct child Mode | 通用 parent event consumer |
| Mode | 选择 OneShot/Continuable/Bound 的稳定 discriminant | 生命周期状态 |
| SeedBuilder | 为 fresh child 构造 detached Session event prefix 的策略 | Agent Factory 或 lifecycle owner |
| Execution | 一次 Subagent 业务运行，与一个 exact Agent 生命周期一一对应 | durable child identity |
| ExecutionState | `Starting/Active/Stopping/Stopped` 的公共阶段词汇 | 一个共享状态机 owner |
| RunID | 配对一次 `subagent/start` 与 `subagent/end` 的运行 ID | child Session ID |
| Terminal | 一次 execution 的稳定 output、structured value、diagnostic 和 StopReason | Session 全部事实 |
| Descriptor | Session event 保存的 durable Mode 和恢复输入 | process-local execution state |
| Settlement | Continuable 在 Agent idle、Inbox empty、无 runtime descendants 后关闭当前 execution | Agent 结构关闭 |
| ChildDirectory | durable child 的只读查询能力 | live execution index |
| Agent Setup | 向 unpublished Agent Scope 安装一组 child-local contribution 的对象 | Plugin 或 publication transaction |
| ScopeResources | 一次 Setup 提交后由 exact Agent Scope 持有的资源集合 | Plugin Handle |
| Inbox | Agent 拥有的 durable pending-message projection | Subagent 自建 queue |
| direct parent | child Header 指向并通过 exact live identity 复核的父 Agent | 任意 ancestor |
| ActivityRunning | ChildDirectory 查询时 child Session 当前位于 LiveStore | 模型正在生成 |
| AgentEvent | exact Agent Scope 内的进程级组件通信事实 | Session event |

## 关键边界

### Execution 与 Agent

Agent 生命周期由 `agent` 拥有：exact Agent、Scope、Inbox、runtime parent 和 child-first close。Subagent execution 由各 Mode 自己拥有：RunID、`ExecutionState`、Terminal 和 Mode-specific close。

OneShot、Continuable 与 Bound 共享 `subagent.Execution` 接口和状态词汇，但分别由 `oneShotExecution`、`residentExecution`、`residentEpoch` 持有状态。禁止恢复共享 `Execution` struct、`Terminator` callback 或 owner back-reference。

### Session ID 与 RunID

Session ID 唯一确定 durable child 和当前 live Agent 的业务 identity。同一个 Continuable child 可以多次恢复，因此每次 resident execution 使用新的 RunID。不得增加 `instanceID` 或 `AgentInstance`。

### Plugin 与 Setup

Subagent 核心只有一个全局 Plugin。Persona、Policy、Descriptor、Structured Output、Extension 和 Report 使用 ordinary `agent.Setup` 安装到 child Scope；它们不是 child Plugin，业务实现不得 mount/unload Plugin。

### AgentEvent 与 Session event

`subagent.Started` / `Ended` 是组件通信 `AgentEvent`，不持久化。`Ended` 可在 exact parent closing 阶段继续派发，直到 Scope 关闭。需要重建的模型输入和 durable child identity 必须来自 append-only Session log。

## 命名规则

1. Consumer 能力使用 `Starter`、`ChildControl`、`ChildDirectory` 等对象名。
2. OneShot、Continuable、Bound 的 Service 是私有 Mode implementation，不发布第二套公共 Service。
3. `Provider` 仅用于兼容 token 或 Service Provider/Consumer 语义。
4. `Activity` 只用于兼容目录快照，不作为运行状态或状态机名称。
5. 不使用 `Outcome`；按职责使用 `Terminal`、`Result` 或具体状态。
6. 不使用 `Installation` 表达 Agent Setup；安装前事务由私有 scope draft 表达，提交后对象叫 `ScopeResources`。
7. 状态进入方法使用 `Enter...`；实际动作使用 `Publish`、`Dispatch`、`Close`、`Flush` 等动作名。
8. 所有状态和常量必须逐项注释语义。
9. 文件按主要对象或职责命名；Plugin 实现文件以 `plugin` 开头，同一 receiver 跨文件时使用公共前缀。
10. map 文档必须说明 key 和 value；变量、参数、receiver、named return 不得与包内函数或类型仅大小写不同。

公开名称、wire token 或 persisted shape 的变化必须同时更新调用者、fixture、测试和权威设计；私有重构不得借机修改兼容契约。
