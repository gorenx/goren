# Bound Subagent 需求

> 状态：当前仓库已实现。Bound 不属于固定 DeepSeek Harness 基线；它是 Goren 的扩展能力，不能用于声明基线兼容。

## 1. 范围

Bound 是独立于 OneShot 和 Continuable 的第三种 Subagent Mode。它表示：exact parent Session 先持久化一个 child binding 和完整配置，随后由 parent Agent 的启动事件或 parent 的后续发送创建、恢复并保持该 child Agent；enabled binding 还持续接收 direct user 与 parent 已完成的交互。

本文只定义 Bound 的可观察行为。接口、数据结构、调用链和职责见[技术设计](./parent-bound-design.zh-CN.md)，配置组合细节见[Bound Config 管理](./bound-config-management.zh-CN.md)。

## 2. 已确认需求

1. Session header 不增加 Bound 字段；所有可恢复事实写入 append-only Session event。
2. binding 可以先于 child Session 和 child Agent 存在。
3. `Bind` 必须先成功提交并 `Flush`，之后才允许创建 child。
4. binding 与 revision 1 config 在 exact parent Session 的同一个 `Commit` batch 中提交。
5. `Bind` 必须提供非空 initial prompt，但 `Bind` 本身不得创建 child。
6. parent Agent 发布后，Subagent 根据 parent Session 中的 binding 尝试创建或恢复 Bound child。
7. 单个 child 的创建或恢复失败不得否决 parent Agent publication；失败写 parent materialization event，并交给 diagnostics。
8. fresh create 和 cold restore 使用相同 child Session ID；initial prompt 只在 child 尚未接受过自己的 Inbox message 时提交一次。
9. Bound child 成功启动后保持 resident；普通 idle 不结束它。
10. config 是完整快照并按 revision 递增，不是字段 patch。
11. resident child 的 config 更新采用 exact Agent epoch replacement：先等待旧 Agent idle并停止旧 epoch，再按最新 revision 创建新 epoch。禁止原位修改 prompt 或 Tool。
12. cold child 的 config 更新只写 parent Session；不因此创建 child。
13. `Enabled=false` 停止 resident epoch，并拒绝后续 Bound `Send`，直到新 revision 再次启用。
14. prompt 由 System Prompt 现有 section 机制组合；Tool restriction 由 Tools 现有 policy 机制应用。Subagent 不定义第二套 prompt 或 Tool model。
15. Extension Registry 同时支持无名 common Extension 和有名 selected Extension。每个 binding 可按名称和顺序选择不同 Extension。
16. 有名 Extension 的注册方或 Registry shutdown 可以注销 registration；Bound 不发起注销，也不为不存在的运行期动态卸载场景定义 callback。
17. Bound child 只通过显式 report Tool 向 parent report；idle、stop 或 config update 不隐式生成 report。
18. binding 本身表达 parent-to-child 观察关系；不增加通用 subscription schema、Session-owned EventBus或任意 event predicate。
19. 只在 `turn/end` 后处理完整 parent turn；只有 source kind 为 `user` 的 `user/message` 使该 turn产生 child delivery。
20. parent relay 使用 canonical `assistant/message`，过滤 reasoning、tool-call和tool-result；report-only turn不得回灌child。
21. worker必须先Flush parent source prefix，再产生child effect；child receipt Flush成功后才允许推进parent cursor。
22. cursor作为exact parent Session中的`subagent/bound-cursor`事件保存，不进入公共Session Projection，也不建立独立cursor Store。
23. crash发生在child receipt durable之后、parent cursor之前时，恢复只能补cursor，不得再次Followup。
24. cursor floor必须排除binding建立前的history与fork已继承的parent prefix；落在partial turn中时跳过该partial turn。
25. `Enabled=false`暂停interaction delivery并保留backlog；再次启用和materialize后主动catch-up。
26. 独立 Bound config Store和原位replacement API不实现，也不放置空接口。

## 3. 三种 Mode 的边界

| Mode | 首次启动输入 | idle 后 | 后续发送 | 配置来源 |
| --- | --- | --- | --- | --- |
| OneShot | `OneShotStartCommand` | 结束当前 Execution | 不恢复 | command snapshot |
| Continuable | `ContinuableStartCommand` | 当前 Execution settle；Session 可冷恢复 | `Resume` | durable descriptor |
| Bound | `BoundStartCommand` 只含 exact parent 和 child ID | Agent epoch 保持 resident | `Bound.Send`；cold 时先恢复 | exact parent Session binding/config events |

Bound 可以复用 `agent.Constructor`、`Execution`、SeedBuilder、child policy 和 Extension Provisioner，但不能用 Continuable flag 表示。

## 4. 上下游交互

配置源、Agent lifecycle、`ChildControl`和现有`session.EventAppended`是上游入口；Session、projection、Agent Constructor和Extension Registry是下游能力。`session.EventAppended`只唤醒Bound worker，不是durable subscription事实。完整主动调用方向和流程图只在[技术设计第3节](./parent-bound-design.zh-CN.md#3-依赖与主动调用方向)维护。

## 5. 用例需求

### 5.1 Bind

输入必须包含 exact live parent、SeedBuilder、parent 内唯一 title、initial prompt、Agent options 和完整 revision 1 config。可由 caller 指定 child Session ID；未指定时生成新 ID。

成功顺序必须是：

1. 验证 parent、lineage、SeedBuilder、creation、config 和 selected Extension 名称；
2. 验证 child ID 在 live Agent、live Session、持久化 Session 和 parent binding 中均未占用；
3. 在 exact parent Session 同一 batch 写 `subagent/bound-binding` 与 `subagent/bound-config` revision 1；
4. `Flush` exact parent Session；
5. 返回 binding identity，不创建 child。

### 5.2 Parent publication

`agent.SessionStarted` 到达时，Subagent 读取 exact parent projection，逐个调用 Bound Start。observer 必须立刻返回，使 child failure 不参与 parent publication 的 veto 路径。每个 child 的失败由 Bound 自己记录和报告。

### 5.3 Fresh create 与 cold restore

Bound Start 必须读取 binding 和 latest config。fresh create 使用 binding 的 SeedBuilder、Agent options、lineage和child ID；cold restore必须核对parent identity、Seed boundary和`BoundDescriptor`。

在 child publication 前必须完成：

- Persona、Tool restriction、common Extension 和 selected Extension provisioning；
- `subagent/bound-config-applied` child event提交；
- 必要时提交 initial prompt；
- child Session `Flush`。

成功后才发布公共 `Execution` 和 `subagent/started`。

### 5.4 UpdateConfig

caller 必须提供 expected revision。Bound 验证完整 snapshot 与 selected Extension，提交 revision N+1 并 `Flush` parent Session。若 child cold，调用结束；若 child resident，在同一 parent-child `bindingSlot` 内替换 exact Agent epoch。revision 冲突必须拒绝且不得写 event。

### 5.5 Send

统一 Service 先查 resident Execution：resident Bound 分派给 `Bound.Send`；不存在 resident Execution 时，先查 exact parent binding，存在则 cold restore Bound，否则交给 Continuable resume。Bound config replacement 与 message admission 使用同一个 parent-child `bindingSlot`。

### 5.6 Parent interaction delivery

每个materialized binding拥有一个`interactionDelivery`。它从durable cursor fold下一条closed parent turn；没有direct user输入的turn只推进`skipped`。需要投递时，它以稳定parent seq range构造`subagent-delivery` message source，扫描child `agent/inbox/spliced` receipt去重，Flush child后再以Session FIFO head上的expected cursor提交`subagent/bound-cursor`。

完整调用链、filter、floor和crash恢复语义见[Parent Interaction投递设计](./session-event-delivery.zh-CN.md)。

### 5.7 Close、interrupt 与 report

- Interrupt 取消当前 turn但保留 Bound Inbox 和 resident epoch。
- parent close、child external close或module shutdown结束 exact epoch并完成 Session flush。
- child 只有调用 report Tool 才向 parent写 report；report调度策略由report Plugin拥有。

## 6. 持久化不变量

必须满足：

- 同一parent内child ID和binding title都唯一；
- config必须已有binding，从revision 1开始并连续递增；
- materialization必须引用当前存在的matching config revision；
- binding/config/materialization与applied分开投影；
- child Agent resident状态只存在于进程内Execution Registry，不伪装成durable Session事实；
- binding/config event不修改Session header。
- 每个child的cursor只能从derived floor连续前进，不允许回退、跨越或接受重复receipt；
- child receipt先于对应`delivered` cursor durable；`skipped` cursor只能对应已closed且没有direct user输入的turn；
- cursor是Bound私有fold事实，不加入公共`subagent-bound` projection value。

## 7. 失败语义

- Bind validation、Commit或Flush失败：返回错误，不创建child。
- parent publication期间单个child create/restore失败：parent继续发布；parent记录failed materialization并报告diagnostics。
- config revision已durable但resident replacement失败：返回committed result和error；latest revision保留，下一次`Send`或启动可重试。
- child final Flush失败：不回滚已经结束的epoch，交给diagnostics。
- parent source Flush失败：不产生新的child effect；child Followup或receipt Flush失败：不推进cursor。
- cursor Commit或Flush失败：报告diagnostics并在后续catch-up中从child receipt恢复。
- malformed parent turn、cursor chain或receipt：停止该次catch-up，不猜测或跳过损坏事实。
- selected Extension在provisioning前不存在：Bind/Update或Start失败；不存在已运行Bound epoch的动态注销callback。

## 8. 未关闭事项

O11 的单parent并发启动上限、同步尝试预算和单child超时尚未确定，因此当前`StartBindings`按binding顺序逐个尝试，不宣称并发或超时保证。该问题需要独立需求值和取消语义后再实现，不能先加默认数字。

## 9. 验收场景

1. Bind成功后parent只有两个同batch事件、header不变且child不存在。
2. 缺少initial prompt、未知Extension、重复title或child ID时零事件提交。
3. parent publication即使某个child create失败仍成功，failed materialization可投影。
4. fresh create与cold restore只提交一次initial prompt，并复用child ID。
5. revision更新替换resident exact Agent，不重复initial prompt；disable停止epoch并拒绝Send。
6. cold Bound `Send`走Bound restore；无binding cold child走Continuable。
7. selected Extensions按config顺序安装，失败时完整回滚；common与selected职责分离。
8. malformed config-without-binding和materialization-without-matching-config被projection拒绝。
9. direct user与parent的完整turn只进入child Inbox一次；reasoning/tool-call和report-only turn不进入child。
10. child receipt已durable但parent cursor缺失时，恢复只补cursor且不重复Followup。
11. binding/fork floor不重放既有parent history；disable期间cursor不推进，重新启用后catch-up。
