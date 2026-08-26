# Subagent 术语与命名规范

状态：Current Vocabulary

本文约束当前 Subagent Go API、设计文档、持久化数据和事件中的用词。固定 wire token 与 Go 领域对象必须分开：兼容名称不能随意修改，但也不能反向决定 Go 对象职责。

## 1. 三类词汇

### 1.1 兼容词汇

兼容词汇可被客户端、持久化日志、Plugin observer、配置或错误处理直接观察，必须按固定契约保留：

- 事件名：`subagent/provider-added`、`subagent/provider-removed`、`subagent/start`、`subagent/end`、`subagent/descriptor`；
- descriptor 字段和 discriminant：`version`、`mode`、`provider`、`label`、`agentProvider`、`agentModel`、`persona`、`toolFilter`、`one-shot`、`continuable`；
- MessageSource token：`coordinator`、`subagent-report`、`subagent-settled`；
- stop reason、错误码、配置 key、LLM provider name、兼容 `providerName` 值和纳入范围的 Host wire method。

`provider` 字段保存首次创建 child 使用的 SeedBuilder 名称。保留该字段不表示 Go 中仍存在 Provider-owned execution。

### 1.2 领域术语

领域术语表达稳定业务对象：`SeedBuilder`、`Execution`、`Terminal`、`Descriptor`、`Settlement`、`ChildDirectory`、`Inbox`、`direct parent`。同一概念不得在不同模块中换一套名称。

### 1.3 实现术语

实现术语描述局部机制，例如 `childSlot`、`execution.Registry`、admission state、`Terminator`。它们不能改变 wire token，也不能制造第二个领域 owner。

## 2. 领域词典

| 术语 | 定义 | 不表示 |
| --- | --- | --- |
| Subagent | 由父 Agent 委派、具有独立 child Session identity 的任务执行者 | Tool DTO 或独立 AgentLoop |
| OneShot | 每次 Start 创建一个 fresh child，形成一个 terminal outcome 后自动释放 exact Handle 的实现 | 无 Session 的函数调用 |
| Continuable | 以 durable child Session 和 Agent Inbox 支持多次消息与 cold resume 的实现 | 长连接或永久 resident Agent |
| Mode | 选择 OneShot/Continuable implementation 的稳定 discriminant | 生命周期状态 |
| ChildRequest | 两种实现共同 snapshot 的 caller-owned child 输入 | mode-specific command union |
| StartCommand | 由合法构造函数生成的 closed OneShot/Continuable 启动命令 | 任意 optional-fields DTO |
| SeedBuilder | 只为 fresh child 构造 detached Session event prefix 的策略 | Agent factory、Subagent implementation 或 lifecycle owner |
| SessionSeed | SeedBuilder 返回的 detached event prefix | 完整 child Session 或 Agent |
| Execution | 一次 Subagent 业务执行；与一个 exact Agent epoch 一一对应 | 第二个 Agent epoch 或 durable child identity |
| ExecutionState | `Starting/Active/Stopping/Stopped` 的统一业务阶段 | Agent 的 idle/running/closing 状态 |
| RunID | 配对一次 Execution 的 `subagent/start` 与 `subagent/end` | child Session ID |
| Terminal | 一次 Execution 的 memoized output、structured value、diagnostic 和 StopReason | Session 的完整事实集合 |
| durable child | Header、descriptor 与 Session log 定义的持久 child identity | 当前一定有 live Agent |
| Descriptor | `subagent/descriptor` event 表达的 durable Subagent mode 和恢复输入 | process-local Execution state |
| Settlement | Continuable 在 idle、Inbox empty、无 runtime descendants 时完成当前 Execution 的正常终止事务 | Agent 的结构关闭算法或普通 report |
| ChildDirectory | 不创建、不恢复 Agent 的 durable child 查询能力 | live Execution Registry 或控制授权 |
| ContinuableExtension | 在 unpublished continuable Agent Scope 安装 child-scoped contribution 的扩展 | Plugin、Agent Provisioner 或 Subagent implementation |
| ExtensionInstallation | 一项 exact child-scoped effect 的幂等卸载权 | Plugin Scope Handle |
| Provisioner | 配置一个尚未发布 Agent Scope 的对象 | AgentLoop 或 Subagent lifecycle |
| Provisioning | Provisioner 返回的 publication commit 和 release 事务 | Extension registration |
| Inbox | Agent 拥有的 durable pending-message projection | Subagent 自建 queue |
| direct parent | child Header 指向的父 Session；live 操作还需匹配 exact Agent identity | 任意 ancestor 或 MessageSource sender |
| ancestor | 沿运行父子关系和 durable parent chain 位于 child 之上的 live Agent | 仅 direct parent |
| authority | 获准执行 Send、Interrupt 或 Report 的 exact caller evidence | 目录快照或仅相同 Session ID 的陈旧对象 |
| ActivityRunning | ChildDirectory 读取时该 child Session 位于 LiveStore | 模型正在生成或 Agent StatusRunning |
| MessageSource | 写入 Session message 的 durable attribution | 权限凭证 |

## 3. 关键区别

### 3.1 Execution 与 Agent epoch

两者是一对一关系，不是两个嵌套 residency 概念：

- Agent epoch 由 `agent` 拥有，包含 exact Agent、Scope、Inbox、runtime parent 和 AgentLoop；
- Execution 由 `subagent` 拥有，记录该 epoch 对应的 Subagent RunID、统一状态和 terminal transaction。

Subagent 不再定义 Activation。需要表达“当前进程中存在 exact Agent”时使用 live Agent/Execution；需要表达持久身份时使用 durable child/Descriptor。

### 3.2 Descriptor 与 SeedBuilder

SeedBuilder 只在 fresh create 时产生 seed。Descriptor 写入 child Session，保存恢复所需的 Subagent identity 和创建策略名称。

Continuable cold resume 读取 descriptor，但不重新调用 SeedBuilder。descriptor 的 `provider` 字段是兼容字段，语义上指向原 SeedBuilder name。

### 3.3 Live、running 与 active

`LiveStore` 中存在 Session 只说明它已在当前进程加载。Agent 可以 idle，Session 仍然 live。

`ActivityRunning` 是 ChildDirectory 的 Session-level 快照，不等价于 Agent `StatusRunning`。需要展示 Agent idle/running 时，由 delivery adapter 组合 `agent.Registry` 状态，不能污染 durable descriptor。

### 3.4 Interrupt、Dispose 与 Settlement

- Continuable Interrupt 发出当前 turn cancellation，保留尚未 claim 的 Inbox message，并立即返回；它不等待 idle，也不删除 durable child。
- `Execution.Dispose` 请求并等待当前 Execution 的唯一 terminal transaction。
- Settlement 是 Continuable 正常触发 Dispose/stop transaction 的一种业务原因。
- Agent parent close 是结构关闭，由 Agent lifecycle owner 传播；Subagent 只 join exact Execution terminal work。

### 3.5 Projection 与事实源

Projection 是 Session events 的可重建 read model。Registry checkpoint 不是 durable truth。

Identity projection 使用最新 descriptor，因为 fork seed 可能包含 ancestor descriptor。Continuable resume 则按 `Header.SeedLength` 跳过 seed 后读取当前 child descriptor；两者不能仅因都处理 descriptor 就共享同一“first/last”规则。

## 4. Go 命名规则

1. 名称首先回答“这个对象是什么”。
2. 名称需要叠加多个 owner、步骤或存储概念时，先拆分职责。
3. 公共接口按 Consumer 能力命名：`Starter`、`ChildControl`、`ParentReporter`、`ChildDirectory`。
4. OneShot/Continuable 是私有 implementation，不发布 `OneShotService` 或 `ContinuableService` capability。
5. 两种实现共享 `Execution` 状态和 Terminal；不同触发时机留在各自 `Terminator`。
6. 不使用 Activation 表达 Agent epoch，也不使用 Catalog 表达 child directory。
7. `Provider` 只用于兼容 token 或 Service Provider/Consumer 架构术语，不作为 Subagent 创建/执行对象名称。
8. 不用 `Setup` 同时表达 Agent publication transaction 和 Extension；分别使用 `Provisioner`/`Provisioning` 与 `ContinuableExtension`/`ExtensionInstallation`。
9. 文件按主要对象或内聚职责命名；不使用 broad `types.go`，也不拆成一函数一文件。
10. 变量、参数、receiver 和 named return 不得与包内函数或类型仅大小写不同。

命名调整不能顺手修改 wire token、persisted shape 或事件。公开契约变化必须同时更新调用者、测试、技术方案和进度证据。
