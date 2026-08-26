# Agent 重构进度矩阵

状态：实现与全量验收已完成

更新时间：2026-08-25

实施方案：[Agent 重构实施方案](Agent重构实施方案.md)

设计依据：

- [Agent 构造与父子生命周期重构方案](Agent构造与父子生命周期重构方案.md)
- [Agent 构造事务与调用流程设计](Agent构造事务与调用流程设计.md)
- [Agent 生命周期与运行期父子所有权设计](Agent生命周期与运行期父子所有权设计.md)

## 1. 记录规则

本文只记录当前状态和可复核证据，不重复实施步骤或设计决策。

执行状态：

- `In Progress`：生产代码或文档已经修改，但该项所需 Gate 尚未全部完成；
- `Completed`：目标代码、直接调用方和该项要求的测试已经完成；
- `Blocked`：存在必须由外部决策解除的阻塞；
- `Deferred`：明确不进入本次重构。

证据等级：

- `Implemented`：当前源码已经符合目标边界；
- `Documented`：仅有独立需求或设计文档，不声明生产能力已实现；
- `Go Verified`：相关 focused 或 full Go tests 已通过；
- `Race Verified`：适用的 race Gate 已通过；
- `Acceptance Verified`：full test、full race、vet、build、architecture 与文档 Gate 全部通过。

## 2. 总览

| 范围 | In Progress | Completed | Blocked | Deferred |
| --- | ---: | ---: | ---: | ---: |
| Session / AgentSessions | 0 | 5 | 0 | 0 |
| Agent | 0 | 9 | 0 | 0 |
| AgentLoop | 0 | 9 | 0 | 0 |
| Subagent | 0 | 9 | 0 | 1 |
| 跨模块与验收 | 0 | 5 | 0 | 0 |

当前结论：四个业务模块的职责迁移、直接调用方、全仓普通测试、race、vet、build、架构约束和文档 Gate 均已通过。parent-bound 新业务能力不属于本次“现有职责重构”的实现范围，继续按其独立设计文档 Deferred。

## 3. Session 与 AgentSessions

| ID | 交付项 | 状态 | 证据等级 | 实现证据 |
| --- | --- | --- | --- | --- |
| AR-S01 | Session Store 与 Plugin adapter 分离 | Completed | Go Verified | [`session/memory_store.go`](../session/memory_store.go)、[`session/plugin.go`](../session/plugin.go) |
| AR-S02 | Prepare/Enter/Announce 与配对 Disposed 保持 Store-owned | Completed | Go Verified | [`session/memory_store.go`](../session/memory_store.go)、[`session/registration.go`](../session/registration.go) |
| AR-S03 | Session core 不触发 Agent 创建 | Completed | Go Verified | [`tests/architecture/agent_lifecycle_boundary_test.go`](../tests/architecture/agent_lifecycle_boundary_test.go) |
| AR-S04 | AgentSessions 只经 Constructor 创建/恢复普通 Agent | Completed | Go Verified | [`apiproxy/session/agent_sessions.go`](../apiproxy/session/agent_sessions.go) |
| AR-S05 | ordinary/subagent 分类只读取 durable Session Header | Completed | Go Verified | [`apiproxy/session/agent_sessions.go`](../apiproxy/session/agent_sessions.go) |

边界结论：Session 不依赖 Agent；`AgentSessions` 不拥有 epoch、runtime parent graph 或 Session 到 Handle 的第二套 lifecycle map；没有新增 `session/prepared`。

## 4. Agent

| ID | 交付项 | 状态 | 证据等级 | 实现证据 |
| --- | --- | --- | --- | --- |
| AR-A01 | 普通 `RegistryService` 与独立 Plugin adapter | Completed | Go Verified | [`agent/registry.go`](../agent/registry.go)、[`agent/plugin.go`](../agent/plugin.go) |
| AR-A02 | `LifecycleCoordinator` 拥有 exact epoch | Completed | Go Verified | [`agent/lifecycle_coordinator.go`](../agent/lifecycle_coordinator.go) |
| AR-A03 | lifecycle/publication/admission 使用明确状态轴 | Completed | Go Verified | [`agent/lifecycle_status.go`](../agent/lifecycle_status.go) |
| AR-A04 | `AgentEpoch.Attach` 与 `AgentTeardown` 单一绑定 | Completed | Go Verified | [`agent/factory.go`](../agent/factory.go)、[`agent/lifecycle_coordinator.go`](../agent/lifecycle_coordinator.go) |
| AR-A05 | managed Handle 直接引用 exact epoch | Completed | Go Verified | [`agent/handle.go`](../agent/handle.go) |
| AR-A06 | 显式 RuntimeParent、parent admission 和 child-first close | Completed | Go Verified | [`agent/registry.go`](../agent/registry.go)、[`agent/lifecycle_coordinator.go`](../agent/lifecycle_coordinator.go) |
| AR-A07 | Factory registration 与 Registry shutdown 分离 | Completed | Go Verified | [`agent/registry.go`](../agent/registry.go) |
| AR-A08 | Agent 业务接口脱离 Plugin identity | Completed | Go Verified | [`agent/agent.go`](../agent/agent.go) |
| AR-A09 | 旧 Custody、owner 查询和通用 lifecycle API 删除 | Completed | Go Verified | [`tests/architecture/agent_lifecycle_boundary_test.go`](../tests/architecture/agent_lifecycle_boundary_test.go) |

边界结论：`FactoryRegistration.Close` 只关闭 exact Factory 路由及其在途构造；`RegistryService.Shutdown` 才永久关闭 Registry 准入并收敛全部 epoch。AgentLoop 不拥有这两个状态机。

## 5. AgentLoop

| ID | 交付项 | 状态 | 证据等级 | 实现证据 |
| --- | --- | --- | --- | --- |
| AR-L01 | 普通 Go `Factory` 与根 Plugin 分离 | Completed | Go Verified | [`agentloop/construction.go`](../agentloop/construction.go)、[`agentloop/plugin.go`](../agentloop/plugin.go) |
| AR-L02 | `ReactLoopAgent` 与 Plugin lifecycle 分离 | Completed | Go Verified | [`agentloop/agent.go`](../agentloop/agent.go)、[`agentloop/agent_runtime_adapter.go`](../agentloop/agent_runtime_adapter.go) |
| AR-L03 | Create/Resume 接收 Registry-owned AgentEpoch | Completed | Go Verified | [`agentloop/construction.go`](../agentloop/construction.go) |
| AR-L04 | unpublished Scope preparation 与失败回滚 | Completed | Go Verified | [`agentloop/prepared_agent.go`](../agentloop/prepared_agent.go)、[`agentloop/scope_preparation.go`](../agentloop/scope_preparation.go) |
| AR-L05 | Session binding 与 AgentEpoch Attach publication 顺序 | Completed | Go Verified | [`agentloop/prepared_agent.go`](../agentloop/prepared_agent.go)、[`agentloop/session_binding.go`](../agentloop/session_binding.go) |
| AR-L06 | `agentScopeRoot` 表达单 Agent Scope，不表达 Agent tree | Completed | Go Verified | [`agentloop/scope_root.go`](../agentloop/scope_root.go) |
| AR-L07 | `agentScopes` 独占 Scope Handle 与结构卸载命令 | Completed | Go Verified | [`agentloop/agent_scopes.go`](../agentloop/agent_scopes.go) |
| AR-L08 | exact Factory registration 使用 commit-phase adapter | Completed | Go Verified | [`agentloop/registration_plugin.go`](../agentloop/registration_plugin.go) |
| AR-L09 | configured Agent 统一经 Constructor | Completed | Go Verified | [`agentloop/startup.go`](../agentloop/startup.go) |

边界结论：`agentScopeRoot` 不保存父 Plugin/Handle，不直接 Dispose 自己；`AgentScopeRuntime.Teardown` 把关闭请求交给 AgentLoop 的 `agentScopes`，再由 Plugin Runtime 卸载 exact isolated Scope。

## 6. Subagent

| ID | 交付项 | 状态 | 证据等级 | 实现证据 |
| --- | --- | --- | --- | --- |
| AR-U01 | spawn/fork Provider 与 Plugin adapter 分离 | Completed | Go Verified | [`subagent/spawn/provider.go`](../subagent/spawn/provider.go)、[`subagent/spawn/plugin.go`](../subagent/spawn/plugin.go)、[`subagent/fork/provider.go`](../subagent/fork/provider.go)、[`subagent/fork/plugin.go`](../subagent/fork/plugin.go) |
| AR-U02 | one-shot 与 continuable 显式传 RuntimeParent | Completed | Go Verified | [`subagent/internal/inprocess/driver.go`](../subagent/internal/inprocess/driver.go)、[`subagent/internal/continuation/manager_materialization.go`](../subagent/internal/continuation/manager_materialization.go) |
| AR-U03 | Activation 只保存 residency 与 settlement 状态 | Completed | Go Verified | [`subagent/internal/continuation/activation.go`](../subagent/internal/continuation/activation.go) |
| AR-U04 | runtime parent graph 和 child-first close 归 Agent | Completed | Go Verified | [`agent/lifecycle_coordinator.go`](../agent/lifecycle_coordinator.go)、[`subagent/internal/continuation/manager_settlement.go`](../subagent/internal/continuation/manager_settlement.go) |
| AR-U05 | Continuation 关闭只释放 exact managed Handle，不暴露 descendant close 命令 | Completed | Go Verified | [`subagent/internal/continuation/manager_disposal.go`](../subagent/internal/continuation/manager_disposal.go)、[`subagent/continuable.go`](../subagent/continuable.go) |
| AR-U06 | Runtime Plugin 组装独立业务 Service | Completed | Go Verified | [`subagent/runtime/plugin.go`](../subagent/runtime/plugin.go) |
| AR-U07 | Subagent Plugin unload 建立 resident child Closing cutoff，随后由 Agent lifecycle 收敛 Scope，且不关闭普通 parent | Completed | Race Verified | [`subagent/internal/continuation/manager_close.go`](../subagent/internal/continuation/manager_close.go)、[`subagent/report_integration_test.go`](../subagent/report_integration_test.go) |
| AR-U08 | durable lineage 只有 Session Header 一个深度事实源 | Completed | Go Verified | [`subagent/internal/lineage/lineage.go`](../subagent/internal/lineage/lineage.go) |
| AR-U09 | 旧 Subagent Agent graph/admission 标识符删除 | Completed | Go Verified | [`tests/architecture/agent_lifecycle_boundary_test.go`](../tests/architecture/agent_lifecycle_boundary_test.go) |
| AR-U10 | parent-bound 新 residency 能力 | Deferred | Documented | [parent-bound requirements](../subagent/docs/parent-bound-requirements.zh-CN.md) |

AR-U10 不表示 parent-bound 业务能力已经实现；它不计入本次职责重构完成条件。

## 7. 跨模块

| ID | 交付项 | 状态 | 证据等级 | 实现证据 |
| --- | --- | --- | --- | --- |
| AR-X01 | Plugin Runtime 保持回调内禁止拓扑重入，不增加 Agent/Subagent 例外 | Completed | Go Verified | [`plugin/operation.go`](../plugin/operation.go)、[`plugin/errors.go`](../plugin/errors.go) |
| AR-X02 | 业务类型与 Plugin adapter AST 边界 | Completed | Go Verified | [`tests/architecture/agent_lifecycle_boundary_test.go`](../tests/architecture/agent_lifecycle_boundary_test.go) |
| AR-X03 | 旧接口、状态和双轨 API 零生产引用 | Completed | Go Verified | [`tests/architecture/agent_lifecycle_boundary_test.go`](../tests/architecture/agent_lifecycle_boundary_test.go) |
| AR-X04 | 包内 README 与独立设计文档收口 | Completed | Acceptance Verified | `agent`、`agentloop`、`session`、`apiproxy/session`、`subagent` README 及本组文档 |
| AR-X05 | full race、vet、build、diff 与文档验收 | Completed | Acceptance Verified | 见第 8 节 |

## 8. 验证记录

| 日期 | 命令 | 结果 | 证明范围 |
| --- | --- | --- | --- |
| 2026-08-25 | `go test ./plugin ./agent ./agentloop ./subagent -count=1` | Pass | 四个直接实现包 |
| 2026-08-25 | Subagent Plugin unload 与 Runtime shutdown 两个 resident child 用例 `-count=10` | Pass | managed close 请求与结构 shutdown 两条关闭链 |
| 2026-08-25 | 上述 Subagent 用例 `go test -race -count=10` | Pass | 新增异步关闭请求的 focused race |
| 2026-08-25 | `go test ./tests/architecture -run TestIdentifierNamesDoNotShadowDeclarations` | Pass | 声明与局部标识符命名约束 |
| 2026-08-25 | `go test ./tests/architecture -count=1` | Pass | Agent 生命周期边界、Plugin policy 与命名架构约束 |
| 2026-08-25 | `go test ./... -count=1 -timeout=180s` | Pass | 全仓普通 Go tests |
| 2026-08-25 | `go test -race ./... -count=1 -timeout=300s` | Pass | 全仓并发与数据竞争检查 |
| 2026-08-25 | `go vet ./...` | Pass | 全仓静态诊断 |
| 2026-08-25 | `go build ./...` | Pass | 全仓生产包构建 |
| 2026-08-25 | `git diff --check` | Pass | 变更格式与空白错误检查 |
| 2026-08-25 | 本组设计文档与包内 README 本地 Markdown 链接检查 | Pass | 本次文档引用均可解析 |

## 9. 后续边界

- 本次重构不实现 AR-U10 parent-bound 新业务能力；它继续由独立需求与设计文档管理。
- 用户确认设计后，才单独更新 `zh-CN/README.md` 和 `zh-CN/08-implementation-progress.md`。
