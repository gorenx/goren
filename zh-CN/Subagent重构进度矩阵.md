# Subagent 重构进度矩阵

状态：工作分支实时记录

最后核对：2026-08-27

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
| S3 | OneShot | 已实现 | 已验证 | `internal/oneshot`、OneShot terminal 与 report integration tests |
| S4 | Continuable | 已实现 | 已验证 | `internal/continuable`、continuable/control/settlement integration tests |
| S5 | Unified Service | 已实现 | 已验证 | `internal/subagents.Service`、统一准入、resident Agent 投递和 interrupt authorization |
| Bound 骨架 | Mode、命令、descriptor、projection、listing、Service 装配 | 已实现 | 已验证 | `lifecycle.go`、`descriptor*.go`、`internal/bound`、`internal/subagents`、`plugin/plugin.go` |
| Bound Start | config source、fresh create、existing resume、Execution publication | 进行中 | 已验证 TODO 失败 | `internal/bound.Service.Start` 当前返回 `ErrStartNotImplemented`；`service_test.go` 固化该边界 |
| S6 | ChildDirectory | 已实现 | 已验证 | `internal/childdirectory`、cold/live/descendant/order tests |
| S6 | Projection Unit 行为 | 已实现 | 已验证 | `internal/projection` identity/timing behavior tests |
| S6 | Projection 接口收口 | 已实现 | 已验证 | `projection.Units()`、`projection.ReadIdentity()`；concrete Unit、key 和 codec 均为包内实现 |
| S7 | Child environment | 已实现 | 已验证 | `internal/childpolicy`、`plugin/environment.go`、`plugin/one_shot_descriptor.go`、`plugin/structured_output.go`、两种模式拥有的 Environment Builder 端口 |
| S7 | Child Extension | 已实现 | 已验证 | OneShot/Continuable 共用 `internal/extension`、report installation/integration tests |
| S8 | Tool adapters | 已实现 | 已验证 | `tools/delegation`、`tools/control`、`tools/report` |
| S9 | Plugin composition | 已实现 | 已验证 | `plugin.Plugin`、factory、assembly 与 architecture tests |
| S10 | 文档同步 | 已实现 | 已验证 | 技术方案、实施方案、本矩阵、package README、术语与 deferred Bound 文档 |
| S10 | 全量验收 | 已实现 | 已验证 | full Go Gate、Assembly/Subagent integration 与真实 Provider OneShot acceptance |

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
- `ParentReporter`、mode implementation 上的 `messenger/reporter` 发现与缓存；

替代结构是：

- `SeedBuilder` + `internal/seedbuilder`；
- common `Execution` + `internal/execution`；
- `internal/oneshot` 与 `internal/continuable`；
- `internal/subagents.Service`；
- `ChildDirectory` + `internal/childdirectory`；
- `internal/childpolicy`；
- `tools/delegation`、`tools/control`、`tools/report`。
- resident child 消息直接使用 `agent.Agent`，cold resume 只进入 Continuable lifecycle strategy；
- report Tool 通过消费端窄 Agent Registry 直接调用 parent Agent。

## 4. 当前已知缺口

### 4.1 最终结构审计

Projection 跨包接口、导出面、consumer-owned ports、状态所有权和无调用声明已完成审计。`DiagnosticUnsupported`、`InitialMessageID`、`ParentSnapshot`、`SeedPolicy` 等未被真实 Consumer 使用的预留面已删除；`assistantoutput`、`childrequest` 等无独立状态的小包已并回实际 owner。OneShot/Continuable 只依赖自己拥有的 Environment Builder 端口，不导入 Plugin Runtime 或 child Plugin adapter；该依赖方向由 architecture test 固化。

### 4.2 Bound 后续实现

Bound 已作为独立 `ModeBound` 进入代码。`StartCommand` 已从单体结构重构为封闭接口，具体 variant 是 `OneShotStartCommand`、`ContinuableStartCommand` 和只携带 exact parent/child identity 的 `BoundStartCommand`；统一 Service 消费 `oneShot`、`continuable`、`bound` 三个完整模式接口。Bound descriptor、identity projection、ChildDirectory/list_agents 映射和 Plugin 装配已经实现。

`bound.Service.Start` 按要求保持 TODO，并返回明确的 `ErrStartNotImplemented`。binding event/projection、immutable creation input、config source、materialization state、cursor、`agent.Created` Bound observer、fresh create/existing resume、next-turn config 和 Bound settlement 均未实现。O4、O5、O7、O11、O12、O13 仍未关闭；不得把结构实施解释为已有可运行 Bound child。

### 4.3 全量验证

当前工作树已运行并通过 focused Subagent、assembly、architecture tests、scoped staticcheck 以及默认全仓 Go Gate：

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

仓库级 TypeScript contract suite 及其遗留的 12 个 contract-tag 测试已经统一移除，不再导入不存在的 `github.com/gorenx/goren/tests/contract/fixture`；`go mod tidy` 已恢复通过。Subagent 的常规 Go behavior、integration 与 Architecture tests 保持为验收依据。

本轮还显式加载根 `.env` 并设置 `GOREN_REAL_PROVIDER_TEST=1`：真实 Provider OneShot acceptance 在补齐测试 fixture 的标准 Session durability plugins 后通过，验证 marker、start/end lifecycle、OneShot release 和 observer failure 数量；credential 未进入日志或 fixture。

## 5. 验收证据矩阵

| 行为 | Owner | 实现证据 | 测试证据 | 状态 |
| --- | --- | --- | --- | --- |
| sealed StartCommand family | `subagent` | `lifecycle.go` 的三个具体 command | `contracts_test.go`、`internal/bound/service_test.go` | 已验证 |
| Bound mode dispatch | `internal/subagents` | `oneShot`/`continuable`/`bound` interfaces、`Service.Start` | `TestServiceStartDispatchesBoundCommand` | 已验证 |
| Bound descriptor/listing | `subagent` / projection / directory | `BoundDescriptor`、`BoundChildEntry` | descriptor/projection/directory/control focused tests | 已验证 |
| Bound runtime start | `internal/bound` | `Service.Start` | `TestStartReturnsExplicitPendingError` | TODO，明确失败 |
| common Execution state/terminal | `internal/execution` | `execution.go` | `execution_test.go` | 已验证 |
| OneShot auto-release | `internal/oneshot` | `service.go`、`terminal.go` | `one_shot_integration_test.go` | 已验证 |
| real Provider OneShot | Subagent + DeepSeek Adapter | `real_provider_integration_test.go` | `TestRealProviderForegroundOneShot` | Environment Verified |
| Continuable cold resume | `internal/continuable` | `start.go`、`messaging.go` | `control_integration_test.go` | 已验证 |
| interrupt keeps queued followup | `internal/continuable` | `messaging.go` | `interrupt_agent_integration_test.go` | 已验证 |
| resident parent-to-child message | `internal/subagents` / `agent` | `Service.Send`、`agent.Agent.Followup` | `service_test.go`（OneShot/Continuable） | 已验证 |
| child-to-parent report | `tools/report` / `agent` | report Tool 的 consumer-owned live Agent view 与 `Inject/Steer` | OneShot/Continuable report integration tests | 已验证 |
| settlement outcome | `internal/continuable` | `settlement.go` | `settlement_outcome_integration_test.go` | 已验证 |
| single terminal transaction | `internal/execution` | `Stop`/`Wait`/`Result`/`StopAndWait` | `execution_test.go` | 已验证 |
| bounded cold directory reads | `internal/childdirectory` | `resolve.go` | `cold_read_test.go` | 已验证 |
| stable child ordering | `internal/childdirectory` | `order.go` | `order_test.go` | 已验证 |
| descriptor/timing projections | `internal/projection` | `identity.go`、`timing.go` | projection behavior tests | 已验证 |
| projection 跨包封装 | `internal/projection` | `Units()`、`ReadIdentity()` | projection/directory/runtime focused tests | 已验证 |
| Plugin/business separation | `plugin` / application modules | package dependency direction | `tests/architecture` | 已验证 |
| default full repository health | repository | final worktree | test/race/vet/build/diff Gate | 已验证 |
| dependency tidy | repository | TypeScript contract 残留已删除 | `go mod tidy` | 已验证 |

## 6. 本轮文档验证

以下检查已在 2026-08-27 对当前工作树实际运行通过：

- 15 个 changed/new Markdown 文件的相对链接和 fenced block 检查；
- Bound-as-Continuable 过时断言检查；
- `git diff --check`；
- `go test ./subagent/...`、`go test ./tests/architecture`；
- `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、`go fmt ./...`、`git diff --check`。

这些证据不包含已删除的 TypeScript DSH 对照；真实 Provider 环境验收已由 `TestRealProviderForegroundOneShot` 独立执行通过。

## 7. 下一步顺序

1. Subagent 重构范围已收敛；保持当前破坏性 API，不增加过渡 wrapper。
2. 不恢复已删除的 `tests/contract/fixture` 或 Node/TypeScript contract runtime；新增兼容证据采用 owner 内固定 fixture 或 Go differential test。
3. 用户确认后再同步全局设计索引与全局实施进度。
