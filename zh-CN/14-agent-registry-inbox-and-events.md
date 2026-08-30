# 14 Agent Registry、Inbox 与实时事件模块设计

状态：Accepted

本文拥有 `agent` 包的公开 Agent contract、live Registry、durable Inbox projection、Agent-scoped 实时事件、显式 initiator attribution 与单步 model selection snapshot。Inbox 消费由[Agent Message 协议重构设计方案](./AgentMessage协议重构设计方案.md)拥有的 `agentmessage.UserMessage`；具体 Turn/Step 驱动由[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)拥有；Session 事实日志见[10 Session Core 与生命周期模块设计](../session/docs/design.zh-CN.md)，Session API 与默认模型来源见[16 Session API Gateway 与实时 Frame 投影](./16-session-api-gateway-and-live-frames.md)，通用 Scope/Event 语义见[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)，当前实施证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

基线固定为 `47f943859bef60e4160492346772ded9b24f765a`。

| 源路径 / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/agent/src/runtime-types.ts` | `agent.Agent`、`Options`、`Status`、事件 payload/handler | live Agent capability、状态和扩展 contract |
| `packages/core/agent/src/index.ts` 的 `AgentRegistry` | `agent.RegistryService`、`agentRecord` | factory seam、live membership、创建/销毁公告、runtime parent-child ownership |
| `packages/core/agent/src/inbox.ts` | `agent.Inbox` | 两类 pending list、durable splice、replay 与通知 |
| `packages/core/agent/src/types.ts` | `InboxSplice`、`InboxSpliced` | `agent/inbox/spliced` Session event contract |
| `packages/core/agent/src/dispatch.ts` | `agent/events.go`、`agentloop.agentScope`、`pluginGateway` | exact Agent local dispatch 与全局 Event/Waterfall adapter |
| `packages/core/agent/src/model-selection.ts` | `ModelSelectionRef`、`ModelSelectionSetup` | prompt 与 request 使用同一步的 provider/model snapshot |
| `AgentRegistry` 的 `AsyncLocalStorage` initiator | `WithInitiator`、`WithoutInitiator`、`InitiatorFrom` | 同进程调用链的因果归因 |
| 源 `@deepseek-ai/dsh-agent` Plugin | `internal/assembly` 的 Agent Factory | 提供 canonical `agents` Service |

Go 不复制 Typert lookup/context、Cordis Proxy、declaration merging 或 `AsyncLocalStorage`。Typert 不属于客户端与 Agent 服务端 wire 兼容的必要路径；Agent Scope 是 Plugin-neutral ordinary object，initiator 由显式 `context.Context` 传播。具体单 Agent 构造和 Loop 驱动由源 `core/agent-loop` 对应的 `agentloop` Provider 拥有，不能并入 Registry。

## 2. 职责与非职责

`agent` 拥有：

- canonical `agents` Service Definition 与 consumer-owned `Factory` seam；
- live Agent 的 shared Agent/Session identity、注册顺序、root/child runtime ownership；
- Session ID 构造准入、private `agentRecord`、Host attach、有序 publication 与 paired disposal；
- `next-turn` / `next-step` Inbox projection、mutation、replay 和 live notification；
- `agent/*` 组件事件的 payload、mode、typed handler 和 exact Agent Scope 路由；
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

Agent Registry 的 canonical Plugin name 是 `agent`。默认 typed config 为空，因为 Registry 本身没有模型、重试、存储或 transport 配置；未知字段仍由 strict config ingress 拒绝。

```text
Agent Loop Plugin
  -> Provide(agent.Factory)

Agent Registry Plugin
  -> Require(agent.Factory)
  -> bind Factory to RegistryService
  -> Provide Registry / Constructor / ScopeSetup /
             RuntimeDescendants / EventDispatcher

API / driver Consumer
  -> Require(agent.Constructor)
  -> Create / Resume(options)
  -> Registry 委派给当前 factory
```

`Factory` interface 由其 Consumer `agent` 定义。Registry 不导入 `agentloop`，也不通过全局变量发现实现。Agent Registry Plugin 的 dependency graph 保证 Factory 先启动、后关闭；`Apply` 显式 bind，`Dispose` 先 unbind 再关闭 Registry。不存在运行期 Factory setter、per-Agent Plugin 或第二个 service locator。

`Factory.CreateAgent` 与 `ResumeAgent` 都只返回一个 unpublished `Host`。`Host` 暴露 exact Agent、Agent Scope、`EnterServing`、Session `Announce` 和幂等关闭；它不预留 Registry identity，不管理 descendants，也不保存 Registry。

## 4. Registry 生命周期与 identity

Agent ID 必须与其 Session ID 完全相同。Registry 在调用 Factory 前以 Session ID 建立 private `agentRecord`，因此同一 ID 的并发 create/resume 只有一个能进入构造；失败路径不需要让两个 unpublished Agent 竞速 publication。

```text
Registry reserves Session ID and optional RuntimeParent relation
  -> Factory constructs one unpublished Host
  -> Registry attaches Host.Agent and Host.Scope
  -> Scope.ApplySetup(exact Agent, Setup)
  -> Host.EnterServing
  -> Host.Announce Session
  -> Scope.Dispatch(agent/created)
  -> Scope.Dispatch(agent/session-start)
  -> record enters Live
  -> return Handle bound to exact agentRecord
```

`Setup` 在 Agent 对外可见前通过 `ScopeEditor` 安装 prompt、tool、policy、local Event/Waterfall 和普通资源。private `scopeDraft` 记录每个可撤销 Handle并在 commit 前执行 `ScopeCheck`；Apply 或 check 失败时逆序回滚。创建事件不能观察半配置 Agent。

`Handle` 直接保存 exact `agentRecord`，旧 Handle 不能按 Session ID 删除未来同 ID lifecycle。`agent/created` 一旦开始派发，任何 publication failure 或 close 都会尝试配对 `agent/disposed`；尚未开始 created 的构造失败不伪造 disposal。

Registry 的 `byID` map 以 Session ID 为 key、以当前 exact `agentRecord` 为 value；`parentByChild` 以 child Session ID 为 key、以 runtime parent Session ID 为 value；`childrenByParent` 以 parent Session ID 为 key、以 child Session ID 集合为 value。parent/child 不保存 record pointer，ownership graph 保持 DAG。`List` 保留 record 顺序，`Roots` 只返回没有 runtime parent 的 live Agent；runtime relation 不等同 `Header.parentSession` 的 durable lineage。

record phase 是 `Constructing → Attached → Live → Closing → Closed`。publication phase 单独记录 created 的 `Unpublished/Publishing/Published/Retired` 配对边界。关闭时先禁止新 descendants、等待已接纳构造，再按 Session ID relation child-first 收敛，最后关闭 Host 与 Scope；child failure 不跳过 sibling 或 parent。

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
  -> Session.Context.Commit(Batch(agent/inbox/spliced draft))
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

`AgentEvent` 是进程内组件通信事实，不是 `session.Event`，不写入 Session log。payload 始终携带 exact Agent subject。Agent Scope 先调用该 exact Agent 的 local observers，再通过 AgentLoop `pluginGateway` 发布给 Plugin Runtime 的全局 observer；不存在 per-Agent Plugin Scope、ancestor Plugin propagation 或 sibling/descendant Scope lookup。

Agent Registry Plugin 提供 `EventDispatcher` 给 Subagent 等外部 producer：dispatcher 根据 exact Agent identity 找到其 Scope，不让 producer持有 AgentLoop 或 Plugin。普通事件只在 record live 时接纳；record closing 后仅实现 `AgentClosingEvent` 的终止事实可继续派发，直到 Scope 真正关闭。`subagent.Ended` 使用该边界，保证 parent closing 期间仍有配对结束通知。

Waterfall 由 AgentLoop 全局 middleware 包裹 Agent-local middleware，再进入 RLA terminal action；Event 与 Waterfall 的方向不能混用。

Go handler 保留具体 function type，不经过 `any`、反射或统一 `Invoke`。源 payload 中的 `AbortSignal` 不复制为数据字段；Go cancellation 由 dispatch 的 `context.Context` 沿 waterfall/serial call chain 传播。

## 7. Initiator attribution

源 `AsyncLocalStorage` 只表达同进程因果归因，不证明 Agent live、owner 或 authorization。Go 使用显式 context：

- `WithInitiator(ctx, agent)` 建立一个 inherited driver call chain；
- `WithoutInitiator(ctx)` 遮蔽继承值，用于共享 timer、pool、watcher 或 exporter；
- `InitiatorFrom(ctx)` 用于允许 agentless 的日志、trace 或 metrics；
- `RequireInitiator(ctx)` 用于 contract 明确要求位于 Agent driver 下游的私有调用。

跨 goroutine 的 detached work 必须由启动它的 subsystem 显式拥有；跨 queue、process、persistence 或 wire 边界必须重新携带、验证并解析稳定 ID，不能序列化 Go context 或信任环境中的 Agent 指针。

## 8. Model selection snapshot

`ModelSelectionRef.Current()` 是下一步选择，`assembled` 是当前 prompt assembly 捕获的选择。`Current()` 先读取进程内显式选择，再读取 Session request/header 来源，最后读取 live default 来源；来源解析失败必须返回错误，不能用空选择继续。完整优先级和 `agentDefaultModel` owner 见[16](./16-session-api-gateway-and-live-frames.md#4-默认模型与-session-选择优先级)。

System Prompt waterfall 在进入 downstream 前读取 `Current()`，只有 downstream 成功后才发布为 `assembled`；因此 assembly 过程中并发切换只影响后续 step。

同一步的 `agent/request` waterfall 在 downstream config 上覆盖 captured provider/model/reasoning effort。captured effort 缺失时必须清除 inherited effort，让所选模型恢复 provider/default 行为，不能把另一个模型的 effort 泄漏到新选择。

该 helper 只协调两个已有 extension point，不拥有模型目录、provider route 注册或请求执行；这些职责仍属于 `llm` 和 Agent Loop。

## 9. 并发、失败与取消

- Registry 用独立锁保护 live map/order、Factory binding、ID relation 和每个 record 的 publication 状态；callback 不在全局 Registry lock 下执行。
- Inbox 用 operation lock 串行化 locate/normalize/append/apply 的完整 mutation，用 state lock 提供 detached list snapshot。
- created listener error vetoes普通 `Register` publication 并触发 paired rollback；disposed observer error 由 Registry reporter 包含，不能复活已删除 entry。
- Inbox durable payload 无效会使 replay 或 mutation 失败；业务层不能跳过损坏 Event 后继续运行。
- `CancelCause` 区分 user、parent、disposed 与 hook intent，`CancelOptions.KeepInbox` 决定 pending work 是否保留；真正的 active operation 和状态转换由 Agent Loop 拥有。
- `WhenIdle` 和 maintenance 不由 Registry 猜测；具体 Agent 必须确保 `RunMaintenance(ctx, operation)` 只在 live/accepting/true-idle 状态执行，并在 operation 返回后恢复 activity。这是 Agent 对一个同步 use case 的直接 admission，不定义宽泛 MaintenanceTask/MaintenanceRunner 抽象。

## 10. 上下游与后续能力进入

```text
API / driver
  -> agent.Constructor.Create / Resume / Agent.Send
  -> Agent Loop concrete Agent
       -> Inbox durable splice -> Session
       -> System Prompt assembly
       -> agent/pre-step + agent/request waterfalls
       -> LLM PrepareCall / stream / retry policy
       -> Tools execute
       -> Session surface events

observer / extension
  -> exact Agent Scope local observer
  -> AgentLoop gateway
  -> Plugin Runtime global observer
```

后续能力必须沿真实 Consumer 扩展：

- `agentloop` 实现 concrete Agent、Factory、Turn/Step、status/idle/cancel 与 retry attempt orchestration；Registry 不吸收这些状态机，具体流程见[15](./15-agent-loop-and-request-driver.md)；
- Session persistence 通过现有 `ResumeOptions`/`ResumeAgent` seam load/repair durable Session；
- `session.*` API 和 Mux/Host projection 只读取 Agent/Session contract，不让 wire frame 成为 Agent 领域类型；具体边界见[16](./16-session-api-gateway-and-live-frames.md)；
- Approval/Question 与 Subagent 沿 `ScopeSetup`、`EventDispatcher` 和 Agent Inbox 消费现有边界；Jobs、ACP、MCP 和 Headless CLI 继续保持 Deferred，直到范围和真实调用链明确；
- 新 Inbox adapter 不得出现；Inbox 是 Session facts 的业务 projection，JSONL/SQLite adapter 只存取 facts。
