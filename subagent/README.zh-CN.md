# Subagent

`subagent` 是父 Agent 委派子任务的领域能力。它统一拥有 Provider 注册、one-shot 运行、continuable 子会话、父子授权、durable descriptor、生命周期事件、子树清理和只读目录；Agent Loop 仍只负责单个 Agent 的 Inbox、轮次与取消。

当前实现依据 DeepSeek Harness `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e` 分析。该提交只是 Subagent 的 feature-local 参考，不改变仓库在 [01 复制范围与兼容基线](../zh-CN/01-porting-scope-and-baseline.md)中固定的全局基线。

## 文档导航

### 当前实现与依据

- [领域设计](./docs/design.zh-CN.md)：职责、接口、状态、依赖和关键流程。
- [术语规范](./docs/terminology.zh-CN.md)：兼容词汇、领域术语和 Go 命名规则。
- [服务端测试问题记录](./docs/server-test-findings.zh-CN.md)：测试暴露的问题、根因、结构性修复和复查证据。
- [DSH 源证据](../zh-CN/subagent/01-source-capability-analysis.md)：固定 feature-local commit 下的源 owner、符号与兼容差异。
- [全局实施进度](../zh-CN/08-implementation-progress.md)：仓库级证据索引，只记录 Subagent 已进入实现、Jobs/Workflow 仍 deferred。

### 临时设计文档

Parent-bound Subagent 仍是待确认的临时设计议题，相关需求和技术草案不属于当前文档集，因此不在这里建立导航。确认前，它们不得作为当前领域设计、兼容基线、实现承诺或现有 one-shot、continuable 和 Activation 行为的解释依据。

## 领域边界

`subagent` 拥有：

- `ProviderRegistry`、`OneShotService`、`ContinuableService`、`ExtensionRegistry` 和 `Catalog` 五个面向不同 Consumer 的能力；
- one-shot `Run` 的发布边界和 `subagent/start`、`subagent/end` 生命周期；
- continuable child 的 durable identity、Activation residency、冷恢复、续投授权、report、interrupt 和 child-first drain；
- `subagent/descriptor` 的严格 codec、父子 MessageSource 和稳定错误码。

`subagent` 不拥有：

- 单个 Agent 的模型循环、Inbox、Session append 或持久化 I/O；
- Provider 的具体 spawn/fork 算法；
- Tool、Host API、Echo 或 WebSocket DTO；
- `subagent-acp`、`subagent-codex`、`subagent-claude-code`、`subagent-dsh-sdk`。

## 运行关系

Provider、Activation Extension 和 `runtime.Plugin` 处于不同层次：

| 角色 | 用途 | 不拥有 |
| --- | --- | --- |
| Provider | 声明一种子 Agent 创建策略；处理 one-shot `Start`，支持 continuable 时只准备 fresh child 的持久化 seed | continuable child 的驻留、恢复、投递与回收 |
| Activation Extension | 在 continuable child 每次形成未发布 Activation 时，把当前注册的 child-scoped 能力安装进该 Scope，并返回可精确撤销的 Installation | Provider 创建策略、Agent Loop 或 Plugin 生命周期 |
| `runtime.Plugin` | 装配并发布五个独立 Service，解析 Agent、Session 等依赖，管理模块启停和失败回滚 | Provider 或 Extension 的具体业务实现，也不代替 Agent Runtime |

```mermaid
flowchart LR
    ProviderPlugin[Provider Plugin] -->|RegisterProvider| Registry[ProviderRegistry]
    ExtensionPlugin[Extension Plugin] -->|RegisterExtension| Extension[ExtensionRegistry]
    Runtime[subagent/runtime.Plugin] --> Registry
    Runtime --> OneShot[OneShotService]
    Runtime --> Continue[ContinuableService]
    Runtime --> Extension
    Runtime --> Catalog
    Tool[Tool / Host Consumer] --> OneShot[OneShotService]
    Tool --> Continue[ContinuableService]
    Control[Control Consumer] --> Continue
    Control --> Catalog
    OneShot -->|Start| Provider[Provider]
    Continue -->|PrepareContinuable，仅 fresh child| Provider
    Continue --> Agent[agent.Registry / Agent Inbox]
    Continue --> Session[session.LiveStore / Persistence]
    Continue --> ChildScope[未发布 child Scope]
    Extension -->|每次 Activation 安装当前扩展| ChildScope
    Catalog --> Session
```

注册只让 Provider 或 Extension 可被后续用例发现，不会主动创建 child。`OneShotService` 或 `ContinuableService` 被 Tool/Host 调用后才查找 Provider；Extension 则由 continuable child 的 Scope 物化过程按当时注册快照安装。`subagent` 根包只声明领域公开契约和共享值对象；已实现用例按内聚能力划分为 `internal/provider`、`internal/oneshot`、`internal/continuation`、`internal/childscope`、`internal/inprocess`、`internal/lineage`、`internal/extension`、`internal/catalog` 和 `internal/projection`。spawn/fork Provider 与 tool/control/report Consumer 位于领域内独立子模块。`subagent/runtime.Plugin` 是核心 Service 的唯一 Plugin 装配入口，但不实现这些业务接口；具体 Provider 与 Consumer 由各自 Factory 装配。

## 生命周期与失败

- Provider 注册先检查名称唯一性，再发布 `subagent/provider-added`；有序 observer 拒绝时回滚注册。精确 registration 撤销后发布 best-effort `subagent/provider-removed`。
- one-shot `Start` 在 Provider 成功发布 `Run` 后才返回；启动失败不产生运行生命周期。返回后由 holder 负责 `Run.Dispose`，Runtime 观察 terminal result 并保证 start 先于 end。
- continuable 以 Session log 为事实来源。Activation 只是一个进程内驻留周期，不是可持久化业务事实。
- Continuation Manager 判断 Activation 何时结束，只持有 Registry 返回的 exact `agent.Handle`；真正的 Agent tree 回收由 Agent Runtime lifecycle 执行。private activation owner 通过 `agent.Custody` 托管 resident child，使 Subagent 卸载时先结构回收 child，再停用领域 Service。
- `context.Context` 只取消接受前的操作或等待；已被 Inbox 接受的消息不因调用方随后取消而撤回。
- `Interrupt` 是发出取消后立即返回，不等待 Agent idle；是否保留 Inbox 由 Agent cancel contract 决定。
- drain 阻止目标父树继续 admission，等待已进入的创建事务，再按 child-first 顺序释放 resident handle。

精确完成状态和测试证据以[全局实施进度](../zh-CN/08-implementation-progress.md)为准；临时设计文档不得作为现有行为已经实现的证据。
