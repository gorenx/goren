# Continuation

本包拥有 continuable child 的创建与冷恢复、exact parent authorization、唯一 Inbox 投递、Activation residency、interrupt、report、settlement 和 child-first drain。Session log 是 durable truth；`Activation` 只表示一个进程内驻留 epoch。

稳定的 `Service` 实现 `ContinuableService`，并在 Plugin activation 期间启用一个 `Manager`；缺少可选 Agent Registry 或 Session LiveStore 时，Service 保持可解析但 continuable 操作返回稳定 unavailable 错误。`Manager` 通过 consumer-owned ports 使用 Agent Registry、Session LiveStore、Persistence、Provider Registry、`ScopeBuilder`、生命周期发布器与 contained failure reporter。它不实现 Agent Loop、Provider 算法、持久化 I/O，也不拥有 Extension registration、child Scope 内容或 Catalog。

| 文件 | 职责 |
| --- | --- |
| `service.go`、`service_api.go`、`service_events.go` | 稳定 capability、Manager 启停、Consumer API 与 Agent Event 入口 |
| `manager.go` | 依赖端口与进程内 continuation coordinator |
| `activation.go` | `residency`、Activation 与 materialization 状态所有权 |
| `manager_start.go`、`materialization.go` | fresh Start、cold resume 与 Handle publication |
| `manager_delivery.go` | Followup、Interrupt、Report 与 Inbox acceptance |
| `manager_settlement.go` | idle convergence 与 parent settlement notice |
| `disposal.go`、`failures.go` | memoized release transaction、child-first teardown 与 contained flush failure |
| `outcome.go`、`output.go` | Activation event suffix 的 StopReason 映射与最终输出捕获 |
| `manager_drain.go` | scoped/global cutoff、materialization barrier 与 child-first drain |
| `start_request.go`、`start_descriptor.go` | caller input snapshot、descriptor 与 Provider preparation |
| `identity.go`、`errors.go` | Session/Run identity 与稳定操作错误 |

```mermaid
flowchart LR
    Plugin[runtime.Plugin] -->|ProvidedService| Service
    Service --> Manager
    Manager --> AgentRegistry[agent.Registry]
    Manager --> Inbox[Agent Inbox]
    Manager --> Persistence[Session Persistence]
    Manager --> ScopeBuilder[internal/childscope]
    Manager --> Lifecycle[subagent start/end]
```

取消调用只影响 Inbox 接受前的 admission。`Interrupt` 使用 `KeepInbox=true` 发出取消后立即返回；drain 关闭 admission、等待已进入的 materialization，再按 child-first 顺序释放 handle。自然 settlement、显式 drain 与外部 Agent disposal 都以同一个 `disposal` 状态表示准入截止；下一 epoch 必须等待旧 epoch 发布 terminal edge 并释放 ownership。

跨包合同见[领域设计](../../docs/design.zh-CN.md)，实现证据见[进度](../../docs/implementation-progress.zh-CN.md)，服务端测试暴露的问题见[问题记录](../../docs/server-test-findings.zh-CN.md)。
