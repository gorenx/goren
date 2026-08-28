# Bound Subagent 需求

> 状态：Accepted / Implemented。Bound 是 Goren 扩展能力，不属于固定 DeepSeek Harness 兼容基线。

## 1. 目标

Bound 是直接属于用户 Session 的长期后台 Subagent。管理员通过 Web 创建一次全局 Definition；之后每个用户 Session 都为每个 enabled Definition 创建一个同名 Bound。Bound 完成创建或恢复后保持 idle，不因创建而发起模型调用；只有新的 parent interaction 或显式 `Send` 才唤醒它。

本文只定义可观察行为。所有权、调用链和并发边界见[技术设计](./parent-bound-design.zh-CN.md)，Definition 与应用快照见[配置管理](./bound-config-management.zh-CN.md)，interaction 恢复见[投递设计](./session-event-delivery.zh-CN.md)。

## 2. 术语和身份

- **Definition**：一次配置、作用于所有用户 Session 的全局 Bound 定义。
- **用户 Session**：`Header.Origin == ""` 且 `Header.ParentSession == nil` 的 Session。
- **Binding**：某个用户 Session 内，Definition 名称与一个稳定 child Session ID 的关系。
- **Applied Definition**：某个 child Agent epoch 实际安装的完整 Definition 快照。
- **Parent interaction**：用户与 parent Agent 已闭合的一次交互，只包含 direct user 输入与 parent 可见回复。
- **Context floor**：Binding 创建时冻结的 parent committed prefix 末端；此前历史只作为 child 初始上下文。
- **Delivery cursor**：Binding 建立后 parent interaction 的增量投递水位。

Definition 名称是稳定身份。名称全局唯一，因此在每个用户 Session 内也唯一。第一版不提供 rename 或 hard delete；停用使用 `Enabled=false`，以后以同名 Definition 重新启用。

## 3. Definition

每个 Definition 必须包含：

- 非空 `Name`；
- 单调递增的 `Revision`；
- `Enabled`；
- 非空且完整的 `SystemPrompt`；
- 可选 Agent options 与最大深度；
- Tool restriction；
- 有序的 selected Extensions。

Definition 不包含 parent Session ID、child Session ID、Title、Persona 或 InitialPrompt。System prompt 是 Bound 自己的完整模型身份，不是追加在 parent prompt 后的 persona 片段。Tool restriction 和 Extensions 复用现有 Tools 与 Extension 能力，不新增任意 Tool 参数模型。

Definition 使用独立 SQLite/sqlc Store，不写入任一 parent Session，也不与 Session SQLite 共表。创建和替换使用 expected revision；Store 成功后才更新进程内索引并协调 live 用户 Session。

## 4. 用户 Session 启动

`agent.SessionStarted` 是一致性检查触发器，不是后台事件历史。只有用户 Session 进入 Bound reconcile；所有 child Session，包括 Bound 自己，必须被过滤，避免递归创建。

启动流程满足：

1. Subagent 在受管任务中处理 `SessionStarted`，任务属于 Plugin 生命周期，可取消、可等待；不得启动裸 `context.Background()` goroutine。
2. scheduler读取enabled Definitions和parent projection，生成缺失Binding候选，然后向parent Session唯一FIFO提交一个`WritePlan`。
3. `WritePlan`到达FIFO队首时重新读取原子Snapshot、fold已有Binding并计算最后完整parent turn之后的`ContextNextSeq`，在一个batch提交全部仍缺失的Binding并`Flush`。
4. Binding保存Definition名称、child Session ID和该FIFO队首Snapshot对应的`ContextNextSeq`。
5. Binding提交不占用Agent activity，不调用`RunMaintenance`，不创建child，也不等待LLM。
6. durable Binding提交后，为不同Binding调度彼此独立的激活任务；不同Bound可以并行。
7. 单个child创建或恢复失败不得否决parent publication；失败写materialization事实并交给diagnostics。

parent正在执行turn或`/compact`时，Binding仍只与其他Session写入在同一FIFO中有序提交，不得竞争Agent maintenance。已经排在Binding之前但尚未闭合的turn事件可以出现在Snapshot中，但`ContextNextSeq`只指向最后一个完整turn之后，因此不进入fresh child seed。Definition新建、启用或替换后的失败reconcile必须保留pending，并由后续Session启动、Definition变更、`turn/end`或显式操作重试。

## 5. 创建、恢复与 idle

fresh child 只复制 `ContextNextSeq` 之前冻结的 parent committed prefix。不能在延迟 materialization 时重新读取“当前最新完整前缀”，否则 Binding 之后的新交互会被吞入 seed，无法作为任务投递。

child 创建或恢复时必须：

1. 核对 parent 是直接用户 Session，child 的 runtime parent 和 `Header.ParentSession` 都指向该用户 Session；
2. 安装 Definition 的完整 system prompt、Tool restriction、common Extensions 与有序 selected Extensions；
3. 在 child Session 提交完整 Applied Definition 快照并 `Flush`；
4. 发布 resident child Agent epoch；
5. 不提交 InitialPrompt，不向 Inbox 发送消息，不调用 LLM；
6. 保持 idle，等待新的 parent interaction 或显式 `Send`。

模型可见输入必须能从 child Session log 重建。Applied event 因此保存实际应用的完整 Definition 和有效配置，不能只引用全局 Store revision 或 parent event seq。

## 6. Interaction 与显式控制

Binding 建立前的 parent 历史只作为 child 上下文，不重放为任务。投递 floor 是 Binding event 后一位；Binding 建立后的完整 interaction 按 cursor 增量投递。

Interaction 只接收：

- source kind 为 direct user 的 `user/message`；
- canonical `assistant/message` 中 parent 的可见内容。

它过滤 reasoning、tool call、tool result、流式 chunk 和内部来源。没有 direct user 输入的 report-only turn 只推进 `skipped` cursor，避免 child report 被反向投递形成 echo loop。

同一 Binding 的 Definition replacement、显式 `Send`、interaction delivery 与 resident epoch 变更必须串行；不同 Binding 相互独立并可并行。显式 `Send` 可以唤醒 idle Bound，但必须先确认 latest enabled Definition 已被当前 resident epoch 应用。

## 7. Definition 变更

- `Create`：CAS 写 Store，更新索引，然后 reconcile 所有 live 用户 Session。
- `Replace`：CAS 写完整 revision N+1，更新索引，并通过每个 Binding 的串行入口替换 resident epoch。
- `Enabled=false`：停止 resident epoch，但保留 Binding、child Session、cursor 与 backlog，不推进 cursor。
- 重新启用：创建缺失 Binding或恢复已有 Binding，应用最新 revision，再从既有 cursor catch up。
- replacement：等待旧 epoch idle，停止旧 epoch，使用同一 child Session 和最新 exact Scope 恢复，提交 Applied Definition，发布新 epoch。

旧 revision 已持久化但某个 live parent reconcile 失败时，不回滚全局 Definition；pending reconcile 保留并在下一次 idle、启动或显式操作时重试。

## 8. 关闭与恢复

关闭顺序固定为：停止新的激活和 reconcile 准入；取消并等待受管任务；并行停止不同 Binding 的 resident child；结算并 Flush；最后关闭 Definition Store。

Session event 是轻量唤醒，不提供 exactly-once 通知。恢复事实来自：

- 全局 Definition Store；
- parent Session 的 Binding、materialization 和 cursor；
- child Session 的 Applied Definition 与 Inbox receipt。

系统承诺 acceptance receipt 与 cursor 可恢复，不承诺 exactly-once 模型调用或 Tool 外部副作用。

## 9. 迁移和验收

旧版 per-parent `subagent/bound-config`、带 `Title`/`Persona`/`InitialPrompt` 的 Binding 已删除。当前实现使用事件version 2并明确拒绝旧版记录，不保留双读或兼容 wrapper；旧开发数据库需要清理后重建。

完成验收至少覆盖：

1. 一个 Definition 为多个用户 Session 各创建一个直接 child；多个 Definition 在同一用户 Session 中名称唯一且并行激活。
2. child Session 不递归创建 Bound。
3. fresh child 只复制冻结 prefix；创建本身无 Inbox、turn 或 LLM 请求。
4. 新 interaction 在 receipt durable 后推进 cursor；崩溃窗口恢复不重复 Inbox acceptance。
5. disable 保留 backlog，enable 后 catch up；replace 使用同一 child Session 和新 exact epoch。
6. Binding通过Session FIFO原子提交，不调用parent maintenance，也不与`/compact`竞争；失败pending可由后续触发追赶。
7. Plugin 关闭后没有遗留 activation/reconcile worker。
8. v1 事件被明确拒绝。

实现证据只记录在[08 实施进度](../../zh-CN/08-implementation-progress.md)。
