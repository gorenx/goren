# Subagent 服务端测试问题记录

状态：Resolved Findings

本文记录服务端测试实际暴露的代码问题、根因、结构性修复和复查证据。它不是第二份架构规范；稳定职责仍以[领域设计](./design.zh-CN.md)为准，完成状态仍以[实现进度](./implementation-progress.zh-CN.md)为准。

参考 DeepSeek Harness feature-local commit：`b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`。

## 1. 创建事务提前结束

现象：fresh Start 或 cold Followup 已发布 child，但首条消息尚未被 Inbox 接受时，scoped drain 可能结束等待；调用方随后得到成功，child 却已进入回滚或释放。

根因：`materialization` 只覆盖 Agent Handle publication，没有覆盖 initial prompt acceptance 与失败回滚。创建准入和业务提交不是同一个事务边界。

解决：把 materialization 延长到“publish、首条 Inbox 接受、失败回滚”全部完成；drain 先建立 lineage cutoff，再等待已准入事务结束。实现提交：`a35daa7`。

证据：`subagent/internal/continuation/materialization_drain_test.go` 覆盖 fresh create、cold resume、Inbox 拒绝和 publication 后 drain。

## 2. 自然 settlement 与 Followup 非线性化

现象：watcher 判断 child 已 idle 后、真正关闭准入前，并发 Followup 可能返回接收成功，随后又被旧 Activation 的释放取消。

根因：旧 Activation 用 `closing`、`disposeDone`、`disposeErr` 三份状态描述释放；“判断 settled”和“安装 admission cutoff”之间存在空窗，且自然结束和显式释放没有共享同一事务 owner。

解决：用 memoized `disposal` 表示唯一释放事务；它的存在就是 admission cutoff。watcher 在 per-child 串行边界内完成 settled recheck 和事务安装，所有调用方等待同一个 `done`，完成 terminal publication 和 ownership release 后才允许 cold resume。

DSH 证据：`packages/subagent/subagent/src/continuation.ts` 的 `dispose()`、`finishDisposal()`。Go 证据：`settlement_delivery_test.go`、`external_disposal_test.go`、`drain_order_test.go`。

## 3. 结果投影按最后一个事件猜测

现象：未进入 step 的尾随 turn 可能覆盖前一轮真正消费输入的终态；one-shot 因此会把已完成结果误报为 blocked/error。空的 usage-only `assistant/message` 会遮蔽更早的非空消息；取消发生在最终 message 提交前时，已经持久化的 text delta 又会被丢弃。冷恢复 epoch 没有新输出时，还可能复用上一 epoch 的 assistant message。

根因：`turn/end` 只描述一轮为何关闭，不描述哪一条 Inbox 输入被该轮消费；空 assistant message 也不表示此前可见输出失效。one-shot 与 continuable 各自复制日志倒序扫描，把“最后出现的事件”误作“当前子任务结果”，没有复用 Agent 拥有的 consumed-work 语义，也没有共享 DSH 的最终输出选择规则。

解决：核心 `agent` 的 `FoldConsumedWork` 继续唯一负责 Inbox claim/cancel、turn 与 step 的消费归属，one-shot 直接消费该结果，不再自行扫描 `turn/end`。Subagent 内部新增共享 `assistantoutput.Select`：选择最后一个非空 assistant message；没有非空 message 时只拼接 text delta，忽略 reasoning delta 和 tool result。continuation 先按 Activation boundary 截取 suffix，再调用同一选择器。解析失败或真正 teardown 失败不得发布不可确认的输出。

DSH 证据：`packages/core/agent/src/consumed-work.ts` 的 `foldConsumedWork()`、`packages/subagent/subagent/src/assistant-output.ts` 的 `finalAssistantOutput()`、`packages/subagent/subagent/src/lifecycle.ts` 的 `epochStopReason()`。Go 证据：`agent/consumed_work_test.go`、`agent/consumed_work_contract_test.go`、`subagent/internal/assistantoutput/output_test.go`、`subagent/internal/assistantoutput/source_contract_test.go`、`subagent/internal/inprocess/result_test.go`、`subagent/internal/continuation/output_test.go`、`settlement_outcome_integration_test.go`。带 `contract` tag 的测试在运行时校验 `../deepseek-harness` HEAD 必须等于 feature-local baseline，再直接调用固定源函数比较观测结果。实现提交：`4b19909`。

## 4. 最终 flush 被误作结构释放失败

现象：child 已正常完成，但最终 Session flush 失败时，Go 实现会把终态改成 `error`，并让显式 drain 返回 Activation teardown failure；handle 和 ownership 虽然最终仍会释放，但调用方看到的是结构释放失败。

根因：best-effort durability checkpoint 与 handle/child teardown 共用一个 failure slice，没有区分“冷恢复数据可能陈旧”和“结构释放本身失败”。

解决：continuation 定义 `FinalFlushFailure` 报告端口，runtime 转交进程 Diagnostics。flush 失败保留告警但继续 handle disposal 和 ownership release；只有 child teardown、idle convergence、输出捕获或 handle disposal 失败才覆盖终态。

DSH 证据：`packages/subagent/subagent/src/continuation.ts` 的 `flushFinalState()`。Go 证据：`final_flush_test.go` 和默认 assembly 的 `RuntimeOptions.ObserverError` 装配。

## 5. 外部释放提前开放下一 epoch

现象：外部 Agent disposal 先关闭等待信号、释放父子 ownership，随后才发布 `subagent/end`；并发 cold Followup 或父 Activation settlement 可能让新的 `subagent/start` 或父终态越过旧 child 的 `subagent/end`。

根因：外部释放路径没有遵守 manager-owned disposal 已有的 terminal ordering，且直接修改了多份 residency 状态。

解决：外部释放同样先安装 `disposal` cutoff 并移除可寻址 Activation，再发布 child terminal edge，最后释放 ownership、唤醒父 Activation 并关闭事务。证据：`external_disposal_test.go`。

## 6. Catalog 同时间 Session 排序与固定源不同

现象：多个 child 的 `createdAt` 相同时，包含大小写、重音、标点或 Unicode 等价形式的 `SessionId` 在 Go 与 DSH 中顺序不同；即使两个 ID 在 locale collation 下相等，Go 多次枚举还可能得到不同次序。

根因：DSH `packages/subagent/subagent/src/list-children.ts` 的 `compareCorpusRecords()` 使用 `id.localeCompare()`，并依赖 JavaScript `Map` 保留首次插入位置。Go 直接用字符串 `<`，得到 UTF-8 字节序；同时 live-preferred corpus 只有 `map[SessionID]sessionRecord`，在构造候选时已经丢失 persisted/live 合并后的首次插入位置。问题不在展示层：两边 `SessionId` 都允许任意字符串，没有 UUID 或小写约束。

解决：Catalog 在 live-preferred 合并时为每个 ID 保留首次出现的 ordinal，覆盖同 ID 的 persisted/live record 时不改变 ordinal；同一 `createdAt` 的 sibling 使用固定 en-US collation 复现基线运行时的 `localeCompare`，collation 相等时回退到 ordinal。排序仍由 Catalog 拥有，没有把展示规则推入 Session。

证据：`order_test.go` 覆盖 collation、Unicode 等价形式的稳定顺序和 `createdAt` 主键；`live_source_contract_test.go` 动态执行固定源 `listChildren()`，实际暴露旧字节序偏差后验证修复。cold 与 descendants 分别由 `cold_source_contract_test.go`、`descendants_source_contract_test.go` 对比固定源，取消转发和四路冷读上限由独立测试文件覆盖。实现提交：`2aa012a`。

## 7. 本轮验证

以下服务端验证均通过：

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
go test -tags contract ./agent ./subagent ./subagent/internal/assistantoutput ./subagent/internal/projection ./subagent/internal/catalog
go test ./subagent/internal/continuation -run '^TestFollowupWaitsForNaturalSettlementAndResumesDurableChild$' -count=100
go test ./subagent -run '^TestContinuableSettlementReports(MaxTokens|ModelFailure)FromAgentLog$' -count=50
go test -race ./subagent/internal/continuation -count=20
go test -race ./subagent ./subagent/internal/continuation -count=10
GOREN_REAL_PROVIDER_TEST=1 go test ./subagent -run '^TestRealProviderForegroundOneShot$' -count=1
```

真实 Provider 测试从本地 `.env` 注入进程环境；凭据未进入日志、fixture 或 Git。Web 到服务端测试按当前范围暂缓，不属于以上结论。
