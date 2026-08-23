# Agent 创建事务与 Provisioning 边界

状态：Agent provisioning 与 Subagent Extension Registry 已实现

本文只固定 Subagent 创建 child 时依赖的 Agent 组合事务，避免把细节继续堆入[03 Go 架构、接口与契约](./03-go-architecture-and-contracts.md)。实现进度见 [`subagent` 领域进度](../../subagent/docs/implementation-progress.zh-CN.md)，本文不更新全局权威索引。

## 1. 来源与结论

feature-local 来源为 DeepSeek Harness `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`：

- `packages/core/agent/src/index.ts` 定义 `AgentSetup` 与 `AgentSetupCommit`；
- `packages/core/agent-loop/src/index.ts` 的 `setupAndPublish()` 主动调用 setup、commit，再 publish；
- `packages/subagent/subagent/src/continuation.ts` 的 `materializeTracked()` 提供 child setup；
- `packages/subagent/subagent/src/activation-setup-registry.ts` 管理 contribution、resident installation 和 commit-time revocation。

DSH 的 `AgentSetup` 是来源术语：setup 函数可以不返回对象，只有 mutable provisioning 才返回可选 `AgentSetupCommit`。Go 不把两个对象压成强制实现空方法的 `Setup`；它分别表达为 `Provisioner` 和可选 `Provisioning`。Agent Loop 是创建用例协调者；Subagent 提供领域 Provisioner；Agent tree 是 Scope 与结构资源 owner。

## 2. Go 对象职责

| 对象 | 是什么 | 拥有 |
| --- | --- | --- |
| `agent.Registry` | Agent 创建入口和 live membership owner | Factory 注册、Create/Resume 委派、Enter/Announce/Remove |
| `agent.Factory` | concrete Agent provider 的创建边界 | Create/Resume 方法，不要求实现 Plugin |
| `agent.Provisioner` | 配置一个未发布 Agent Scope 的调用方对象 | Provision；清理尚未转交 Scope 的 partial acquisition |
| `agent.Provisioning` | Provision 返回的一次配置事务 | publication commit、rollback 与 resident Dispose |
| `agent.Scope` | 一个 active 但未发布 Agent 的组合边界 | exact Agent、Plugin mount、普通 effect ownership |
| `agentloop.Plugin` | 默认 Factory 与创建用例协调者 | Session preparation、Agent tree mount、Provisioner 调用、publication |
| `agentTree` | Agent 私有 Scope root | 基础 Plugin、动态 Plugin 与普通 effect 的结构生命周期 |
| `preparedAgent` | active 但未发布的创建事务 | tree、subject、lifecycle，成功后转移给 Handle |
| `agentMembership` | publication effect | Session/Agent Enter/Announce、work admission、Remove/Release |
| `agentLifecycle` | caller 的 exact tree 销毁权 | 一次幂等 Runtime tree unload |
| `constructionGate` | Factory 创建准入闸门 | active 状态和 in-flight Create/Resume join |

`constructionGate` 不保存 live Agent；live membership 已由 Registry 拥有，结构 teardown 已由 Runtime tree 和 Handle 拥有。也不再需要 `agentScope` 包装器：`agentTree` 直接实现 `agent.Scope`。

## 3. 契约

```go
type Provisioner interface {
	Provision(context.Context, Scope) (Provisioning, error)
}

type Provisioning interface {
	Effect
	Commit() error
}

type Scope interface {
	Agent() Agent
	Mount(context.Context, plugin.Plugin) (Effect, error)
	Own(Effect) error
}

type Effect interface {
	Dispose(context.Context) error
}
```

- `Provision` 只配置未发布 Scope，不能驱动 Agent；
- 没有剩余 publication validation 或 resident resource 时返回 nil，不制造空 `Commit`/`Dispose`；
- `Provisioning.Commit` 只做 publication 前同步复核，不发布 membership；
- `Provisioning.Dispose` 和 Effect disposal 必须幂等；
- `Scope.Mount` 返回 exact Effect，供 Provisioner 在需要时持有或转移；
- `Scope.Own` 接收非 Plugin effect，使它参与整棵 Agent tree 的逆序 teardown。

## 4. 调用顺序与成功边界

```mermaid
sequenceDiagram
    participant Subagent as continuation.Manager
    participant Registry as agent.Registry
    participant Loop as agentloop.Plugin
    participant Scope as agentTree
    participant Provisioner as subagent Provisioner
    participant Provisioning as optional Provisioning
    participant Member as agentMembership

    Subagent->>Registry: Create/Resume(options.Provisioner)
    Registry->>Loop: Factory.CreateAgent/ResumeAgent
    Loop->>Scope: mount base Agent tree
    Loop->>Provisioner: Provision(ctx, Scope)
    Provisioner->>Scope: Mount/Own exact effects
    Provisioner-->>Loop: optional Provisioning
    Loop->>Provisioning: Commit()
    Loop->>Scope: mount agentMembership
    Member->>Member: Enter, beginServing, Announce
    Loop-->>Registry: agent.Handle
    Registry-->>Subagent: exact Handle
```

Agent 创建成功边界是 membership publication 完成并返回 `agent.Handle`。Subagent `StartContinuable` 还有更晚的成功边界：初始 prompt 被 child Inbox 接受。两层事务不可合并；Inbox acceptance 前失败时，Subagent 仍须 Dispose 已返回的 Handle 并回滚 Activation ownership。

## 5. 失败与取消

- base tree、Provision、Provisioning Commit 或 membership publication 失败：`preparedAgent` 不返回 Handle，并卸载整棵树；
- 创建 context 取消：停止仍可取消的准备工作，但结构回滚使用非取消上下文；
- Extension registration 在 Provision/Commit 之间撤销：Subagent Provisioning 的 Commit 返回兼容码 `ACTIVATION_SETUP_REVOKED`；
- registration 在 child resident 后撤销：Extension Registry 释放该 registration 对应的 exact installations，不销毁 Activation；
- caller Handle Dispose：先 quiesce Agent、移除 membership，再释放 Scope effects 和基础 Plugin。

Agent 的 durable ID 已由 `Agent.ID()` 提供。`agent.Same` 只回答两个接口是否为同一个进程内实例，不创建 `InstanceID` 或第二套身份。

## 6. 实现证据

- contract：`agent/provisioning.go`、`agent/factory.go`、`subagent/extension.go`；
- Registry exact ownership：`agent/registry.go`；
- creation transaction：`agentloop/prepared_agent.go`；
- Scope/effect ownership：`agentloop/tree.go`；
- publication/teardown：`agentloop/membership.go`、`agentloop/lifecycle.go`；
- construction admission：`agentloop/construction.go`；
- owner-local tests：`agentloop/provisioning_test.go`、`subagent/internal/extension/registry_test.go`、`agentloop/shutdown_test.go`、`agent/registry_test.go`。

基础 seam 最初由提交 `b136298` 建立；提交 `dccd2c1` 将强制三方法 `Setup` 拆成 `Provisioner` 与可选 `Provisioning`，并接入 `ActivationExtension` 的有序安装、构建期撤销复核和 resident 精确撤销。这些证据不代表尚未实现的 spawn/fork Provider 或 Tool/control/report Consumer 已完成。
