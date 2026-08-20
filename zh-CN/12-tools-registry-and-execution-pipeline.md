# 12 Tools Registry 与执行流水线模块设计

状态：Accepted

本文拥有 `tools` 的 Tool Definition、Root Registry + Child Overlay、restriction/guard、执行流水线、结果物化和 System Prompt 投影边界。Plugin 的 Service Definition、父子可见性、typed Event、Waterfall 和生命周期由[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)拥有；模型 content block 和 `ToolSchema` 词汇由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有；代码近邻说明见[`tools/README.zh-CN.md`](../tools/README.zh-CN.md)；当前完成度和验证证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 设计目标与固定源

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

Tools 的目标是提供一套可被 Plugin 扩展、被 Agent Loop 调度、又不泄漏 Registry 内部状态的通用 Native Tool 运行时：

- 保留 DeepSeek Harness `packages/core/tools/src/index.ts` 的 Tool Definition、分层可见性、policy Waterfall、执行结果和事件语义；
- 用 Go 的静态类型、具名对象和方法表达职责，不复制 TypeScript closure、对象冻结或 Cordis context extension；
- 让具体 Tool Plugin 只提供业务定义和行为，不创建第二套 Registry、scheduler 或事件分发器；
- 让 Agent Loop 只编排 Turn、并发和 Session commit，不接管 Tool schema、policy 或结果规范化；
- 启动、卸载和失败回滚由 Plugin Runtime 驱动，不在 Tools 内创建第二套 effect/lifecycle 框架。

源职责映射如下：

| 固定源 owner / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `ToolRuntime`、`ToolLayer`、`ToolView` | `tools.Service`、`registry`、`toolStore` | 服务能力、layer lineage、shadow、restriction 和 schema view |
| `tools/pre-execute` | `PreExecuteRequest` / `PreExecuteOutcome` | allow、deny、ask 和 approval 前置策略 |
| `tools/execute` | `ExecuteRequest` / `ExecuteOutcome` | around-dispatch、context 包装和短路结果 |
| `tools/post-execute` | `PostExecuteRequest` / `PostExecuteOutcome` | accept、replace content/value 和 block |
| `tools/result`、`tools/change` | `ExecutionCompleted`、`RegistryChanged` | 最终结果通知与 Registry 变更通知 |
| `schema.ts` | `schema.go`、`jsonschema/v6` | 参数/output schema 编译和执行期校验 |
| System Prompt Tool provider seam | `systemprompt.ToolProvider` | 当前 layer 可见 schema 与 known-name 投影 |

## 2. 职责与非职责

`tools` 拥有：

- Tool name、description、参数/output schema、Executor、renderer、presentation metadata projector、finalizer、timeout metadata 和 concurrency classifier；
- Root layer、ancestor layer 与 exact overlay layer 的 Tool 可见性、shadow、restriction 和 guard；
- `tools/pre-execute`、`tools/execute`、`tools/post-execute` Waterfall 的业务输入、输出和 terminal；
- 调用取消融合、输入/output 校验、结果规范化、additional context 顺序和最终快照；
- `UNKNOWN_TOOL`、`INVALID_ARGS`、`INVALID_TOOL_OUTPUT`、`ABORTED` 与 `ABORTED_BEFORE_DISPATCH` 的稳定分类；
- 向 System Prompt 只投影 `name`、`description` 和 `parameters`。

`tools` 不拥有：

- Agent Turn、Step、批量并发、Session append、durable commit 或 interaction routing；
- filesystem、shell、数据库、用户问答等具体 Tool 业务；
- Approval 的策略、审计、UI 或 transport；
- LLM provider、stream、retry 或模型选择；
- 通用自愈、跨 Tool 重放和业务补偿；
- Web 卡片、生成 SDK、Code Runtime 或 Typert。

Tools 的恢复边界是把单次调用稳定物化为成功或失败结果，不自行重试。timeout、retry、metrics 等 around-dispatch 行为通过 `tools/execute` Waterfall 接入；跨调用恢复和重新调度由 Agent Loop 或更上层 use case 决定。

## 3. 包边界与包内职责

Tools 的包边界采用[04 Go 技术架构决策与技术选型](./04-go-technology-decisions.md)中的 D-15。当前实现由一个 `tools` 领域包、一个 `tools/factory` 入站构造适配器，以及领域外的具体 Tool Plugin 组成。

包内使用具名对象分离职责：

| 组件 | 职责 |
| --- | --- |
| `Service` | Plugin 生命周期、依赖解析、能力 facade、System Prompt provider 和 change publication |
| `registry` | 当前 layer 与 ancestor lineage 的组合、可见性查询和 mutation 规则 |
| `toolStore` | 锁保护的 exact-layer Tool、restriction、guard 及快照 |
| `executionRuntime` | `Prepare -> Dispatch -> Finalize/Finish` 一次性状态机 |
| `policyEngine` | pre-execute、Approval 解析和 monotonic guard |
| `dispatcher` | execute Waterfall、取消融合、Executor、schema 校验和成功结果规范化 |
| `resultProcessor` | post-execute、finalizer、最终快照和 `tools/result` 发布 |

Store 不调用 Plugin、Executor 或用户策略。Service 不实现执行阶段细节；`executionRuntime` 不拥有 Plugin 装卸或 System Prompt 注册。

## 4. Root Registry 与 Child Overlay

Root 与 Child Overlay 都是正常的 `tools.Service` Plugin 实例：

- root 由 `tools.New(validatedConfig)` 创建，拥有根 layer；
- overlay 由 `tools.NewOverlay()` 创建，挂载在需要隔离的父 Plugin 之下；
- overlay 在 `Apply` 中要求最近的 ancestor `ToolRuntime`，只读取其 layer snapshots，再追加自己的 exact layer；
- overlay 提供新的 `ToolRuntime`、`ToolCatalog` 和 `PolicyRegistry`，其子 Plugin 自动解析最近的能力，不需要业务对象持有 Plugin Context；
- overlay 卸载只清理自己的 layer；ancestor 和 sibling 不受影响。

可见视图按 root 到 exact overlay 的顺序计算：

1. ancestor Tool 先进入视图，近层同名 Tool shadow 远层 Tool；
2. 每层 restriction 只过滤此前继承的 Tool；
3. exact layer 自己的 Tool 在 restriction 后加入，因此不被本层 restriction 过滤；
4. `knownNames` 保留 lineage 中出现过的 Tool name，供 System Prompt 判断预限制能力；
5. schema 保持稳定注册顺序；被过滤项不占可见位置，同名 shadow 保留原位置。

Restriction 只能添加到 overlay。`Allow` 的非 nil 空 slice 表示有意隐藏全部 inherited Tool，不等于 omitted；restriction 不能引用 `run_code`、未知 inherited Tool 或只存在于 exact layer 的 Tool。Guard 可以存在于 root 或 overlay，并按 root 到 exact layer 的顺序单调拒绝。

## 5. 公共能力契约

`Service` 同时提供三个面向不同消费者的能力：

```go
type ToolRuntime interface {
    plugin.Service
    Get(string) (ToolDefinition, bool)
    Schemas() []llm.ToolSchema
    ExecutionMode(ToolExecutionInput) ToolExecutionMode
    Scheduler() ToolExecutionScheduler
    Execute(context.Context, ToolExecutionInput) ToolExecutionResult
}

type ToolCatalog interface {
    plugin.Service
    AddTool(context.Context, ToolDefinition) error
    RemoveTool(context.Context, string) error
}

type PolicyRegistry interface {
    plugin.Service
    AddRestriction(context.Context, string, ToolRestriction) error
    RemoveRestriction(context.Context, string) error
    AddGuard(context.Context, string, ToolGuard) error
    RemoveGuard(context.Context, string) error
}
```

`ToolRuntime` 是 Agent Loop 的读取与执行边界；`ToolCatalog` 是具体 Tool Plugin 的定义变更边界；`PolicyRegistry` 是 visibility/policy Plugin 的变更边界。消费者不依赖 `Service`、`registry` 或 `toolStore` 的具体实现。

`ToolExecutionScheduler` 由 `ToolRuntime.Scheduler()` 返回，实际 owner 是私有 `executionRuntime`。`Service` 不把 `Prepare`、`Dispatch`、`Finalize`、`Finish` 摊平成自己的方法，避免生命周期 facade 同时成为执行状态机。

Tool 行为使用有业务含义的 interface：`Executor`、`OutputRenderer`、`PresentationProjector`、`ContentFinalizer`、`ConcurrencyClassifier` 和 `ToolGuard`。对应 `Func` 类型只是无状态函数的可选适配器，不是主要对象模型。

`json.RawMessage` 只用于 Tool contract 本来允许任意 JSON shape 的参数、canonical value、schema 和 presentation metadata。Registry state、decision、execution state 和 result 不使用 `any`。

## 6. 具体 Tool Plugin 的实现方式

具体 Tool 不需要实现 `ToolRuntime`，也不需要显式调用 `plugin.Define`。它本身是一个普通 Plugin：

1. `Manifest` 要求 `ToolCatalog`，以及该 Tool 真正需要的其他能力；
2. `Apply` 构造完整 `ToolDefinition` 并调用 `AddTool`；
3. `Dispose` 使用同一 name 调用 `RemoveTool`；
4. 业务执行对象实现 `Executor`；只有需要自定义投影、finalizer、并发分类时才实现相应可选接口；
5. 需要 restriction 或 guard 时再要求 `PolicyRegistry`，不能把所有插件强制成 policy Plugin。

Plugin Runtime 保证依赖就绪后才调用 `Apply`，并在启动失败、替换或卸载时调用 `Dispose`。具体 Tool Plugin 必须把注册和撤销成对实现；Tools Service 不猜测哪个业务对象应该被自动注册。

## 7. 注册、撤销与 System Prompt

Tool 注册流程：

```text
validate active layer and definition
  -> lossless clone parameter/output schema
  -> compile and cache both schemas
  -> add exact-layer definition
  -> publish ordered tools/change
```

同一 exact layer 重名失败，近层同名允许 shadow ancestor；`run_code` 始终保留。新增 Tool 或 restriction 时，`tools/change` observer 失败会回滚刚才的新增。撤销以生命周期清理为优先：Tool/restriction 已删除后，即使通知失败也不恢复，错误返回给调用方；幂等重试不会留下停止插件的贡献。Guard 不改变 schema，因此增删 guard 不发布 `tools/change`。

Definition 在写入 Store 前复制 schema bytes 并保留 behavior interface；`Get`、`Schemas` 和 observer accessor 再次返回 detached data。调用方不能通过原 Definition 或返回值改写 Registry。

Tools Service 在 `Apply` 中把自己注册为 System Prompt 的 Tool provider，在 `Dispose` 中撤销。System Prompt 不缓存第二份 Catalog；每次 assembly 都读取当前 layer 的 live view。Tools 不调用 `RenderPrompt`，System Prompt 不读取 Executor、output schema 或 guard。

## 8. Typed config、schema 与 lossless JSON

root Factory 严格解码 owner-defined `Config`：未知字段、显式 `null`、错误类型和小于 `1` 的 `maxParallelSubCalls` 都失败。`mode` omitted 时默认 `native`；`code` 和 `both` 因 Code Runtime 未纳入而明确失败，不静默退化。

`maxParallelSubCalls` 是固定源 Code Mode sub-dispatch 的配置。Native-only 模式只验证并保留该字段，不把它误用成 Agent Loop 顶层 Tool 并发限制。

参数 schema 必须是声明 `type: "object"` 的 JSON object schema；output schema 可以描述任意 JSON value。两者注册时编译，执行时复用。

`internal/jsonvalue` 负责 lossless JSON validation/clone，拒绝 duplicate object key、negative zero、non-finite/无法稳定表示的 number、malformed JSON 和 trailing value。它回答“能否形成稳定 JSON snapshot”；JSON Schema 回答“snapshot 是否满足 Tool contract”，两者职责不同。

## 9. 执行状态机与核心流程

普通调用使用 `ToolRuntime.Execute`。Agent Loop 为了保持 policy 和 Session commit 的模型顺序，同时只重叠允许并发的 Tool body，使用 `ToolExecutionScheduler`：

```text
Prepare (ordered)
  -> snapshot visible definition and finalizer
  -> create immutable ToolExecution identity and arguments
  -> pre-execute Waterfall
  -> Approval and guards
  -> ScheduledDispatch | ScheduledPostResult | ScheduledFinalResult

Dispatch (only this stage may overlap)
  -> execute Waterfall
  -> fuse wrapper Context with caller cancellation
  -> validate arguments
  -> Executor
  -> validate canonical value
  -> render content and optional top-level presentation metadata

Finalize (ordered, result needs post)
  -> post-execute Waterfall
  -> cancellation convergence
  -> definition-owned finalizer
  -> materialize result and publish tools/result

Finish (ordered, terminal prepared result)
  -> definition-owned finalizer
  -> materialize result and publish tools/result
```

每个 prepared execution 是一次性状态机令牌：`Dispatch` 只能消费 `ScheduledDispatch` 一次，`Finalize` 只能消费 post-ready 状态一次，`Finish` 只能消费 final-ready 状态一次。错误顺序或重复调用属于 scheduler failure，不能重复执行 Tool body、finalizer 或结果事件。

`Prepare`、`Dispatch`、`Finalize` 的 `error` 表示 scheduler contract 无法继续；unknown Tool、schema、policy、Executor 和取消等预期失败仍是 `ToolExecutionResult`。`Finish` 没有 error 通道，是 total boundary，会把 nil、非法或错误阶段物化为失败结果。

## 10. Waterfall、结果与 Turn 信号

`pre-execute` 使用封闭的 `AllowDecision`、`DenyDecision`、`AskDecision`。Ask 通过可选 Approval capability 解析；缺少 Approval 或 Subject 时 fail closed。Guard 只可能拒绝，后续 guard 不能恢复允许。

`execute` 是 onion around-dispatch。Middleware 可以替换传给下游的 Context，或短路并返回公开 success/failure。来自 Tool terminal 的成功使用私有 read-only canonical wrapper 暴露给 Middleware；Middleware 返回的公开 success 必须重新经过当前 output schema 和 renderer，不能伪造 content、meta 或 turn marker。

`post-execute` 接收 detached `ToolResultSnapshot`，可以 accept、replace content、replace successful value 或 block。replace value 必须重新校验和 render；failed result 不能替换 value。

additional context 的顺序固定为：Tool body `DeferContext`、execute wrapper、post policy。Agent Loop 只能在对应 Tool result durable commit 后把这些消息加入下一 Step，维持 model call/result 邻接。

`ToolRunContext.ConcludeTurn()` 只在 Executor 正在执行时记录信号，失败结果永远不携带它。最终成功结果通过兼容字段 `ConcludesTurn` 保存信号，并通过 `ConcludesAgentTurn()` 读取。Tools 只产生信号；Agent Loop 必须先提交 Tool result 和 Session event，再结束当前 Turn。它不表示停止 Plugin、Runtime 或 Session。

## 11. Event、取消与失败语义

Tools 使用 Plugin Runtime 的统一 Event 分发：

| Event | Delivery | 可见性与失败语义 |
| --- | --- | --- |
| `RegistryChanged` / `tools/change` | `DeliveryOrdered` | 从发布 Service 所在 Plugin layer 向 ancestor observers 发布；新增失败回滚，撤销失败保留清理结果 |
| `ExecutionCompleted` / `tools/result` | `DeliveryBestEffort` | 从执行 Service 所在 layer 向 ancestor observers 并行发布；失败交给 Runtime reporter，不替换 Tool result |

pre/execute/post Waterfall 同样由发布 Service 的 Plugin layer 决定可见链，按 root 到当前 layer 组装 onion；sibling 和 descendant Plugin 不参与。Tools 不保存事件历史，也不实现 Event Sourcing。

取消结果由 body 是否开始决定：

- body 前取消为 `ABORTED_BEFORE_DISPATCH`；
- body 已开始、最终仍成功时等待 body 收敛，再变为 `ABORTED`；
- body 已形成结构化失败时保留业务失败；
- wrapper short-circuit success 在 caller 已取消时按 body 未开始处理。

Go 不能强杀同进程函数。Executor 必须观察 `ToolRunContext.Context`，并在返回前让自己拥有的 goroutine、I/O 或子任务达到 quiescence。Executor、renderer、projector、guard、Waterfall Middleware 和 finalizer 的 panic 都在所属边界转为失败，不能击穿 Runtime。

## 12. 排除项与 Consumer 边界

当前 included surface 是 Native Tools。以下不属于本模块：

- Code Mode 的 `run_code`、生成 TypeScript/Python SDK、Code Runtime 和 sub-dispatch log；
- Web `presentCall`/`presentResult` 和卡片模型；
- Approval policy/audit、UserQuestions 与交互 transport，由[17 Approval、UserQuestions 与 Interaction Gateway](./17-approval-user-questions-and-interaction-gateway.md)拥有；
- Agent Loop 的批量并发、additional context admission、Session event 和 durable commit，由[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)拥有；
- filesystem、shell 等具体 Tool Plugin。

Agent Loop 消费 `ExecutionMode` 和 `ToolExecutionScheduler`，但不直接读取 Registry state。Tools 提供单次调用的规范结果和 Turn 信号，但不创建第二套 driver、Inbox、Session log 或自愈调度器。
