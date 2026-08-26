# Subagent 术语与命名规范

状态：Working Vocabulary

本文约束 `subagent` 领域文档、Go API、持久化数据和事件中的用词。它不要求 Go 类型逐字翻译 TypeScript 标识符；它要求先分清哪些词是外部兼容契约，哪些词是领域概念，哪些词只是实现名称。

## 1. 三类词汇

### 1.1 兼容词汇

“兼容词汇”是客户端、持久化数据、插件 observer、配置或错误处理能够直接观察并据此分支的 token。它们属于契约，必须与接受的 DeepSeek Harness 基线逐字一致，不能为了 Go 风格改名。

当前包括：

- 事件名：`subagent/provider-added`、`subagent/provider-removed`、`subagent/start`、`subagent/end`、`subagent/descriptor`；
- JSON 字段与 discriminant：`version`、`mode`、`provider`、`label`、`agentProvider`、`agentModel`、`persona`、`toolFilter`，以及 `one-shot`、`continuable`；
- MessageSource wire token：`coordinator`、`subagent-report`、`subagent-settled`；
- stop reason 和 error code，例如 `completed`、`aborted`、`NO_PROVIDER`、`UNAUTHORIZED`；
- 纳入范围的配置 key、Provider name 和 Host wire method。

例如 Go 类型叫 `ReportSource`，但它编码后的 `kind` 必须仍是 `subagent-report`。Go 类型名不是 TypeScript 客户端协议；wire token 才是这里的兼容词汇。

### 1.2 领域术语

领域术语表达稳定业务概念。Go 可以按语言习惯选择简洁名称，但同一概念不能在不同文件中换同义词。

例如 `Provider`、`Run`、`Activation`、`Descriptor`、`Settlement`、`Inbox`、`direct parent`。这些名称应回答对象是什么，而不是罗列它关联谁、经过什么处理或存在哪里。

### 1.3 实现术语

实现术语只描述 Go 内部机制，例如 `provider.Registry`、`continuation.Manager`、admission lock。它们可以在局部证据充分时调整，但不能改变兼容 token 或悄悄改变领域职责。

## 2. 领域词典

| 术语 | 定义 | 不表示 |
| --- | --- | --- |
| Subagent | 由父 Agent 委派并具有独立 Session identity 的子任务执行者 | 一个 Tool DTO 或独立 Agent Loop 实现 |
| one-shot | Provider 发布 `Run`，caller 等待一个 terminal `Result` 的策略 | 不可观察、无 Session 的函数调用 |
| continuable | 以 durable child Session 和唯一 Inbox 支持多次消息、冷恢复的策略 | Provider 持有的长连接或 `Run` |
| Provider | 建立 one-shot Run，并可选贡献 continuable creation data 的扩展者 | Subagent Plugin、Agent Registry 或 persistence adapter |
| Plugin | 装配并发布 Subagent capability、适配 Plugin 生命周期和事件的对象 | Subagent 用例实现或每个 child 的模型循环 |
| Run | 已发布 one-shot child 的 holder-owned handle | continuable child 或它的 Activation |
| Result | one-shot Run 的 terminal outcome | durable Session 的完整事实集合 |
| Descriptor | child Session 中第一个 `subagent/descriptor` event 表达的 durable Subagent identity | process-local runtime status |
| Activation | continuable child 在一个进程中的一次 resident epoch，持有 exact Agent Handle | 当前正在对话的 Session；durable 状态机 |
| resident child | 当前有 Activation、因此在 Agent Registry 中有 live Agent 的 continuable child | 只要 Session 存在就 resident |
| LiveStore | 当前进程中已加载且尚未释放的 live Session 集合 | 当前被用户选中的会话；当前正在运行模型的 Session |
| Inbox | Agent 拥有的 durable pending-message projection，是 continuable 消息的唯一队列 | Subagent 自建的第二条 queue |
| direct parent | child Session Header 指向的直接父 Session，live 操作还要匹配 exact Agent identity | 任意 ancestor 或发送 MessageSource 的对象 |
| ancestor | 沿 durable parent 链位于目标 child 之上的 Agent | 仅 direct parent |
| authority | 获准对 live child 执行 followup、interrupt 或 report 的精确身份 | MessageSource 或仅相同 Session ID 的陈旧对象 |
| Settlement | Activation 在无待处理工作且 Agent idle 后形成 terminal edge、parent notice 和资源释放的过程 | 一条普通 report；one-shot Result 本身 |
| Provisioner | 配置一个 active 但未发布的 Agent Scope 的对象 | Agent Loop、Plugin phase 或 resident lifecycle |
| Provisioning | Provisioner 返回的一次配置事务，负责 publication commit 与后续释放 | 所有 Provisioner 都必须制造的空生命周期对象 |
| ActivationExtension | 安装到 continuable Activation 的可选 child-scoped 能力 | 全局 Service locator、Agent Provisioner 或 child Runtime |
| Catalog | 不创建、不 resume Agent 的 durable child 查询能力 | Activation registry 的别名 |
| MessageSource | 写入消息的 durable attribution | 权限凭证 |

## 3. 容易混淆的契约

### 3.1 Activation 与 Descriptor

Descriptor 回答“这个 durable child 是什么”，写入 Session 后跨重启存在。Activation 回答“这个 continuable child 此刻是否在本进程驻留，以及谁持有它的 exact Agent Handle”，进程结束即消失。同一个 child 可以先后产生多个 Activation epoch，但只有一个 authoritative descriptor。

### 3.2 Live、running 与 active conversation

`LiveStore` 的 live 只表示 Session 已在当前进程 materialize 且未释放。Agent 可以 idle，Session 仍然 live。UI 当前选中的会话也可能只是一个 durable identity，不能据此推断其在 LiveStore。

Catalog 的 `ActivityRunning` 当前表示 Session 在 LiveStore；它不是“模型正在生成”。若客户端需要更细的 idle/running sampling，应由 delivery mapping 组合 Agent status，不能污染 durable descriptor。

### 3.3 Cancel 与 idle

“resident child 接收 `Agent.Cancel(..., KeepInbox=true)`，方法不等待 idle”表示：continuation Service 同步验证 authority 并发出取消信号后立即返回；目标 Agent 可能稍后才观察信号并进入 idle。`KeepInbox=true` 保留尚未被 loop claim 的排队消息，Activation 和 descendants 也不因此自动释放。

## 4. Go 命名规则

1. 名称首先回答“这个对象是什么”。
2. 同一对象不要把 owner、处理步骤、存储方式和关联对象全部拼进名称。
3. 名称需要连续叠加多个概念才能成立时，先检查类型是否混合职责，并优先拆分。
4. 接口按 Consumer 能力命名：`OneShotService` 与 `ContinuableService` 分开；不要用一个 `Service` 暴露所有模式。
5. 值对象按身份命名：`ReportSource`、`SettlementSource`；wire `kind` 仍保留兼容 token。
6. 不再用 `Setup` 同时表示 Agent 配置事务和 Subagent 扩展；分别使用 `Provisioner`/`Provisioning` 与 `ActivationExtension`。
7. 不使用 `types.go` 集中收容所有声明；文件按 `provider`、`one_shot`、`continuable`、`descriptor` 等概念组织。
8. 变量、参数、receiver 和 named return 不得与包内函数或类型名发生仅大小写不同的复用。
9. 子模块按内聚业务能力命名；不按 DTO、service、mapper、storage 等技术处理阶段切目录。

命名调整不能顺手改变公开 wire、persisted shape 或事件。若要改变公开 Go API，也必须同时更新调用者、测试和本领域文档，并记录兼容影响。
