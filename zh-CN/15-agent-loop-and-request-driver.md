# 15 Agent Loop 与请求驱动模块设计

状态：Accepted

本文拥有 `agentloop` Provider 的 concrete Agent 构造、生命周期事务、Turn/Step 状态机、请求重建、模型流消费、失败 attempt 重试边界、Tool-call 调度和动态 runtime context 投影。live Agent contract、Registry、Inbox 和 `agent/*` Event Definition 由[14 Agent Registry、Inbox 与实时事件模块设计](./14-agent-registry-inbox-and-events.md)拥有；Session facts/surface 由[10 Session Core 与生命周期模块设计](./10-session-core-and-lifecycle.md)拥有；System Prompt、Tools 与 LLM 的 Provider 职责分别见[11](./11-system-prompt-registry-and-assembly.md)、[12](./12-tools-registry-and-execution-pipeline.md)和[13](./13-harness-llm-runtime-and-deepseek-provider.md)；默认 policy 的执行与等待生命周期见[`llmretry` 模块说明](../llmretry/README.zh-CN.md)。当前实施状态、验证证据和剩余缺口只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源路径 / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/agent-loop/src/index.ts` 的 `AgentLoop` | `agentloop.Plugin`、`agentloop/factory.Factory`、`agent.Registry` 的 Factory seam | 配置、Agent Factory 与 lifecycle transaction；Go 不暴露无 Consumer 的第二个 Loop Service |
| `packages/core/agent-loop/src/agent.ts` 的 `ReactLoopAgent` | `agentloop.ReactLoopAgent`、私有 `loop`、`activityCoordinator`、`turnRunner` | 每 Agent 一个执行循环、live coordination、Turn/Step、cancel、maintenance 和 idle convergence |
| `ReactLoopAgent.buildRequest` | `agentloop/request.go` | request waterfall、exact Adapter resolution、header/context facts 和 stream assembly |
| `packages/core/agent-loop/src/tool-calls.ts` | `agentloop/tool_calls.go`、`tools.ToolExecutionScheduler` | bounded Tool scheduling、barrier、ordered result/context commit 和 abort drain |
| `packages/core/agent-loop/src/runtime-context.ts` | `agentloop/runtime_context.go` | 最新 retained runtime-context snapshot 的 durable projection |
| 源 Agent Loop Session event union | `session/agent_events.go` | Turn/Step/request/chunk/message/Tool fact 和 request/surface folds |

Go 不复制 Cordis `Service` 继承、Fiber、`AbortSignal`、`AsyncLocalStorage`、Schemastery 或 live Settings document。它以 Runtime 私有 Scope/Fiber、`context.Context`、typed config 和 capability interface 保留相同职责；这不是把 Agent Loop 改造成 Session、Tools 或 LLM 的第二个 owner。源 `AgentLoop` 的 `maxParallelToolCalls` 和 Factory 行为没有独立 Consumer，因此 Go 根 `Plugin` 直接实现 consumer-owned `agent.Factory`，不发布一个只能被实现自身读取的 `agentLoop` Service。兼容影响限于 Go 扩展 API；Session fact、Agent Event、Tool 顺序、取消和 wire 行为不变。

## 2. 职责与非职责

`agentloop` 拥有：

- `agent.Registry` 所拥有的 `agent.Factory` 实现，以及根 Plugin 的构造准入与停机排空；
- unpublished Agent/Session 和完整私有 Plugin Tree 的构造、publication 与 reverse teardown；
- 每个 `ReactLoopAgent` 独占的私有 loop，以及 activity、Turn、request、Tool-call 协作者；
- `idle`、`maintenance`、`running` activity coordination，以及 `WhenIdle` convergence；
- `Followup`、`Steer`、`Inject` 和 `Cancel` 对 Inbox/loop 的组合语义；
- Turn/Step boundary、Prompt assembly、`agent/pre-step`、`agent/request`、`agent/request-error` 与 `agent/turn-stopping` 的调用顺序；
- 只从 Session log 派生模型输入，并记录重建下次请求所需的完整 header/context facts；
- StreamChunk durable logging、Assistant message assembly、Tool-call scheduling 与最终 Turn reason；
- System Prompt runtime context 到 model-visible UserMessage 的增量投影。

`agentloop` 不拥有：

- Registry membership 的数据结构与规则、Inbox mutation contract 或 `agent/*` Event Definition；
- Session `seq`、surface replacement、JSONL/SQLite/sqlc、load、repair 或 persistence I/O；
- Prompt section/context/tool-schema registry、Tool policy/body/result semantics或 LLM Adapter route；
- retry policy 的延时、jitter 和次数决策；这些由[`llmretry`](../llmretry/README.zh-CN.md)负责，Agent Loop 只提供 failed attempt 的 retry seam 并执行明确返回的 retry action；
- Echo、RPC、Mux/Host frame、`session.*` API、client disconnect mapping 或 reconnect baseline；
- approval/question、permission、sandbox、Web UI、SDK、Typert 或 `!!js`。

## 3. Service、配置与依赖方向

根 `agentloop.Plugin` 只依赖 `agent.Registry`、`session.LiveStore`，并可选消费 `session/persistence.Persistence` 以恢复 durable Session；它提供零个 Service，直接把自身作为唯一 concrete `agent.Factory` 附加到 Registry。每个私有 Agent Tree 中的 `ReactLoopAgent` 再消费本 Scope 的 `session.LiveStore`、`llm.LlmRuntime`、`tools.ToolRuntime` 和 `systemprompt.Assembler`。这样根 Factory 不持有一次具体 Agent 的执行依赖，`agent.Registry` 也只依赖自己拥有的 `Factory` interface，不反向导入 `agentloop`。

```text
API / compiled Plugin
  -> agent.Registry.Create(CreateOptions)
  -> consumer-owned agent.Factory seam
  -> agentloop.Plugin
       -> prepare Session + construct complete private agentTree
       -> plugin.Runtime.MountScopedChild
            -> scoped sessions / llm / tools / systemPrompt
```

raw 配置只存在于 `agentloop/factory`，Factory 严格解码、校验并映射为不含 JSON 语义的 `agentloop.Settings`。配置只接受：

- `maxParallelToolCalls`：正整数，omitted 默认 `10`；
- `agents[]`：`id`、`sessionId`、`provider`、`model`、`maxTokens`、`cwd` 和 `resumeSessionId`。

unknown/重复 field、显式 `null`、错误类型、非安全 `maxTokens`、相对 `cwd`、同时设置 `sessionId`/`resumeSessionId` 或重复 exact Session identity 必须在 Plugin 构造前失败。相同配置 label 不等于相同 identity；未指定 exact identity 的相同 `id` 可以各自生成 `${id}-session-<uuid>`。

当前没有 Settings Service，因此并发上限是 Plugin 配置快照，只能通过受控 Plugin replacement 改变。未来纳入 Settings 时可增加 owner-defined live source，但不能让 Agent 或 Tool body 解析 raw config。请求 `resumeSessionId` 必须等待真实 Session persistence Provider，不能静默改成 fresh create。

配置 Agent 是显式的 post-start 事务：composition root 必须在 `plugin.Runtime.Start` 成功后调用 `StartConfiguredAgents`。Runtime 的静态启动事务不接受动态 mount，因此根 Plugin 的 `Apply` 只附加 Factory，不偷偷创建配置 Agent。`StartConfiguredAgents` 只能调用一次；一项失败会逆序销毁本批已经启动的 Agent，随后 composition root 必须停止 Runtime，不能在同一 Plugin 上重跑。

## 4. Agent 创建与 lifecycle transaction

一次 fresh create 使用同一个 `SessionID` 作为 Agent/Session identity。根 Plugin 先在 Runtime 外构造完整的 `agentTree` 声明，再一次性交给 Runtime：

```text
Session LiveStore Prepare（未发布）
  -> construct private agentTree
       -> System Prompt Overlay（Main）
       -> Tools Overlay（Main）
       -> provider/model/cwd variables（Main）
       -> ReactLoopAgent + private loop（Main）
       -> caller Extensions（Main）
       -> agentMembership（Commit）
  -> Runtime.MountScopedChild(agentTree)
  -> activate all Main children in declaration order
  -> agentMembership: Session Enter -> Agent Enter
  -> open exact Agent work admission
  -> Session Announce -> Agent Announce
  -> agent/session-start(startup)
  -> attach root Handle and return agent.Handle
```

Overlay 必须先于 `ReactLoopAgent` 激活，使 Agent 捕获本 Scope 的 Prompt/Tools runtime；扩展随后在同一 Scope 安装 scoped prompt、Tool、guard、Waterfall 或 Agent Event listener，并可依赖 exact `agent.Agent`。Main 阶段只装配依赖，Agent 的 `Followup`、`Steer`、`Inject` 与 maintenance 尚未准入；Commit membership 在 Session/Agent Enter 成功后才开放调用，再执行 Announce。任何 Main extension、Commit、collision、announcement 或 liveness 失败都由 Runtime 沿同一棵树逆序回滚，不发布半完成 Agent。

销毁由 Runtime 逆序执行。Commit membership 先拒绝新 work，以 `disposed` cause 取消 active activity、清空当时已存在的 Inbox并等待 loop/maintenance 收敛，再从 Agent Registry 移除、撤销 runtime-context route、释放 Session membership；之后才释放扩展、`ReactLoopAgent`、变量和 Overlay。Tool body 已经开始时必须先 drain；在取消后才完成并被 Tool result 接受的 additional context 仍按 durable 顺序进入 Inbox，但不能触发 disposal 后的新 Turn。调用方不需要显式调用“准入”或“排空”方法，这些都是根 Plugin、membership 和 Runtime 的内部职责。

Runtime tree Handle 和 returned `agent.Handle` 指向同一 lifecycle；重复 Dispose 必须幂等。根 Plugin 跟踪当前 lifecycle 集合以协调并发构造与全局停机，但不成为第二个 Agent owner。Agent Registry detach 先于 Session detach，且二者只删除 exact lifecycle entry，旧 disposer 不能删除后续同 ID instance。

## 5. Activity、发送与 idle convergence

每个 concrete Agent 拥有一个私有 `loop`；该对象组合唯一 `activityCoordinator` 与 `turnRunner`，不注册为 Runtime Service，也不形成第二个 Agent identity。每个 Agent 只允许一个 activity：

- `idle`：没有正在运行的 Turn 或 maintenance；
- `maintenance`：不运行 Turn，对外 Status 仍为 `idle`；
- `running`：一个私有 loop 独占 Turn/Step progression，对外 Status 为 `running`。

`Followup` 写入 `next-turn` 并唤醒；`Steer` 写入 `next-step` 并唤醒；`Inject` 只写入 `next-step`。私有 loop 在 step boundary claim 新 steering，不为每个 send 建 goroutine。唤醒落在 maintenance 或已经 aborted 的 activity 时会锁存到 convergence；`WhenIdle` 必须跟随这个 successor activity，不能在两次 logically connected activity 之间的瞬时 idle 提前返回。

`Cancel` 默认先清空 Inbox，再取消 active activity；`keepInbox=true` 只取消 activity并保留尚未 claim 的 work。一个 activity 的第一个 typed cause 是 durable authority，后续 user/parent/disposed/hook cancel 不能改写它。取消后到达的 waking input 进入 `next-turn`，避免加入已经 aborted 的 step。

## 6. Turn 与 Step 状态机

每个 Turn 从 durable `turn/start` 开始，并保证以一个 `turn/end` 收口：

```text
turn/start
  -> Inbox.Claim(next-turn 或 next-step)
  -> SystemPrompt.Assemble
  -> runtime-context projection
  -> agent/pre-step waterfall
       reject -> turn/end(blocked)
       enter empty initial batch -> turn/end(completed)，不开 Step
       enter messages
            -> step/start
            -> user/message surface append（逐条）
            -> model attempt / Tool calls
            -> step/end
  -> agent/turn-stopping（仅无 next-step 时）
  -> next-step continuation 或 turn/end
  -> session.LiveStore.Flush
  -> successor Turn 或 idle convergence
```

`step/start` 只在 pre-step admission 后追加；claimed/rewritten messages 只在 boundary 打开后进入 surface。`step/end` 由 finally-style boundary 保证，即使 request、stream 或 Tool execution 失败也不应留下无故开放的 Step。

`turn/end` 提交后，Turn runner 只通过 consumer 已持有的 `session.LiveStore.Flush` 发起正常 durability checkpoint，不直接调用 `Persistence`、SQLite 或 Backend。未配置 durability listener 时它是可观测的 no-op；配置 `SessionLogStore` 时，Turn runner 必须等待 write-behind retained batch 提交后才能成功进入 successor Turn 或 idle convergence。flush failure 进入 `agent/error` 并使当前 loop 失败，不能把尚未确认落盘的边界当作成功完成。

一个 completed/max-tokens step 后若 `next-step` 出现，仍在同一 Turn 继续。`max-tokens` 是该 Turn 的 sticky reason：后续 completed step 不能降级；下一 Turn 重新计算。Tool result 标记 `concludesTurn` 时停止其本来需要的模型 continuation，但已经到达的 steering 仍可要求另一个 Step。

## 7. 请求重建与模型 attempt

每次 attempt 都在 step boundary 重新调用 `Session.DeriveMessages()`，而不是持有可变 conversation 副本。请求配置按以下链路决议：

```text
Agent Options / persisted exact-model effort
  -> agent/request waterfall
  -> llm.PrepareCall(exact route + Adapter identity + defaults)
  -> canonical EpochHeader
  -> request/header(initial | resume | change)
  -> request/context（route/capacity 变化时）
  -> immutable GenerateOptions
```

第一次调用追加完整 `request/header`；已有 header 的新 Loop instance 使用 `resume` reason；后续只有 canonical header 真正改变时才追加 `change`。Adapter-owned default 在下一次 proposal 前移除，再由 exact Adapter 重新 materialize，避免 provider/model 切换时泄漏旧默认。`request/context` 只记录 provider、model 和可选 context window，未变化不重复追加。

任一已 dispatch 请求的离线重建边界是该 Step 首个 `assistant/chunk` 之前的 Session 日志前缀。把此前缀交给一个全新 Session 后：`DeriveMessages()` 只按 surface 节点与 replacement 生成 `messages`，`RequestHeaderValue()`/header fold 给出当时完整 `config`、`system` 和 `tools`；加上 Session identity 即得到同一份 `GenerateOptions`。`request/context` 是 route capacity 事实，不重复进入 `GenerateOptions`。因此 Tool continuation、header change 或 surface compaction 都必须由日志中的 append/header/replace 事件解释，不能依赖旧 Loop instance 的临时字段。

用已有日志 seed 一个新 Loop generation 时，构造阶段只恢复 Session fold、surface、Inbox 和最后 Turn 序号；该 generation 第一次真正请求时追加 `request/header(reason=resume)`，并从 seed 中的 replacement 后 surface 继续。这个 seed theorem 是 persistence-independent 的重建证明。真实 cold resume 由[19](./19-session-persistence-and-sqlite.md)的 `Persistence.Prepare` 先载入 Header/Event、提交必要 repair、创建唯一 unpublished Session，再交给 Agent Factory Enter/Announce；Loop 不导入 SQLite，也不自行读取物理存储。

模型流中的每个 chunk 先作为 `assistant/chunk` durable fact 追加，再交给 `BlockAssembler`。正常完成后追加一个带 chunk provenance 的 `assistant/message` surface event；空完成 anchor 仍记录但不进入派生消息。`max-tokens` 不执行被截断的 Tool call。

`finish:error` 或 `finish:aborted` 先进入 `agent/request-error` waterfall，payload 携带 structured failure 和 prepared Adapter 的 RetryPolicy snapshot。只有明确 retry action 才在同一 Step 重新发起 attempt；失败 attempt 的 chunk 保留为非 surface replay fact，不伪造 Assistant message。recovery 未选择 retry、recovery 自身失败或 cancellation 已生效时，Turn 以 structured error/aborted reason 结束。

## 8. Tool-call 调度

模型 Tool-call arguments 的空字符串映射为 `{}`，合法 JSON 保持原值，无效 JSON 作为 JSON string 交给 Tools owner，由 schema/policy 产生 model-visible failure。

调度只允许 Tool body/around-execute 阶段并发；pre-execute、post-execute、finalizer、Session event 和 additional context admission 始终按模型 call 顺序执行：

```text
classify first call
  -> exclusive: 单调用 barrier
  -> parallel: bounded rolling pool
       start 前重新分类尚未启动的 call
       Prepare（顺序）
       Dispatch/body（可重叠）
       settlement channel
       Finalize + tool/result commit（模型顺序）
       additional context（紧随各自对应 result、保持模型顺序）
```

parallel group 遇到重新分类为 exclusive 的 call 时先 drain 当前 pool，再让外层以新 barrier 处理。`maxParallelToolCalls` 限制每个 Agent Step 的 in-flight body 数，不限制不同 Agent。

取消停止补充新 dispatch，但必须 drain 已启动 body、按顺序提交其真实 result/context，并为尚未启动的模型 call 追加 `ABORTED_BEFORE_DISPATCH` synthetic call/result pair，使 replay 保持 Tool call/result 平衡。

普通 Tool body、policy、schema 或 unknown Tool failure 是 `ToolExecutionResult`，继续走 model-order finalize 和 `tool/result` commit。只有 `Prepare`、`Dispatch` 或 `Finalize` 本身无法返回规范结果时才是 Agent-level scheduler failure：首个 failure 停止补充新 dispatch，所有已经启动的 dispatch 必须先 settle，随后 Step/Turn 以 error 收口；尚未启动的 call 不追加，已经写入 `tool/call` 但结果未知的 call 也不伪造 `tool/result`。因此 failure drain 与 cancellation drain 使用同一个收敛骨架，但持久化结果规则不同。

## 9. Dynamic runtime context

System Prompt 的 contexts 不写入 system header，而是在每个 pre-step assembly 后形成一个 plugin-authored UserMessage。投影记录最后一个仍位于 Session surface 的 `@deepseek-ai/dsh-system-prompt` snapshot：

- 从未存在 snapshot 且当前 context 为空时不追加；
- 当前完整文本与 retained snapshot 相同则不重复；
- context 从非空变空时追加 canonical cleared marker；
- surface replacement 删除 retained snapshot 后，即使当前文本相同也重新 materialize；
- named sections 写入 message source 的 `form=snapshot` 与 `sections`，清空 marker 不伪造已不存在的 section。

投影只跟随 Session facts，不拥有 commit、replacement 或 compaction 决策。

## 10. 失败、取消与观测

- 非取消失败转为 `TurnError`；`LlmError` 保留 structured failure，其他错误使用稳定 `UNKNOWN` code；
- active cancellation 转为 `TurnAborted`，并把 user、parent、disposed、hook 或 legacy cause 写入 durable reason；
- `turn/end` 提交后通过 `session.LiveStore.Flush` 建立正常完成路径的 durability barrier；Persistence/SQLite 只作为 LiveStore listener 参与；
- `agent/error` 是 live observer，不代替 `turn/end`；observer failure 由 runtime reporter 包含，不能中断 boundary finalization；
- `agent/status` 只在 `idle <-> running` 变化时发布，maintenance 不产生伪 running；
- Assistant/Tool result 的 model-visible surface commit 与原始 chunk/call provenance 分离；
- Agent Loop 不吞掉 Session、Prompt、LLM 或 Tool owner 返回的 contract error，也不把技术失败改写成成功。

## 11. 上下游进入规则

```text
Connection / session.* API（`apiproxy/session.Gateway` inbound adapter）
  -> agents Registry / Agent live capability
  -> agentloop private loop
  -> Session facts
  -> Mux/Host projection（`apiproxy.LiveFrameSource` outbound projection）
```

- `session.*` API 只把 wire request 映射为 Registry/Agent capability 调用；不得让 `agentloop` 依赖 Echo、RPC 或 frame DTO；具体 method、projection 与 reconnect 规则由[16](./16-session-api-gateway-and-live-frames.md)拥有；
- Session persistence 进入后，由 Session owner 提供 prepare/load/repair；Agent Loop 增加真实 resume transaction，并发布 `agent/session-start(resume)`，不能在 Adapter 内决定业务修复；
- 默认 RetryPolicy 的 delay/jitter/attempt Consumer 由[`llmretry`](../llmretry/README.zh-CN.md)作为独立 Plugin 监听 `agent/request-error`，不能塞进 DeepSeek Adapter 或硬编码在 Agent Loop；
- compaction、approval/question、Guard、Subagent 或 Workflow 通过现有 Session/Agent/Tools Event seam 进入，不在 Loop 中增加 capability-specific branch；
- browser Connection、Web UI、SDK、Typert generator 和 `!!js` 始终不因 Agent Loop 实现而重新进入范围。
