# 11 System Prompt Registry 与 Assembly 模块设计

状态：Accepted

本文拥有 `systemprompt` 的贡献注册、scope overlay、assembly、工具 schema 排序、变量插值和渲染边界。通用 Child Scope、typed Event 与 effect 生命周期由[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)拥有；Tool definition、可见性与执行由[12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)拥有；Harness LLM 公共词汇由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有；实施状态与验证证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/system-prompt/src/index.ts` 的 `SystemPrompt` | `systemprompt.SystemPrompt`、`promptRegistry` | section、context、variable、tool provider 的注册入口与 change notification |
| 同文件的 `PromptLayer`、assembly 与 render 函数 | `promptStore`、`promptAssembler`、`RenderPrompt`、`RenderContextSections` | scope overlay、provider snapshot、确定性 assembly 与严格插值 |
| `packages/core/system-prompt/src/invariant.ts` | `validateAssembly` | waterfall 返回后的 owner invariant |
| `packages/core/scope/src/index.ts`、`store.ts` | `plugin.Scope`、`ScopeKey`、`ScopeLineage` 与 `promptStore` | opaque scope identity、祖先链、named shadow、anonymous contribution 和 effect ownership |
| `packages/core/llm/src/types.ts` 的 `ToolSchema`、`ContextSnapshotSection` | `llm.ToolSchema`、`llm.ContextSnapshotSection` | System Prompt 当前实际消费的 Harness LLM contract slice |

Go 不复制 Cordis context extension、WeakMap、declaration merging 或 TypeScript 可变对象惯例。相同职责分别由显式 Service interface、opaque Go key、泛型 Event key、内部 snapshot 和 detached value 实现。

## 2. 职责与非职责

`systemprompt` 拥有：

- canonical `systemPrompt` Service Definition；
- named `PromptSection`、`PromptContext`、prompt variable 与 anonymous tool provider；
- global、祖先和当前 Child Scope 的 overlay 规则；
- `system-prompt/change` 与 scope-filtered `system-prompt/assemble`；
- provider membership snapshot、求值顺序、section/context 排序与 assembly invariant；
- `Complete`、runtime-context suppression 与 tool order；
- strict single-pass `{{name}}` 插值和 runtime-context snapshot 渲染；
- owner-defined `Config` 的默认值、strict decode 和校验。

`systemprompt` 不拥有：

- Tool definition 的注册、权限、执行、结果或 policy waterfall；
- Agent turn、模型选择、LLM stream、Session append 或 context compaction；
- Jinja/JavaScript evaluator、配置脚本或通用模板平台；
- Web UI、Client prompt editor、SDK 或 Typert gateway；
- JSONL、SQLite、sqlc 或其他业务数据存储。

`ToolProvider` 只把当前 assembly 可见的 `llm.ToolSchema` 投影给模型。当前 `tools` owner 以同一个 scope view 提供 schema 和执行 lookup；但 restriction、guard、schema validation 与执行失败仍只由 Tools 决定，不能从 schema 出现在 prompt 中反推执行成功。

## 3. 内部职责划分

`systemprompt` 包内不是一个大 Registry 同时完成所有工作：

| 组件 | 单一职责 |
| --- | --- |
| `promptRegistry` | `SystemPrompt` 服务 facade；校验调用参数，把成功 mutation 绑定到调用方 `plugin.Scope`，发布 change event |
| `promptStore` | 锁保护的 global/scoped contribution state；精确增删、空 layer 回收与 provider membership snapshot |
| `promptAssembler` | 在锁外求值 snapshot，形成 assembly，执行 scoped waterfall 并恢复 owner invariant |
| `tool_order.go` | tool order 配置校验、schema detach、JSON object 检查和确定性排序 |
| `render.go` | 严格变量插值、prompt join 与 runtime-context snapshot join |
| `config.go` | JSON presence/null 语义、默认值和 owner validation |

共享锁只属于 `promptStore`。`promptRegistry` 不读取内部表，`promptAssembler` 不修改 Registry，provider callback 也绝不在 LiveStore lock 内执行。

## 4. 公共类型与 Service 边界

`SystemPrompt` 只暴露能力所需的方法：

```go
type SystemPrompt interface {
    Section(context.Context, *plugin.Scope, PromptSection) (plugin.Disposer, error)
    Context(context.Context, *plugin.Scope, PromptContext) (plugin.Disposer, error)
    SuppressRuntimeContext(context.Context, *plugin.Scope) (plugin.Disposer, error)
    Tools(context.Context, *plugin.Scope, ToolProvider) (plugin.Disposer, error)
    Variable(context.Context, *plugin.Scope, string, VariableProvider) (plugin.Disposer, error)
    Assemble(context.Context, AssembleContext) (PromptAssembly, error)
}
```

静态和动态文本统一通过 `TextProvider` 表达；`StaticText` 与 `TextFunc` 是显式 adapter。`VariableValue` 用 `Defined` 区分空字符串与 undefined，避免用空值猜测 presence。`AssembleContext.Scope` 只选择贡献和 listener 路由；请求取消与 deadline 继续由 `context.Context` 传播。

## 5. Scope overlay 与可见性

Root Plugin Scope 的 `ScopeKey` 是 global zero key。`Scope.Child(label)` 创建新的 opaque identity 并记录 parent link；Child Scope 继承所属 Plugin 已声明的 Service dependency，但不能提供 root Service。Child disposer 或 parent teardown 会按 effect 顺序释放其全部 contribution。

System Prompt 读取视图为：

```text
global layer
  -> farthest existing ancestor layer
  -> ...
  -> nearest selected scope layer
```

规则如下：

- section、context 和 variable 是 named contribution；同一 exact layer 重名失败，近层同名值 shadow 远层；
- tool provider 和 runtime-context suppressor 是 anonymous contribution；global 与全部祖先层累加；
- scope view 不读取 sibling 或 descendant layer；
- global view 只读取 global layer；
- scoped assembly waterfall 接受 global listener、selected scope listener 及其祖先 listener，拒绝 sibling 和 descendant listener；
- `system-prompt/change` 不过滤，因为 global mutation 可能影响所有 Agent view。

这套 overlay 复用 Plugin Scope identity 和 lineage，不建立 System Prompt 私有 scope registry；`promptStore` 只保存该能力自己的业务 contribution。

## 6. 注册与撤销事务

每个注册调用遵循同一生命周期：

```text
validate named input/provider
  -> mutate exact promptStore layer
  -> plugin.Own(caller Scope, undo)
  -> emit system-prompt/change
  -> return idempotent disposer
```

若 `plugin.Own` 失败，立即撤销 mutation。若首次 change listener 失败，调用 disposer 回滚 contribution，且不发布第二个伪 change。正常 disposer 先精确删除本次 registration、回收空 scoped layer，再发布 change。相同 callback 注册两次仍是两个 anonymous contribution，并拥有独立 disposer。

`PromptSection`/`PromptContext.Order` 必须是有限数；variable name 必须匹配 `^[a-z][a-z0-9_]*$`；nil provider 在修改 LiveStore 前失败。重复验证不下沉到每个内部层：外部形态由 facade 校验，LiveStore 只维护 uniqueness 和 ownership invariant。

## 7. Assembly 流程

```text
Agent/model-step caller
  -> SystemPrompt.Assemble(scope)
  -> promptStore.capture membership snapshot
  -> unlock LiveStore
  -> resolve variables global -> ancestors -> selected scope
  -> resolve effective ordered sections
  -> resolve contexts unless suppressed
  -> resolve tool providers and detach schemas
  -> apply deterministic tool order
  -> system-prompt/assemble scoped waterfall
  -> validate authoritative returned assembly
  -> restore Complete/suppression owner invariants
  -> return detached PromptAssembly
```

Snapshot 固定的是本次请求的 provider membership，不预先求值 provider。并发注册或撤销只影响后续 assembly；本次已经捕获的 callback 仍完整执行。callback 可在求值时注册新 contribution，不会死锁，也不会让当前请求半途改变成员集合。

variable provider 先按 global、远祖先到近 scope 求值，近层覆盖同名值。section/context 在 provider 求值前已完成 named shadow，因而被 shadow 的 provider 不执行。section 与 context 分别按 `Order` stable sort；相同 order 保留有效注册顺序。

## 8. Waterfall 与 owner invariant

`system-prompt/assemble` 是 expert waterfall：listener 可查看或变换 sections、contexts、tools 和 variables，也可不调用 `Next` 而短路下游。其返回值是 authoritative assembly，随后必须通过：

- section/context name 非空且分别唯一；
- tool name 非空；
- variable name 仍符合 canonical pattern。

两个规则在 waterfall 后由 System Prompt owner 强制恢复：

- 若有效 scope view 有一个 `Complete=true` section，waterfall 仍执行以解析 tools、contexts 和 variables，但最终 sections 恢复为该 section 的精确已解析值；多个有效 complete section 直接失败；
- 任一可见 layer 注册 runtime-context suppressor 时，context provider 不执行，waterfall 后 contexts 仍恢复为空。

因此 expert listener 不能绕过 complete prompt 或 suppression policy。其他变换保持其 authoritative 结果。

## 9. Tool schema 排序边界

每个 `ToolProviderResult` 返回当前可见 `Schemas`，并可返回 restriction 之前的 `KnownNames`。后者用于区分“配置引用了不存在的工具”和“工具已注册但本 scope 被隐藏”。省略时默认取 `Schemas` 的 name。

- 未配置 `toolOrder`：按 tool name 的稳定字典序输出；
- 已配置：必须且只能出现一次 `<unlisted-tools>`，显式名字按配置顺序，未列出 schema 在 marker 位置按 name 排序；
- 配置中的未知名字失败；已知但当前不可见的名字可以不产生 schema；
- provider 不能返回名为 `<unlisted-tools>` 的真实 tool；
- `Parameters` 必须是有效 JSON object，并在进入 assembly 时复制，调用方后续修改不能改变 snapshot。

System Prompt 不校验 Tool 输入，也不执行 Tool；这些属于 Tool Registry/Executor。

## 10. 渲染与模板边界

`RenderPrompt` 和 `RenderContextSections` 只识别完整的 `{{name}}`。未知、undefined、非法名字或带闭合符的 malformed group 失败；没有后续 `}}` 的孤立 `{{` 作为普通文本。替换值不会被再次扫描，所以不存在递归展开或表达式执行。

非空 prompt section 用一个空行连接。非空 context 保留 contribution name，`JoinContextSections` 加入 canonical superseding-snapshot 前缀，明确本次 snapshot 取代先前 runtime context。

[Gonja](https://github.com/nikolalohinski/gonja) 是完整 Jinja 模板引擎，带 control flow、filter、function 和可扩展环境；把它放进 core 会把源实现的严格占位替换扩大成另一种动态语言，并改变错误、escaping 和求值行为。因此 core 不引入 Gonja。未来若真实 Plugin 需要富模板，可在自己的 typed provider 中预渲染普通字符串，再把结果交给 `TextProvider`；它不能改变配置语义或 System Prompt 的 strict interpolation contract。

## 11. Typed config

`Config` 只有 `includeHarnessIdentity`、`includeRuntimeContext`、`persona` 和 `toolOrder`。strict decoder 拒绝 unknown field、错误类型和显式 `null`；omitted 与 explicit empty `toolOrder` 必须保留区别，因为空数组缺少 rest marker，应失败而不是退化为 omitted。

默认启用固定 `harness:identity` section 和 runtime context，persona 使用 canonical `deployment:persona`、order `0`。Factory 在构造 Plugin 前得到 `ValidatedConfig`；业务逻辑不接收 `map[string]any`、YAML node、`!!js` 或脚本结果。

## 12. 上下游交互

```text
System Prompt Factory
  -> strict Config -> ValidatedConfig
  -> New(promptStore + promptAssembler)
  -> Provide(systemPrompt)

Agent Provider (later)
  -> Require(systemPrompt)
  -> create effect-owned Child Scope
  -> preset registers scoped section/variable/listener
  -> each model step calls Assemble(scope)
  -> RenderPrompt + RenderContextSections
  -> pass prompt/context/tool schemas to Harness LLM contract

Tools Provider
  -> owns definitions/restrictions
  -> registers ToolProvider projection
  -> provides tools Service
  -> System Prompt never invokes executor
```

当前 shipped System Prompt Provider 可独立激活，不依赖 Session、API Proxy 或 Connection。Agent 进入后通过 Service dependency 消费它；composition root 不持有 `promptRegistry` 或越过 `SystemPrompt` interface。

## 13. 失败、取消与后续进入规则

- `context.Context` 取消由所有 provider 和 waterfall handler 接收；System Prompt 不另建 retained signal；
- 任一 provider、tool schema、waterfall 或 invariant 失败时，本次 assembly 整体失败，不返回部分 prompt；
- callback panic 保持编程错误语义，不在 Registry 中伪装成空贡献；需要统一 containment 时由 Plugin/Agent execution owner 明确增加边界；
- `PromptAssembly` 返回前复制 slice、map 和 JSON bytes，Consumer 不能修改 Registry 内部状态；
- future Agent Child Scope 直接消费现有 `Scope.Child` 和 `ScopeKey`，只有出现真实 Service namespace isolation 需求时才扩展 Service resolution；
- future LLM migration 扩充唯一 `llm` owner；不得为 System Prompt 再建一套 ToolSchema、Message 或 context DTO；
- 当前 Tool Registry 通过 `ToolProvider` 连接，不把执行/policy 逻辑塞进 `promptAssembler`；后续具体 Tool plugin 也只向 Tools owner 注册能力。
