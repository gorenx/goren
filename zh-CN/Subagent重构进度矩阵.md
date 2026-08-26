# Subagent 重构进度矩阵

状态：工作分支实时记录

最后核对：2026-08-26

分支：`refactor/agent-lifecycle`

## 1. 文档职责

本文只记录[Subagent 技术方案](./Subagent架构与生命周期重构技术方案.md)和[实施方案](./Subagent重构实施方案.md)在当前工作分支上的实际完成状态、代码证据、测试证据和剩余缺口。

状态定义：

- `已实现`：目标代码和调用者已经迁移，但不代表所有验证完成；
- `已验证`：相应 focused check 已实际运行通过；
- `进行中`：已有实质变更，但仍存在已知缺口；
- `待实施`：方案已确认但代码尚未修改；
- `待验证`：实现存在，但指定 Gate 尚未运行或需要重跑。

本文不替代全局 [`08-implementation-progress.md`](./08-implementation-progress.md)。本轮确认前不更新全局进度。

## 2. 模块进度

| 阶段 | 模块 | 实现 | 验证 | 主要证据 |
| --- | --- | --- | --- | --- |
| S0 | 公共契约 | 已实现 | 已验证 | `subagent/lifecycle.go`、`control.go`、`directory.go`、`extension.go`、`contracts_test.go` |
| S1 | SeedBuilder | 已实现 | 已验证 | `internal/seedbuilder`、`spawn/builder.go`、`fork/builder.go` |
| S2 | Common Execution | 已实现 | 已验证 | `internal/execution`、统一 Execution state/terminal tests |
| S3 | OneShot | 已实现 | 已验证 | `internal/oneshot`、`one_shot_integration_test.go` |
| S4 | Continuable | 已实现 | 已验证 | `internal/continuable`、continuable/control/settlement integration tests |
| S5 | Unified Service | 已实现 | 已验证 | `internal/subagents.Service`、统一准入和 interrupt authorization |
| S6 | ChildDirectory | 已实现 | 已验证 | `internal/childdirectory`、cold/live/descendant/order tests |
| S6 | Projection Unit 行为 | 已实现 | 已验证 | `internal/projection` identity/timing tests 与 contract fixtures |
| S6 | Projection 接口收口 | 已实现 | 已验证 | `projection.Units()`、`projection.ReadIdentity()`；concrete Unit、key 和 codec 均为包内实现 |
| S7 | Child policy | 已实现 | 已验证 | `internal/childpolicy`、OneShot/Continuable 各自 provisioning |
| S7 | Continuable Extension | 已实现 | 已验证 | `internal/extension`、`tools/report` installation tests |
| S8 | Tool adapters | 已实现 | 已验证 | `tools/delegation`、`tools/control`、`tools/report` |
| S9 | Plugin composition | 已实现 | 已验证 | `plugin.Plugin`、factory、assembly 与 architecture tests |
| S10 | 文档同步 | 已实现 | 已验证 | 技术方案、实施方案、本矩阵、package README、术语与 deferred parent-bound 文档 |
| S10 | 全量验收 | 进行中 | 待验证 | focused tests 已通过；full/race/vet/build 需在最终代码后统一运行 |

## 3. 已完成的破坏性删除

以下旧抽象和目录已经从当前工作树删除，不保留兼容 wrapper：

- public Provider execution、OneShot Run、ContinuableService、Catalog；
- `internal/provider`；
- `internal/inprocess`；
- `internal/continuation`；
- `internal/catalog`；
- `internal/childscope`；
- `internal/identity`；
- 根级 `subagent/tool`、`subagent/control`、`subagent/report` 旧布局；
- Continuable Activation、ResidentChild 和 continuation drain/Quiesce 路径。

替代结构是：

- `SeedBuilder` + `internal/seedbuilder`；
- common `Execution` + `internal/execution`；
- `internal/oneshot` 与 `internal/continuable`；
- `internal/subagents.Service`；
- `ChildDirectory` + `internal/childdirectory`；
- `internal/childpolicy`；
- `tools/delegation`、`tools/control`、`tools/report`。

## 4. 当前已知缺口

### 4.1 最终结构审计

Projection 跨包接口和旧实现术语已经收口。最终验收前仍需完成整个 Subagent 模块的导出面、重复 consumer port、重复状态和无调用声明审计。已确认的具体残留是未被当前 Consumer 使用的预留枚举 `DiagnosticUnsupported`；是否删除以最终代码审计结果为准，不把预留声明当成已实现能力。

### 4.2 Future parent-bound 决策

Parent-bound Subagent 尚未实现。需求和技术草案已经按 common Execution、SeedBuilder 和 ChildDirectory 重新整理，但 O1-O8 尚未确认，不能据此解释现有行为或开始实现。

### 4.3 全量验证

当前重构期间已经运行并通过 focused Subagent、assembly、architecture tests 和 scoped staticcheck；由于最终结构审计尚未完成，以下全量 Gate 尚不能标记为最终通过：

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
go test -tags contract ./agent ./subagent ./subagent/internal/execution ./subagent/internal/projection ./subagent/internal/childdirectory
git diff --check
```

## 5. 验收证据矩阵

| 行为 | Owner | 实现证据 | 测试证据 | 状态 |
| --- | --- | --- | --- | --- |
| closed StartCommand variants | `subagent` | `lifecycle.go` | `contracts_test.go` | 已验证 |
| common Execution state/terminal | `internal/execution` | `execution.go` | `execution_test.go` | 已验证 |
| OneShot auto-release | `internal/oneshot` | `service.go`、`terminal.go` | `one_shot_integration_test.go` | 已验证 |
| Continuable cold resume | `internal/continuable` | `start.go`、`messaging.go` | `control_integration_test.go` | 已验证 |
| interrupt keeps queued followup | `internal/continuable` | `messaging.go` | `interrupt_agent_integration_test.go` | 已验证 |
| child-to-parent report | `internal/continuable` / `tools/report` | `messaging.go`、report Tool | `report_integration_test.go` | 已验证 |
| settlement outcome | `internal/continuable` | `settlement.go` | `settlement_outcome_integration_test.go` | 已验证 |
| single terminal transaction | `internal/execution` | `Stop`/`StopAndWait` | `execution_test.go` | 已验证 |
| bounded cold directory reads | `internal/childdirectory` | `resolve.go` | `cold_read_test.go` | 已验证 |
| stable child ordering | `internal/childdirectory` | `order.go` | `order_test.go` | 已验证 |
| descriptor/timing projections | `internal/projection` | `identity.go`、`timing.go` | projection and contract tests | 已验证 |
| projection 跨包封装 | `internal/projection` | `Units()`、`ReadIdentity()` | projection/directory/runtime focused tests | 已验证 |
| Plugin/business separation | `plugin` / application modules | package dependency direction | `tests/architecture` | 已验证 |
| full repository health | repository | final worktree | full Gate | 待验证 |

## 6. 本轮文档验证

以下检查已在 2026-08-26 对当前工作树实际运行通过：

- Subagent 主文档、局部文档和 package README 共 28 个 Markdown 文件的相对链接检查；检查器忽略 fenced code 与 inline code；
- `git diff --check`；
- 当前 Subagent 文档中的已删除包路径检查；旧路径只保留在实施方案和本矩阵的“删除证据”中；
- 当前职责术语检查；`Activation`、`Catalog`、Provider execution 只保留在兼容/删除说明中，不再作为当前对象。
- `go test ./subagent/... ./internal/assembly ./tests/architecture`；
- `go run honnef.co/go/tools/cmd/staticcheck@latest ./subagent/... ./internal/assembly ./tests/architecture`。

这些证据只覆盖列出的范围，不代表 race、contract 或全仓构建已经通过。

## 7. 下一步顺序

1. 完成导出面、重复 consumer port、重复状态和无调用声明审计。
2. 根据审计结果完成最后一轮破坏性清理。
3. 运行 focused、race、contract 和全仓 Gate。
4. 根据真实结果更新本矩阵。
5. 用户确认后再同步全局设计索引与全局实施进度。
