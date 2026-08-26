# Parent-bound Subagent 需求

状态：Deferred Draft，核心决策未确认

## 1. 文档边界

本文记录尚未实现的 Parent-bound Subagent 产品和领域需求，不定义当前 OneShot/Continuable 行为，也不固定 Go 类型、持久化字段或目录结构。

当前基线以[Subagent 技术方案](../../zh-CN/Subagent架构与生命周期重构技术方案.md)为准：OneShot 与 Continuable 共用 common `Execution`；Agent 拥有 exact epoch、runtime parent 和结构关闭；`SeedBuilder` 只参与 fresh create；`ChildDirectory` 只读 durable child。

旧草案基于已删除的 Activation、Provider execution 和 Catalog 抽象，不能继续作为实现依据。本文已改用当前术语，但所有 Open Items 仍需确认。

## 2. 目标能力

Parent-bound Subagent 是一个 durable Continuable child，它与 parent Session 建立显式持久绑定：

- parent Agent create/resume 时，系统发现并 create/resume 已启用的 bound children；
- bound child 在 parent idle 时可以继续保留 exact Agent/Execution，而不是按普通 Continuable settlement；
- child 可以按静态 subscription definition 观察 exact direct parent 的指定事件，并把匹配事实转换为自己的 durable Inbox message；
- child 保持独立 Session、Inbox、Agent options、persona、Tool policy 和 model context；
- child 可以通过现有 report Tool 直接向 exact live direct parent Agent 报告；
- parent 结构关闭时，Agent lifecycle owner 仍按 child-first 顺序关闭 bound children；
- 进程重启后复用原 child Session、descriptor、Inbox 和 binding，不重新运行 fresh SeedBuilder。

该能力不新增第三个 Subagent Mode。它是 Continuable durable child 的 binding/residency policy，不是新的生命周期实现。

## 3. Owner 与非职责

| Owner | 负责 | 不负责 |
| --- | --- | --- |
| Session | parent/child facts、binding facts、Inbox message 和 checkpoint 的 durable log | Agent materialization 或 subscription execution |
| Agent | exact Agent epoch、runtime parent、descendant admission、child-first close、Scope、Inbox 和 AgentLoop | binding policy、event matching 或 handoff checkpoint |
| Subagent | 触发 Continuable create/resume、common Execution、binding policy、授权和 parent/child message semantics | Agent 运行时父子关系、Session persistence adapter 或 AgentLoop |
| Binding capability（拟议） | durable binding interpretation、subscription installation、handoff admission 和 recovery coordination | 创建另一个 Session store、直接修改 Agent internals |
| SeedBuilder | 仅 fresh child 的 Session seed | binding、resume、delivery 或 resident Handle |
| ChildDirectory | durable child/binding 查询视图 | create/resume、authority 或 delivery |
| Plugin Runtime | capability binding、event dispatch、Plugin/Scope lifecycle | Parent-bound 业务状态机 |

## 4. 统一语言

| 术语 | 定义 | 不表示 |
| --- | --- | --- |
| binding | parent Session 与一个 durable Continuable child Session 之间的持久关系 | runtime parent pointer |
| bound child | 拥有有效 binding 的 Continuable child | 当前一定 resident |
| resident | 当前存在 exact Agent 和对应 common Execution | 模型正在执行 step |
| subscription definition | 描述要观察的 parent event type、filter 和 delivery policy 的持久定义 | process-local listener handle |
| installation | 某次 exact child Execution Scope 中安装的 subscription/handoff effects | binding 本身 |
| handoff | 把一个匹配的 parent durable fact 转换为 child Inbox message 的过程 | 普通 report 或直接调用 child state |
| cursor | 表示某 binding 已处理到哪个 parent event 的 durable checkpoint | Session global revision |

## 5. Use Cases

### 5.1 建立 binding

调用方为 exact parent 和一个 Continuable child 建立 binding，并提供 subscription definition、启用状态和必要 policy。成功必须表示 durable binding facts 已提交；不得只创建 process-local listener。

fresh child 创建失败时不能留下“成功但无 child”的 binding。是否允许先绑定已有 child，以及 binding/create 的原子边界，见 Open Item O1。

### 5.2 Parent create/resume

parent Agent publication 前或后，系统发现其 enabled bindings，并为每个 child create/resume exact Agent 和 common Execution。

- 必须复用原 child identity、Session 和 Inbox；
- 不重新调用 SeedBuilder；
- descriptor/binding 损坏不能静默创建替代 child；
- 单个 child 失败如何影响 parent publication，见 O2。

### 5.3 Parent event handoff

parent 继续正常执行并发布事件。binding installation 只接受 exact direct parent 的匹配事件，生成带 provenance 的 child Inbox message。

parent 不知道有哪些 children，也不主动为某个 child 调用路由方法。child subscription 不得依赖“child Scope listener 能向上观察 parent Scope”的错误假设。

### 5.4 Child report

bound child 复用现有 report Tool 的 Agent delivery。report 只送 exact live direct parent；quiet/next-step scheduling 保持现有语义。report message 必须可与 parent event handoff 区分，防止默认 feedback loop。

### 5.5 Idle residency

普通 Continuable 在 settlement 条件成立时停止当前 Execution。bound child 可以由 durable policy 保持 resident，但仍使用同一个 common Execution 和 Agent epoch，不建立额外 residency object。

保持 resident 不等于 busy。Agent 可以 idle，Inbox 可以为空。

### 5.6 Parent close

parent 进入 Agent lifecycle closing 后：

- 停止接纳新的 parent-to-child handoff 和 child-to-parent report；
- join 已接受 handoff；
- 由 Agent lifecycle owner 关闭 runtime descendants；
- Subagent/Binding owner 完成各 child Execution terminal transaction；
- release 后不得残留 listener、worker、installation 或 resident child。

### 5.7 进程重启

系统从 durable binding、Session log、descriptor、Inbox 和 cursor 恢复。process-local listener、worker、Handle 和 Execution 必须重新建立，旧对象不得作用于新 epoch。

### 5.8 用户隔离

bound child 默认不得直接向用户发起 `ask_user_question`。其模型输入来自自己的 Inbox、匹配的 parent facts 和显式 Tool policy；需要用户决策时通过 parent report 请求 parent 协调。

## 6. Invariants

### 6.1 Identity 与 ownership

- I1：binding 使用 durable parent/child Session identity，不使用 process-local pointer 作为事实源。
- I2：一个 child 同时至多有一个 authoritative binding owner，是否允许多 parent 见 O3。
- I3：任一时刻一个 durable child 至多有一个 current Execution。
- I4：旧 Agent、Execution、installation 或 disposer 不能操作后来复用同一 Session ID 的新 epoch。
- I5：Agent 唯一拥有 RuntimeParent/children 关系和 child-first close。

### 6.2 Durability 与 delivery

- I6：模型可见 handoff 必须先进入 child Session/Inbox durable path。
- I7：replay 不得因进程重启重复生成语义相同的 child message。
- I8：cursor 只能在 child Inbox 接受成功后推进。
- I9：parent durable fact 必须先于依赖它的 child handoff 被确认持久化。
- I10：binding disabled/removed 后不得继续接受新 handoff。

### 6.3 Lifecycle

- I11：parent closing 的 admission cutoff 与已接纳 handoff join 必须线性化。
- I12：parent release 完成时不得残留 bound child live Execution。
- I13：single child failure 不能隐式关闭无关 siblings。
- I14：binding policy 只改变 Continuable settlement/residency 触发，不复制 common Execution 状态机。

### 6.4 Security

- I15：subscription 只接受 exact direct parent，不能用相同 Session ID 的 stale Agent 冒充。
- I16：ChildDirectory snapshot、MessageSource 和 persisted label 都不是 authority。
- I17：child Tool/Approval policy 在每次 exact Agent Scope publication 前安装。
- I18：secrets 不进入 binding、Session event、Inbox、diagnostic 或 telemetry。

## 7. Failure Semantics

- child create/resume failure：保留原 identity，报告 per-child diagnostic，不静默替换；
- binding/subscription codec failure：拒绝 installation，不能退化为无监听 resident child；
- event filter failure：归属于该 binding/event，不阻止无关 subscriptions；
- Inbox rejection：不推进 cursor，可按已确认 retry policy 重试；
- parent unavailable：child report 返回稳定 parent-unavailable failure；
- shutdown timeout：报告仍未收敛的 exact child/handoff，不假装 release 成功；
- persistence failure：区分 binding truth、Inbox acceptance、cursor checkpoint 和 best-effort diagnostics。

## 8. Non-functional Requirements

- 每个 parent event 的匹配复杂度必须有明确上限；不能全量扫描所有 Sessions。
- per-binding handoff 必须保持确定顺序，并限制并发与队列增长。
- diagnostics 至少包含 parent ID、child ID、binding revision、subscription identity、parent event seq、child Message ID 和 Execution RunID。
- recovery 必须可由 durable facts 重建，不依赖 Plugin 启动顺序的偶然内存状态。
- 所有 registration、listener、worker 和 installation 都有 exact idempotent disposer。

## 9. Open Items

| ID | 决策 | 影响 |
| --- | --- | --- |
| O1 | binding 与 fresh child create 是否一个原子用例 | 决定失败回滚和孤儿 child 语义 |
| O2 | bound child 恢复失败是否阻止 parent Agent publication | 决定 required/optional policy 和批量回滚 |
| O3 | 一个 child 是否允许多个 parents/bindings | 决定 authority、cursor 和 report destination |
| O4 | subscription filter 的静态表达能力 | 决定配置 schema、安全边界和版本迁移 |
| O5 | handoff 的 at-least-once/exactly-once observable contract | 决定 provenance、dedupe 和 checkpoint transaction |
| O6 | binding/config 更新何时作用于 resident child | 决定下一 Execution 生效还是支持 live replacement |
| O7 | parent event retention 与落后 cursor 的 repair policy | 决定长时间离线恢复和数据清理 |
| O8 | bound child 的用户交互是否永远禁止或可显式授权 | 决定 Approval/Tool policy |

这些决策会改变持久化、publication 或 delivery 语义。在确认前，配套技术设计只能保持 Proposed，不得实施或更新全局进度。

## 10. Acceptance Scenarios

1. parent restart 后恢复原 bound child identity、Session、Inbox 和 cursor，不调用 SeedBuilder。
2. 两个 siblings 独立保持 prompt、Tool policy、Inbox 和 Execution；一个失败不污染另一个。
3. 匹配 parent event 只生成一个带 provenance 的 child message；重放不会重复模型可见输入。
4. disabled binding 不再接收新 handoff，已接受 handoff 按最终 policy 收敛。
5. parent close 与并发 handoff/report 线性化，release 后无 live child、worker 或 listener。
6. stale Agent、Execution 或 installation 无法操作同 ID 的新 epoch。
7. bound child 不直接向用户提问，除非 O8 明确授权。
8. ChildDirectory 可以展示 binding diagnostic，但查询本身不 create/resume Agent。
