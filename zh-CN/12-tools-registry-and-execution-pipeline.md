# 12 Tools Registry 与执行流水线模块设计

状态：Accepted

本文拥有 `tools` 的 Tool Definition、scope-aware Registry、restriction/guard、执行流水线、结果物化和 System Prompt 投影边界。通用 Plugin Scope、typed Event 和 effect 生命周期由[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)拥有；模型 content block 和 `ToolSchema` 词汇由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有；当前实施状态与验证证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/tools/src/index.ts` 的 `ToolRuntime` | `tools.ToolRuntime`、`toolRegistry` | Tool 注册、scope view、schema 投影、执行与结果通知 |
| 同文件的 `ToolLayer`、`ToolView`、`ToolRestriction` | `toolStore`、`toolView`、`compiledRestriction` | global/scoped contribution、shadow、继承过滤与 own-layer exemption |
| `tools/pre-execute`、`tools/execute`、`tools/post-execute` | `OnPreExecute`、`OnExecute`、`OnPostExecute` | pre policy、around dispatch 与 post policy waterfall |
| `tools/result`、`tools/change` | `OnResult`、`OnChange` | scope-filtered final result 与 unfiltered registry change |
| `packages/core/tools/src/schema.ts` | `schema.go`、`jsonschema/v6` | 参数/输出 schema 编译和运行时校验 |
| `packages/llm/llm/src/types.ts` 的 content block | `llm.ContentBlock` 及 core block types | Tool model-facing content 和 detached clone |
| `packages/attachment/attachment/src/types.ts` 的 `ImageAttachmentRef` | `attachment.ImageAttachmentRef` | image block 引用的 durable metadata owner |
| `packages/core/system-prompt/src/index.ts` 的 Tool provider seam | `systemprompt.ToolProvider` | 当前 scope 可见 schema 和 pre-restriction known-name 投影 |

Go 不复制 Cordis context extension、WeakMap、JavaScript `unknown` 对象、对象冻结或 union object 判别方式。它们分别映射为显式 interface、opaque token、`json.RawMessage` 协议值、detached snapshot 和封闭 Go interface。Web presentation 的 `presentCall`/`presentResult`、Tools Code Mode 生成 SDK 及 Code Runtime bridge 属于排除范围，不并入 Native Tool 核心。

## 2. 职责与非职责

`tools` 拥有：

- canonical `tools` Service Definition 和 typed Factory config；
- Tool name/description、输入/output schema、executor、renderer、presentation metadata、finalizer、timeout metadata 和 concurrency classifier；
- global、祖先和 selected scope 的可见性、shadow、restriction 与 guard；
- pre/execute/post waterfall、取消语义、输出校验、结果物化和 final notification；
- 只向 System Prompt 投影 `name`、`description`、`parameters`；
- `UNKNOWN_TOOL`、`INVALID_ARGS`、`INVALID_TOOL_OUTPUT`、`ABORTED` 与 `ABORTED_BEFORE_DISPATCH` 的稳定分类。

`tools` 不拥有：

- Agent turn/step 调度、Session append、interaction routing 或 approval UI；
- filesystem、shell、PTY、LSP、sandbox 等具体能力的业务实现；
- LLM provider、stream、retry 或模型选择；
- Web 卡片、浏览器 presentation、SDK 生成或 Typert；
- JSONL、SQLite、sqlc 或其他业务数据存储。

默认 composition 已提供 Approval；`AskDecision` 调用其 capability，并在 Service 或交互通道缺失、取消、失败时稳定退化为拒绝，不能把不可达解释成允许。Approval policy/audit 和 transport 闭环由[17](./17-approval-user-questions-and-interaction-gateway.md)拥有。Tool timeout 只作为定义 metadata 保留；具体 timeout policy 通过 `tools/execute` wrapper 进入，不硬编码进 Registry。

## 3. 包内职责划分

`tools` 不是一个 Registry 文件同时拥有全部状态和流程：

| 文件 / 组件 | 单一职责 |
| --- | --- |
| `registry.go` / `toolRegistry` | Service facade、注册调用校验、effect ownership、System Prompt provider 和 change publication |
| `store.go` / `toolStore` | 锁保护的 global/scoped Tool、restriction、guard 状态和 immutable membership view |
| `runtime.go` | pre/guard/dispatch/post/finalize/result 的执行流水线与 cancellation fusion |
| `schema.go` | Definition snapshot、JSON Schema 编译和输入/输出 validation |
| `result.go` | success/failure clone、stable error result 和 read-only `ToolResultSnapshot` |
| `events.go` | typed callback contract、Event key、scope dispatch adapter 和 callback panic containment |
| `restriction.go` | allow/deny 编译、reserved/unknown/duplicate name 校验 |
| `config.go` | typed config 的 omission/null/default/cross-field 语义 |
| `types.go` | owner 公共 Definition、execution、policy 和 Service contract |

共享锁只属于 `toolStore`。schema 编译发生在注册 mutation 之前；executor、renderer、policy、observer 和 finalizer 均不在 LiveStore lock 内运行。Registry facade 不解析工具业务参数，LiveStore 不调用 Plugin 或用户 callback。

## 4. 公共 contract 与动态 JSON 边界

插件行为使用具体 interface，而不是 `any` callback：

```go
type Executor interface {
    Execute(json.RawMessage, ToolRunContext) (json.RawMessage, error)
}

type OutputRenderer interface {
    Render(json.RawMessage, json.RawMessage) ([]llm.ContentBlock, error)
}

type ToolRuntime interface {
    Register(context.Context, *plugin.Scope, ToolDefinition) (plugin.Disposer, error)
    Restrict(context.Context, *plugin.Scope, ToolRestriction) (plugin.Disposer, error)
    Guard(*plugin.Scope, ToolGuard) (plugin.Disposer, error)
    Get(string, plugin.ScopeKey) (ToolDefinition, bool)
    Schemas(plugin.ScopeKey) []llm.ToolSchema
    ExecutionMode(ToolExecutionInput) ToolExecutionMode
    Scheduler() ToolExecutionScheduler
    Execute(context.Context, ToolExecutionInput) ToolExecutionResult
}
```

`json.RawMessage` 只表示 Tool contract 本身允许任意 JSON shape 的参数、canonical value、schema 和 presentation metadata。内部 contribution table、callback、decision 和 result 不用 `any`；success/failure、pre decision 和 post decision 都是封闭 interface。`ToolExecution.ArgumentsJSON` 与 `ToolResultSnapshot` 的 accessor 每次返回 detached copy，policy 或 observer 不能通过共享 byte slice 改写下游执行。

`ToolExecutionScheduler` 是 Tools 提供给 Agent Loop 的 staged capability，不是第二个 Tool executor。`Prepare`、`Dispatch`、`Finalize` 的 `error` 只表示无法形成规范结果的内部 scheduler failure；Tool body、policy、schema、取消和 unknown Tool 等预期失败仍返回封闭 `ToolExecutionResult`，由 Agent Loop 按模型顺序提交。`Finish` 是同步且 total 的最终物化边界。

`llm.ContentBlock` 是 merge-extensible behavior interface，当前 core variant 为 `text`、`reasoning`、`image`、`tool-call` 与 `tool-result`。`image` 中的 durable reference 仍由 `attachment` owner 定义；引入这个被实际消费的 metadata contract 不等于 Attachment upload/storage Service 已实现。

## 5. Typed config 与 presentation 边界

`Config` 保留源字段 `mode` 和 `maxParallelSubCalls`，strict decode 区分 omitted 与显式 `null`：

- omitted `mode` 默认 `native`；
- omitted `maxParallelSubCalls` 默认 `10`；
- 显式 `null`、未知字段、错误类型和小于 `1` 的 limit 失败；
- 当前 included surface 只接受 `native`；
- `code`/`both` 明确报告需要 Code Runtime bridge，不静默退化为 Native。

Code Mode 会生成模型调用用 SDK，并需要 `run_code` transport、Code Runtime 和 sub-dispatch log。项目已经排除 SDK，因此不复制该路径；`run_code` 名称仍无条件保留，防止未来重新纳入时与普通 Tool identity 冲突。Native mode 只把每个可见 Tool 的 schema 直接交给模型。

## 6. `sourceScope`、`selectedKey` 与 scope view

`sourceScope` 是创建长期 Tools Service 的 Plugin Scope，用来找到所属 `plugin.Runtime` 并发布事件；`selectedKey` 是一次查询、assembly、执行或通知所属的 Agent/Child Scope identity。两者不能互换。

```text
Tools Plugin sourceScope
├─ Agent A Child Scope -> selectedKey A
└─ Agent B Child Scope -> selectedKey B
```

`toolStore.view(selectedKey)` 按以下顺序计算：

1. 从 global 开始，依次合并 farthest ancestor 到 nearest ancestor，同名项由近层 shadow；
2. lineage 上全部 restriction 交集过滤 inherited entries；
3. selected scope 自己注册的 Tool 最后覆盖同名 inherited 项且不受这些 restriction 过滤；若该名字此前仍可见则保留其位置，否则追加到剩余 inherited 项之后；
4. `knownNames` 保留 restriction 前的能力名，`restrictableName` 只包含 inherited 名；
5. schema 顺序跟随固定 registration position：被过滤的 inherited 项不占位置，own shadow 保留仍可见同名项的位置，其他 own Tool 按注册顺序追加。

Restriction 只能注册到 Child Scope；global restriction 会遮蔽所有 Agent，因此直接失败。`allow` 的非 nil 空集合表示有意隐藏全部 inherited Tool，不等价于 omitted。restriction 不能引用 `run_code`、未知或 exact-own-only Tool。ancestor contribution 对更深 descendant 是 inherited，因此可以被 descendant restriction 约束。

## 7. 注册与撤销事务

Tool 注册执行：

```text
validate name/output/timeout
  -> lossless clone parameter/output schema
  -> compile and cache both schemas
  -> mutate exact toolStore layer
  -> plugin.Own(caller Scope, undo)
  -> EmitFrom(tools/change)
  -> return idempotent disposer
```

同一 exact layer 重名失败，近层同名允许作为 scoped shadow；`run_code` 始终禁止注册。若 `plugin.Own` 或首次 change observer 失败，mutation 回滚且不留下半注册 Tool。正常 disposer 精确删除本次 record、回收空 layer，再发布一次 change。Guard 是 execution policy，不改变模型可见 schema，因此注册/撤销不发 `tools/change`。

Definition 在进入 LiveStore 前复制 schema bytes 并保留 behavior interface；`Get` 和 `Schemas` 再次 detach 公开 JSON。调用方修改原 Definition 或返回 schema 都不能改变 Registry state。

## 8. Schema 与 lossless JSON

Tool parameter schema 必须是 lossless JSON object schema并声明 `type: "object"`；output schema 可以描述任意 JSON value。两者在注册时由 `jsonschema/v6` 编译并缓存，执行期不重复编译。

共享 `internal/jsonvalue` 只负责同进程边界的 lossless JSON validation/clone，拒绝：

- duplicate object key；
- negative zero、non-finite 或无法表示的 number；
- malformed JSON 和多个 trailing value。

这不是 schema validation 的重复实现：lossless validator 回答“能否形成稳定 JSON snapshot”，JSON Schema 回答“该 snapshot 是否满足 Tool contract”。该 helper 同时取代 Session 中原先重复的 lossless scanner，但不拥有 Session 或 Tool 业务规则。

## 9. 执行流水线

```text
ToolRuntime.Execute(input)
  -> snapshot visible definition/finalizer
  -> mint ToolExecution token + rootCallId
  -> lossless snapshot arguments
  -> caller pre-dispatch cancellation check
  -> scoped tools/pre-execute waterfall (default allow)
  -> approval fallback + monotonic guards
  -> scoped tools/execute waterfall
       -> fuse wrapper context with original caller cancellation
       -> input schema validation
       -> Executor
       -> output schema validation
       -> Native content render
       -> top-level presentation metadata projection
  -> normalize around-wrapper-authored result
  -> scoped tools/post-execute waterfall (default accept)
  -> definition-owned finalizer exactly once
  -> lossless result materialization
  -> EmitScopedFrom(tools/result)
  -> detached return result
```

`pre-execute` 的 `AllowDecision`、`DenyDecision`、`AskDecision` 是封闭决策；handler 不允许重写已经 materialize 的 call identity/arguments。Guard 只可能拒绝，按 global、远祖先到近 scope 顺序执行；后注册 policy 不能把既有拒绝变回允许。

`execute` 是 around-dispatch waterfall。wrapper 可以用新的 `context.Context` 调用 `Next` 实现 timeout、retry 或 metrics，但 terminal 会把原 caller cancellation 重新 fuse 进去，wrapper 不能用 `context.Background()` 脱离调用方取消。wrapper 可短路并返回 authored success/failure；authored success 必须通过当前 Tool output schema 重新校验和 renderer，不信任 wrapper 提供的 content/meta。

`post-execute` 接收 read-only snapshot，可 `Accept`、只替换 content、只替换成功 value 或 `Block`。替换 value 会重新执行 output validation/render；failed result 不能替换 value；`Block` 只产生失败和 feedback，不保留成功 value。

## 10. 取消、失败与 quiescence

取消结果由 body 是否已开始决定：

- body 前取消：`AbortError / ABORTED_BEFORE_DISPATCH`；
- body 已调用且最终成功时取消：等待 Executor 返回后改为 `AbortError / ABORTED`；
- body 已返回结构化失败时即使同时取消，也保留 Tool 自己的失败；
- wrapper short-circuit success 在 caller 已取消时按 body 未调用处理；
- 初始即取消绕过 policy；pre 后取消仍进入 post policy，与固定源顺序一致。

Go 无法硬杀同进程函数；Executor 必须观察 `ToolRunContext.Context` 并在返回前让 owned goroutine/I/O 达到 quiescence。Registry 不因 caller 取消提前遗弃正在运行的 body。wrapper context 和 caller context 的 fusion 同时处理已经取消与注册后取消的 race。

用户 callback panic 在它所属边界转为 failure：executor、renderer、projector、guard、policy handler 和 finalizer 均不能击穿 Runtime。result observer error/panic 被聚合并交给可选 `ResultObserverReporter`，不能改变已经 materialize 的 authoritative outcome。

## 11. Result、finalizer 与 turn marker

成功结果包含 canonical `Value`、model-facing `Content`、可选 top-level `Meta` 和 `ConcludesTurn`；失败结果包含 `ToolFailure`、content 和可选 meta，永远没有成功 value。nested execution 不生成 top-level presentation metadata。

`ToolRunContext.ConcludeTurn` 只对成功结果生效。definition-owned finalizer 在每次 normalized outcome 上恰好执行一次，包括 pre/execute/post failure；它只可选择替换 content，不能修改 value、failure、meta、call identity 或 turn marker。finalizer panic/非法 content 形成普通 failure，不二次调用 finalizer。

`ToolResultSnapshot` 只暴露 detached accessor。`tools/result` observer 看到的是最终物化快照；observer 失败被 contain，返回给 caller 的结果不与 observer 持有的 slice/bytes 共享。

## 12. Event 可见性

Tools 使用两种 emit：

| 事件 | 发布 API | 可见范围 | 原因 |
| --- | --- | --- | --- |
| `tools/change` | `plugin.EmitFrom` | 所有存活 listener | global mutation 可能改变任意 Agent 的下一次 assembly |
| `tools/result` | `plugin.EmitScopedFrom` | global + selectedKey ancestors + exact scope | 一个 Agent 的执行结果不能泄漏给 sibling Agent |

`EmitFrom` 和 `EmitScopedFrom` 都是同步 `emit`，差异只在 subscriber filter。`From` 表示从长期 Service 的 `sourceScope` 找 Runtime；`selectedKey` 只表示本次事件属于哪个 scope view。

pre/execute/post waterfall 也使用 selected scope：global、祖先和 exact listener 参与，sibling 与 descendant listener 排除。outer-to-inner 顺序仍由 Plugin Runtime 的全局 registration ordinal 决定。

## 13. System Prompt 与 assembly 交互

Tools Factory canonical name 为 `@deepseek-ai/dsh-tools`，Manifest `Requires(systemPrompt)`、`Provides(tools)`。启动流程为：

```text
System Prompt Provider -> Provide(systemPrompt)
Tools Provider waits/resolves systemPrompt
  -> New(toolStore + toolRegistry)
  -> systemPrompt.Tools(scope-owned provider)
  -> Provide(tools)

每次 prompt assembly(scope)
  -> Tool provider calls toolStore.view(scope)
  -> returns visible Schemas + pre-restriction KnownNames
  -> System Prompt owns configured toolOrder
```

Tools 不调用 `RenderPrompt`，System Prompt 不读取 executor/output schema/guard。schema 出现在 prompt 与实际 execution lookup 使用同一个 `toolStore.view`，避免“模型看到一个工具，但执行走另一套可见性规则”。

## 14. 排除项与后续 Consumer 进入

当前 Native Tools 能力已经完整提供 Definition、Registry、policy waterfall、执行、结果和 System Prompt 投影。以下职责不属于本模块当前 included surface：

- Code Mode 的 `run_code`、生成 TypeScript/Python SDK、Code Runtime 和 sub-dispatch log；
- Web `presentCall`/`presentResult` 和卡片模型；
- Approval policy/audit、UserQuestions 与交互 transport（由[17](./17-approval-user-questions-and-interaction-gateway.md)拥有）；
- Agent Loop 的批量并发编排、additional context 入队与 Session event commit；
- filesystem/shell 等具体 Tool plugin。

Agent Loop 作为 Consumer 使用 `ExecutionMode` 和 `ToolExecutionScheduler`：Tools owner 顺序执行 `Prepare`、`Finalize`、`Finish`，只有 `Dispatch`/body 可由 Agent Loop 重叠；结果中的 `ConcludesTurn` 和 `AdditionalContextMessages` 由 Agent Loop 在 durable `tool/result` 之后消费。批量算法和 Session event 顺序由[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)拥有，Tools 不建立第二套 driver 或 Inbox。
