# 14 Agent Registry、Inbox 与实时事件模块设计

状态：Accepted

本文拥有 `agent` 包的公开 Agent contract、live Registry、durable Inbox projection、Agent-scoped 实时事件、显式 initiator attribution 与单步 model selection snapshot。具体 Turn/Step 驱动由[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)拥有；Session 事实日志见[10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)，通用 Scope/Event 语义见[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)，当前实施证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

基线固定为 `47f943859bef60e4160492346772ded9b24f765a`。

| 源路径 / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/agent/src/runtime-types.ts` | `agent.Agent`、`Options`、`Status`、事件 payload/handler | live Agent capability、状态和扩展 contract |
| `packages/core/agent/src/index.ts` 的 `AgentRegistry` | `agent.Registry`、`registryService` | factory seam、live membership、创建/销毁公告、runtime ownership |
| `packages/core/agent/src/inbox.ts` | `agent.Inbox` | 两类 pending list、durable splice、replay 与通知 |
| `packages/core/agent/src/types.ts` | `InboxSplice`、`InboxSpliced` | `agent/inbox/spliced` Session event contract |
| `packages/core/agent/src/dispatch.ts` | `agent/events.go` | Agent subject 与 Child Scope 融合后的 scoped dispatch |
| `packages/core/agent/src/model-selection.ts` | `ModelSelectionRef`、`InstallModelSelection` | prompt 与 request 使用同一步的 provider/model snapshot |
| `AgentRegistry` 的 `AsyncLocalStorage` initiator | `WithInitiator`、`WithoutInitiator`、`InitiatorFrom` | 同进程调用链的因果归因 |
| 源 `@deepseek-ai/dsh-agent` Plugin | `internal/assembly` 的 Agent Factory | 提供 canonical `agents` Service |

Go 不复制 Typert lookup/context、Cordis Proxy、declaration merging 或 `AsyncLocalStorage`。Typert 不属于客户端与 Agent 服务端 wire 兼容的必要路径；Scope 由 opaque `plugin.ScopeKey` 表达，initiator 由显式 `context.Context` 传播。具体 Agent 构造和 Loop 驱动由源 `core/agent-loop` 对应的 `agentloop` Provider 拥有，不能并入 Registry。

## 2. 职责与非职责

`agent` 拥有：

- canonical `agents` Service Definition 与 consumer-owned `Factory` seam；
- live Agent 的 shared Agent/Session identity、注册顺序、root/child runtime ownership；
- unpublished `Enter`、publication `Announce` 与 paired disposal；
- `next-turn` / `next-step` Inbox projection、mutation、replay 和 live notification；
- `agent/*` 事件的 payload、mode、typed handler 和 Agent Scope filtering；
- process-local initiator attribution；
- prompt assembly 与 LLM request 之间的 model selection snapshot。

`agent` 不拥有：

- Turn/Step 状态机、LLM stream consumption、Tool-call scheduling、retry attempt loop 或最终 Assistant message；
- Session `seq` 分配、Event 存储、JSONL/SQLite/sqlc、repair 或 reconnect projection；
- Echo、RPC、Mux/Host frame、客户端 cancel 或 API method；
- DeepSeek HTTP/SSE、Tool 副作用、approval/question 决策或权限策略；
- Typert Host Gateway、浏览器 Connection、Web UI、SDK 或 `!!js`。

`Agent` interface 是 Registry、Agent Loop、API adapter 和扩展之间的 live capability，不是把这些职责实现进一个对象。方法中的 `Send`、`Followup`、`Steer`、`Inject`、`Cancel`、`WhenIdle` 和 `RunMaintenance` 由具体 Agent Loop instance 实现；Registry 只记录和返回该 capability。

## 3. Service 与 Factory 边界

`agent.Service` 使用 canonical key `agents`。默认 Agent Plugin 的 typed config 为空，因为 registry 本身没有模型、重试、存储或 transport 配置；未知字段仍由 strict config ingress 拒绝。

```text
Agent Plugin
  -> NewRegistry(providerScope)
  -> Provide(agents)

Agent Loop Plugin
  -> Require(agents)
  -> agents.SetFactory(loopScope, concreteFactory)
  -> disposer 精确撤回 factory

API / driver Consumer
  -> Require(agents)
  -> agents.Create(ownerScope, CreateOptions)
  -> Registry 委派给当前 factory
```

`Factory` interface 由其 Consumer `Registry` 定义。Registry 不导入 `agentloop`，也不通过全局变量发现实现。没有 factory 时 `Create` 明确失败；重复注册 factory 明确失败；owner Scope unload 时 factory slot 由 effect disposer 撤回。

当前 Go boundary 开放新建所需的 `CreateAgent` seam。源 `resume` 要等待 Session persistence 的真实 load/repair call chain 进入，届时扩展现有 consumer-owned interface，不预建返回假数据的入口。

## 4. Registry 生命周期与 identity

Agent ID 必须与其 Session ID 完全相同。Registry 的 authoritative collision boundary 是 `Enter`：同一 ID 的并发 create/resume 可以先在 unpublished Scope 中准备，但只有一个 exact entry 能进入 live map。

```text
Factory prepares Session + Agent Child Scope
  -> optional Setup.Apply(unpublished scope)
  -> Registry.Enter(agent, owner)
       -> validate Agent / Session / Child Scope
       -> reject same-id collision
       -> return exact detach capability
  -> SetupCommit.Commit（若存在）
  -> Registry.Announce(agent)
       -> mark announcement begun
       -> scoped agent/created
  -> start Agent Loop
```

`Enter` 与 `Announce` 分离是 publication transaction 的一部分：setup 可以在 Agent 对外可见前安装 scoped prompt、tool、model selection 或 policy contribution；创建事件不能观察半成品。普通 `Register` 组合 `Enter`、Scope-owned disposer 与 `Announce`，announcement 失败时回滚 live entry。

detach 捕获 exact entry identity，旧 disposer 不能删除未来同 ID lifecycle。若 created listener 在同步 dispatch 内请求 detach，Registry 延迟到全部 created listener 返回后再移除，确保同一次公告的 listener 都看到相同 live entry；只要 created publication 已开始，后续移除就发布 paired `agent/disposed`。未 announce 的 entry 回滚时不伪造 disposal。

`List` 保留 registration order，`Roots` 只返回 runtime owner 为空的 Agent。`IsOwnedBy` 表示创建时的 live runtime ownership，不等同 `Header.parentSession` 的 durable lineage；Go 用 owner Agent 的 opaque Scope identity 保留 exact runtime boundary。

## 5. Durable Inbox

Inbox 拥有两个有序列表：

- `next-turn`：每次新 Turn 最多消费一条 queued user input；
- `next-step`：下一次 model step 前整体消费的 steer/injected context。

`Claim(next-turn, turn)` 先清空全部 `next-step`，再取一条 `next-turn`，并按此顺序发布 claimed notification。`Claim(next-step, turn)` 只清空 `next-step`。`Clear` 先取消 `next-step`，再取消 `next-turn`，确保同一步追加内容先收到 discarded。

所有 mutation 归一化为 Session event：

```json
{
  "target": "next-turn",
  "start": 0,
  "removedCount": 1,
  "inserted": [],
  "outcome": "canceled"
}
```

`removedCount` 为零时缺失；`outcome=canceled` 只表示被 replace/remove/clear 等取消，不用于 Loop 正常 claim。`inserted` 始终是数组。`start` 与 `deleteCount` 使用 JavaScript splice 对应的整数边界归一化：负 start 从尾部计算，超出范围截断，负 delete count 视为零。零删除且零插入是 no-op，不追加 Event。

提交顺序不可交换：

```text
validate normalized mutation against current projection
  -> Session.Append(agent/inbox/spliced)
  -> decode committed snapshot
  -> mutate live projection
  -> discarded notifications
  -> inserted notifications
```

因此 Session observer 先看到 durable fact，且看到的是 mutation 前的 Inbox；append 失败时 live list 和 notification 都不改变。两个列表共享 Message ID 唯一约束，replace 也不能把一个 identity 复制到另一个 list。

构造 Inbox 时只 replay `Header.seedLength` 之后的 `agent/inbox/spliced`。这保留源语义：seed 表示已经折叠进基线的历史，pending work 只由 seed boundary 之后的 splice 重建。未知字段、缺失 required 字段、错误 message shape、越界坐标、未知 target/outcome 或重复 ID 都使恢复失败，不能静默修复业务事实。

## 6. Agent-scoped 实时事件

| Event | Mode | Owner 语义 |
| --- | --- | --- |
| `agent/created`、`agent/disposed` | emit | live publication 与 paired teardown |
| `agent/status` | emit | `idle` / `running` 非重复状态转换 |
| `agent/inbox/inserted`、`claimed`、`discarded` | emit | durable Inbox mutation 后的 live projection 通知 |
| `agent/session-start` | emit | `startup`、`resume`、`clear`、`compact` 驱动入口 |
| `agent/pre-step` | waterfall | reject 或给出进入一步的 UserMessage batch |
| `agent/request` | waterfall | 构造该步 immutable `llm.CallConfig` |
| `agent/request-error` | waterfall | 一个 listener 决定失败 attempt 是否 retry |
| `agent/turn-stopping` | serial | 已提交 listener 按序完成 turn 停止边界 |
| `agent/error` | emit | 已包含的 live failure 通知 |

事件 payload 始终携带 exact Agent subject。发布方从长期 Service 的 `sourceScope` 找到 Runtime，再把 subject 的 Child Scope key 作为 selected target；global、祖先和 exact listener 可见，sibling 与 descendant 不可见。

这里的 `selectedKey` 意为“本次事件或 view 所属的目标 Scope key”，不是 UI selection。它只负责 listener/view admission；`sourceScope` 才负责定位 Runtime。两者不能互换，也不能因此创建每 Agent 一套 Service Registry。

Go handler 保留具体 function type，不经过 `any`、反射或统一 `Invoke`。源 payload 中的 `AbortSignal` 不复制为数据字段；Go cancellation 由 dispatch 的 `context.Context` 沿 waterfall/serial call chain 传播。

## 7. Initiator attribution

源 `AsyncLocalStorage` 只表达同进程因果归因，不证明 Agent live、owner 或 authorization。Go 使用显式 context：

- `WithInitiator(ctx, agent)` 建立一个 inherited driver call chain；
- `WithoutInitiator(ctx)` 遮蔽继承值，用于共享 timer、pool、watcher 或 exporter；
- `InitiatorFrom(ctx)` 用于允许 agentless 的日志、trace 或 metrics；
- `RequireInitiator(ctx)` 用于 contract 明确要求位于 Agent driver 下游的私有调用。

跨 goroutine 的 detached work 必须由启动它的 subsystem 显式拥有；跨 queue、process、persistence 或 wire 边界必须重新携带、验证并解析稳定 ID，不能序列化 Go context 或信任环境中的 Agent 指针。

## 8. Model selection snapshot

`ModelSelectionRef.current` 是下一步选择，`assembled` 是当前 prompt assembly 捕获的选择。System Prompt waterfall 在进入 downstream 前读取 `current`，只有 downstream 成功后才发布为 `assembled`；因此 assembly 过程中并发切换只影响后续 step。

同一步的 `agent/request` waterfall 在 downstream config 上覆盖 captured provider/model/reasoning effort。captured effort 缺失时必须清除 inherited effort，让所选模型恢复 provider/default 行为，不能把另一个模型的 effort 泄漏到新选择。

该 helper 只协调两个已有 extension point，不拥有模型目录、provider route 注册或请求执行；这些职责仍属于 `llm` 和 Agent Loop。

## 9. 并发、失败与取消

- Registry 用独立锁保护 live map/order、factory slot 和每个 entry 的 publication 状态；callback 不在全局 Registry lock 下执行。
- Inbox 用 operation lock 串行化 locate/normalize/append/apply 的完整 mutation，用 state lock 提供 detached list snapshot。
- created listener error vetoes普通 `Register` publication 并触发 paired rollback；disposed observer error 由 Registry reporter 包含，不能复活已删除 entry。
- Inbox durable payload 无效会使 replay 或 mutation 失败；业务层不能跳过损坏 Event 后继续运行。
- `CancelCause` 区分 user、parent、disposed 与 hook intent，`CancelOptions.KeepInbox` 决定 pending work 是否保留；真正的 active operation 和状态转换由 Agent Loop 拥有。
- `WhenIdle` 和 maintenance 不由 Registry 猜测；具体 Agent 必须确保 maintenance 只在 true idle 执行。

## 10. 上下游与后续能力进入

```text
API / driver
  -> agents.Create / Agent.Send
  -> Agent Loop concrete Agent
       -> Inbox durable splice -> Session
       -> System Prompt assembly
       -> agent/pre-step + agent/request waterfalls
       -> LLM PrepareCall / stream / retry policy
       -> Tools execute
       -> Session surface events

observer / extension
  -> Agent Child Scope listener
  -> global + ancestors + exact subject only
```

后续能力必须沿真实 Consumer 扩展：

- `agentloop` 实现 concrete Agent、Factory、Turn/Step、status/idle/cancel 与 retry attempt orchestration；Registry 不吸收这些状态机，具体流程见[15](./15-agent-loop-and-request-driver.md)；
- Session persistence 进入后再增加 `ResumeOptions`/resume seam，并由 Session owner load/repair；
- `session.*` API 和 Mux/Host projection 只读取 Agent/Session contract，不让 wire frame 成为 Agent 领域类型；
- approval/question、Jobs/Subagent、ACP、MCP 和 Headless CLI 继续保持 Deferred，直到范围和真实调用链明确；
- 新 Inbox adapter 不得出现；Inbox 是 Session facts 的业务 projection，JSONL/SQLite adapter 只存取 facts。
