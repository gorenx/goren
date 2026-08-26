# Subagent 服务端测试问题记录

状态：Resolved Findings

本文记录测试曾暴露的结构问题、根因和当前修复落点。它不是架构或进度真相源；当前职责见[技术方案](../../zh-CN/Subagent架构与生命周期重构技术方案.md)，完成状态见[进度矩阵](../../zh-CN/Subagent重构进度矩阵.md)。

以下固定源引用沿用既有验证记录，本轮文档同步没有重新与 DeepSeek Harness checkout 比较。

## 1. 创建事务早于首条消息接受结束

现象：child Agent 已构造，但 initial message 尚未被 Inbox 接受时，外部 close 可能先结束构造等待；调用方随后得到成功，child 却已进入回滚。

根因：旧 materialization 只覆盖 Agent Handle publication，没有覆盖 initial Inbox acceptance 和失败回滚。

当前结构：OneShot 与 Continuable 都在 initial message 接受后才 `Execution.Activate` 并发布到 live Execution Registry。此前任一步失败都会释放 exact Handle，不返回成功 Execution。Agent parent-close 的 descendant admission 继续由 Agent lifecycle owner 线性化。

证据：`internal/oneshot/service.go`、`internal/continuable/start.go`、`agent/registry_test.go` 的 parent closing/construction tests，以及 Subagent integration tests。

## 2. 自然 settlement 与并发 Send 存在空窗

现象：旧 watcher 判断 child 已 idle 后，新的 Followup 可能先返回接受成功，随后又被旧 residency disposal 取消。

根因：旧实现用多个布尔值描述 admission cutoff 和 disposal completion；“settled recheck”与“安装停止事务”不是同一线性化边界。

当前结构：Continuable 用 per-child `childSlot` 串行化 current Execution，common `Execution.Stop` 原子 claim 唯一 terminal transaction。Send、watcher 和 detach 都重新核对 slot 中的 exact current Execution；旧 Execution 完成并 detach 后才允许 cold resume 形成下一次 Execution。

证据：`internal/continuable/messaging.go`、`settlement.go`、`internal/execution/execution_test.go`、`control_integration_test.go`。

## 3. 结果按最后一个事件猜测

现象：尾随且未消费 Inbox 输入的 turn 可能覆盖真正任务终态；空 assistant message 可能遮蔽已有可见输出；cold resume epoch 没有新输出时可能误用上一 epoch 内容。

根因：旧 OneShot/Continuable 各自倒序扫描 Session log，把“最后出现的事件”误当成“本次 Execution 的结果”，没有复用 Agent 的 consumed-work 语义，也没有共享输出选择规则。

当前结构：

- `agent.FoldConsumedWork` 唯一解释 Inbox claim/cancel 与 turn/step 消费关系；
- `internal/execution.SelectAssistantOutput` 选择本次执行事件片段中最后一个非空 assistant message，没有完整消息时才拼接 text delta；
- OneShot 使用 seed boundary，Continuable 使用本次 Execution boundary，避免跨 epoch 读取输出。

证据：`agent/consumed_work_test.go`、`internal/execution/output_test.go`、`internal/execution/output_contract_test.go`、`one_shot_integration_test.go`、`settlement_outcome_integration_test.go`。

## 4. Final flush failure 被误作结构释放失败

现象：child 已形成正常终态，但最终 Session flush 失败时，旧实现会把终态改成 `error`，混淆 durability warning 与 Handle teardown failure。

根因：持久化 checkpoint failure 与执行终止共用一个 failure channel。

当前结构：Continuable 的 `FinalFlushFailure` 通过 consumer-owned `FailureReporter` 交给 Runtime diagnostics。flush 失败不覆盖已经形成的 StopReason，且不会阻止 Execution Registry 移除和 exact Handle release。

证据：`internal/continuable/service.go`、`settlement.go`、`runtime/diagnostics.go` 和 settlement integration tests。

## 5. 外部 Agent closure 与 Subagent terminal ordering 分叉

现象：Agent 结构关闭与 Subagent 自然结束走不同释放路径时，可能提前移除 residency 或重复 Dispose Handle，使新的 Execution/lifecycle event 越过旧 `subagent/end`。

根因：旧实现把 Agent-owned close 与 Subagent-owned terminal transaction分别编码，并直接修改多份 residency state。

当前结构：Runtime 只观察 `agent/disposed` 并调用统一 Service 的 `AgentDisposed`。它按 exact Agent identity 找到 common Execution，使用 `StopExternal` join 同一个 terminal transaction；mode Terminator 不再次 Dispose 已经由 Agent 关闭的 Handle。

证据：`runtime/events.go`、`internal/subagents/service.go`、OneShot/Continuable terminators，以及 `report_integration_test.go` 的 runtime shutdown tests。

## 6. ChildDirectory 同时间 Session 排序不稳定

现象：多个 child 的 `createdAt` 相同时，大小写、重音、标点或 Unicode 等价形式在 Go 字节序与客户端基线排序中不同；map 枚举还会丢失首次插入顺序。

根因：旧目录只保存 `map[SessionID]sessionRecord`，覆盖 persisted/live record 时没有保留首次 ordinal；tie-breaker 也不是固定 locale collation。

当前结构：ChildDirectory 的 live-preferred merge 为每个 ID 保存首次 ordinal；同 `createdAt` sibling 使用固定 en-US collation，collation 相等时回退 ordinal。排序仍属于 ChildDirectory，不进入 Session 或 Projection。

证据：`internal/childdirectory/order.go`、`order_test.go`、`live_source_contract_test.go`、`cold_source_contract_test.go` 和 `descendants_source_contract_test.go`。

## 7. 当前验证边界

本轮重构期间 focused Subagent、integration、assembly 和 architecture tests 已运行通过，Projection API 与旧测试名/注释也已经收口。最终代码仍有导出面、重复状态/接口和无调用声明审计，以及 race、contract 和全仓 Gate，因此不能把历史验证命令当作当前工作树最终验收。

待执行命令及实际状态只在[进度矩阵](../../zh-CN/Subagent重构进度矩阵.md)更新。真实模型测试继续要求显式凭据、自跳过且不得把 secret 写入日志、fixture 或 Git。
