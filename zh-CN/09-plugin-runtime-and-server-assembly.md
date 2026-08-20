# 09 Plugin Runtime 与 Server Assembly 模块设计与实现

状态：Accepted

本文拥有 `plugin` 与 `internal/assembly` 的职责、Go 类型模型、上下游流程和生命周期。全局依赖方向与 Child Scope 使用规则由[02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)拥有；System Prompt、Tools、Agent Loop、Session API Gateway、Interaction Gateway、Session Projection/Title、Session Persistence 与 Workspace 的消费语义分别由[11](./11-system-prompt-registry-and-assembly.md)、[12](./12-tools-registry-and-execution-pipeline.md)、[15](./15-agent-loop-and-request-driver.md)、[16](./16-session-api-gateway-and-live-frames.md)、[17](./17-approval-user-questions-and-interaction-gateway.md)、[18](./18-session-projection-and-title.md)、[19](./19-session-persistence-and-sqlite.md)和[20](./20-workspace-registry-and-api.md)拥有；当前实施证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 源职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留的职责 |
| --- | --- | --- |
| `vendor/cordis/src/registry.ts` 的 `Plugin`、`RegistryService` | `plugin.Plugin`、`plugin/factory.Configurator`、`Factory`、`Catalog` | 静态 Factory 注册、Plugin 创建和实例跟踪 |
| `vendor/cordis/src/fiber.ts` 的 `Fiber`、`effect`、`FiberState` | `plugin.Runtime`、`FiberStatus`、Runtime 私有 Effect | dependency settlement、effect ownership、rollback、unload |
| `vendor/cordis/src/reflect.ts` 的 `provide`、`notify` | `ServiceDefinition[S]` 的 `Provide`、`Require` | Service Definition、唯一 Provider、Consumer 重启 |
| `vendor/cordis/src/events.ts` | `EventDefinition[E]`、`WaterfallDefinition[I,O]` | Observer ownership、fact delivery 与 middleware control |
| `vendor/cordis/src/context.ts` | `plugin.Context`、`Scope` | Plugin instance 的 Runtime interaction 与可见性 |
| `packages/core/scope/src/index.ts`、`store.ts` | `Context.ChildScope`、`LoadChild`、`ScopeKey`、`ScopeLineage` | opaque child identity、祖先链、child lifecycle 与 scoped event admission |
| `packages/host/apiproxy` | `internal/assembly` 的 API Proxy Plugin | 提供 `apiProxy` Service |
| `packages/client/connection` 的 Host half | `internal/assembly` 的 Connection Plugin | 消费 `apiProxy`，挂载 HTTP/WebSocket carrier |
| `packages/core/system-prompt` | `internal/assembly` 的 System Prompt Plugin | 提供 `systemPrompt` Service |
| `packages/core/tools` | `internal/assembly` 的 Tools Plugin | 消费 `systemPrompt`，提供 `tools` Service 并注册 schema projection |
| `packages/core/agent` | `internal/assembly` 的 Agent Plugin | 提供 `agents` Registry Service；具体 Factory 由 Agent Loop 注册 |
| `packages/core/agent-default-model` | `internal/assembly` 的 Agent Default Model Plugin | 提供 `agentDefaultModel` Service；当前静态 Provider 保留 Settings 缺失时的 source fallback |
| `packages/core/agent-loop` | `internal/assembly` 的 Agent Loop Plugin | 消费 Agent/Session/LLM/Tools/System Prompt，提供 `agentLoop` 并注册 concrete Factory |
| `packages/core/llm-retry` | `internal/assembly` 的 LLM Retry Plugin | 消费 Agent request-error seam，安装默认 provider-routed retry Consumer |
| `packages/session/session-projection` | `internal/assembly` 的 Session Projection Plugin | 提供 `sessionProjections` Registry |
| `packages/session/session-title` | `internal/assembly` 的 Session Title Plugin | 消费 Session/Projection，提供 log-backed `sessionTitle` |
| `packages/session/session-persistence` 与 `session-persistence-sqlite` | `internal/assembly` 的 Session Persistence Plugin | 消费 Session LiveStore，内部装配 SQLite Backend，只提供 `sessionPersistence` |
| `packages/workspace/workspace` 与源 Storage Domain | `internal/assembly` 的 Workspace Plugin | 消费 Session LiveStore/Persistence，内部装配 SQLite Backend，只提供 `workspaceRegistry` |
| `packages/interaction/user-approval` | `internal/assembly` 的 Approval Plugin | 消费 System Prompt，提供 `approval` Service |
| `packages/interaction/user-questions` | `internal/assembly` 的 UserQuestions Plugin | 提供 `userQuestions` Service，并可读取 live Agent Registry |
| `packages/interaction/tool-ask-user` | `internal/assembly` 的 Tool Ask User Plugin | 消费 Tools/UserQuestions 并注册 `ask_user_question` |

Go 不复制 Proxy property lookup、decorator、declaration merging、npm module loader、Profile evaluator 或 `!!js`。这些机制在 Go 中分别由显式 interface、泛型自由函数、静态 Catalog 和 typed config 取代；Service/Provider/Consumer、事件 mode 和 effect 生命周期不因语言变化而合并。

## 2. 模块边界

### 2.1 `plugin`

`plugin` 是公开扩展 contract，拥有：

- `Plugin`、`Manifest`、`Context`、Service/Event/Waterfall Definition；
- `plugin/configuration` 与 `plugin/factory` 子包中的构造边界；
- `Runtime` 的 Plugin declaration、Service graph settlement、replacement 与 shutdown；
- 每次 `Apply`/`Dispose` 的 `Scope`、私有 LIFO effect stack 和 diagnostics；
- owner-defined `ServiceDefinition[S]`、`EventDefinition[E]`、`WaterfallDefinition[I,O]` 与 typed 注册/dispatch；
- JSON Factory 边界的 strict typed decode helper。

`plugin` 不拥有 HTTP、Agent turn、Session Event、Tool policy、LLM wire、存储事务或具体 Provider config。它只协调已经由各能力 owner 定义的 contract。

### 2.2 `internal/assembly`

`internal/assembly` 是 shipped server composition owner，拥有：

- 当前可实例化 Factory 的白名单；
- process-derived `Environment` 与 Factory 输入 `PluginSpec`；
- 当前 included server 的默认 declaration 集合；
- Session、Session Projection、Session Title、Default Model、System Prompt、Tools、Approval、UserQuestions Provider，LLM Retry/Tool Ask User Consumer、API Proxy Consumer/Provider 与 Connection Consumer Plugin；
- 多 Plugin 启动失败时的 composition rollback。

它不重新解释 HTTP/RPC contract，也不把 Excluded/Deferred capability 注册为占位 Factory。外部扩展通过自定义 composition root 静态加入公开 Factory，而不是修改 Runtime 内部 map。

### 2.3 `cmd/goren`

命令入口只解析首期 CLI typed fields、解析工作目录、创建 Catalog/Runtime、加载 shipped declarations、等待 signal 并触发 bounded shutdown。它不直接注册 API route、创建 Echo、解析 Plugin 私有配置或持有 Service 实例。

## 3. Service Definition 与依赖结算

每项 Service 由 owner package创建并导出唯一 `ServiceDefinition[S]`。Definition 包含 canonical source name 和私有 token；`S` 是嵌入 `plugin.Service` 的能力 interface。Manifest 使用擦除后的 `ServiceRef` 声明 `Provides`、`Requires` 与 `Optional`，业务调用通过 Definition 的 `Provide`/`Require` 保持静态类型。

```text
Consumer Load
  -> Manifest.Requires 尚不可解析
  -> StateWaiting，不执行 Apply

Provider Load
  -> shadow Scope 执行 Apply
  -> Provide 只写入候选 Scope
  -> Apply 与 contribution invariant 成功
  -> Service 原子发布
  -> Runtime 重新结算 waiting Consumer
  -> Consumer Apply 读取 typed interface
```

依赖结算不使用 declaration 文件顺序。一个 canonical Service name 只能关联一个 owner key，并只能有一个已声明 Provider；同名 key 被重新创建、重复 Provider 或已声明 Service 未实际提供都会在激活前后相应边界失败，不执行 last-write-wins。

Provider Fiber 因 unload、dependency change 或 replacement 停止时，Runtime 先停止直接与传递 Consumer，再由 Provider Fiber 的私有 registration effect 撤回 Service；仍保留的 Consumer declaration 回到 Waiting，并在新 Provider active 后重新激活。Go API 不向插件公开单项 Service registration cleanup。

## 4. Scope、effect 与失败回滚

每次 `Plugin.Apply` 获得一个独立 Context 和 Root Scope。Runtime 在调用 `Apply` 前先登记调用该 Plugin `Dispose` 的 lifecycle effect；`Provide`、Waterfall Middleware、Event Observer 和 Child Plugin 也形成当前 Fiber 的私有 Effect。registration Effect 在 Mount commit 前保持不可见，插件作者不创建 Effect，也不接触 registration cleanup。

```text
Apply
  -> provide Service
  -> start and retain Plugin-owned resource
  -> failure
  -> rolling-back
  -> withdraw pending Service
  -> Plugin.Dispose partial resource
  -> failed
```

Plugin `Dispose` 必须幂等、接受 cleanup context，并等待自身 owned operation 停止；Runtime 的私有 release 操作严格按登记逆序运行。候选 Scope 在 `Apply` 与 contribution invariant 全部成功前不进入全局 Service/Event view，因此启动失败不会留下 route、listener、goroutine 或 Service。`FiberStatus` 暴露 ID、canonical name、State、live effect label 和最后一次 lifecycle error，不暴露锁、内部表或业务对象。

Fiber 进入 `stopping` 后不再参与新的 Service 解析、Waterfall snapshot 或 Event snapshot；随后私有 Effect stack 按 LIFO 执行，每个 registration release 从 Registry 移除自己的精确 entry，最后调用 Plugin `Dispose`。未激活的 rollback Fiber 从未进入全局 Service/Waterfall/Event view。

Root Plugin Scope 的 `Target()` 是 global zero key。`Context.ChildScope(label)` 生成 opaque key 并记录 parent lineage；普通 Child Scope 与当前 Fiber 同寿命，只改变可见性，不能提供 root Service。需要独立生命周期时使用 `Context.LoadChild`；Child Plugin 形成 Child Fiber，并作为 Effect 加入父 Fiber stack。scoped event 读取同一 lineage：global、祖先和 exact listener 可见，sibling 与 descendant listener 不可见。

## 5. Replacement 与 shutdown

replacement 必须保持 Plugin canonical name 和 `Provides` 集合，避免把一个 Handle 偷换为不同职责。流程为：

```text
active(old)
  -> strict decode + Factory.New(candidate)
  -> candidate shadow Apply
  -> candidate invariant success
  -> stop dependents against old Service
  -> atomic Service/Event scope swap
  -> dispose old scope
  -> restart dependents against candidate
```

候选失败只撤回候选 registration 并调用候选 `Dispose`，旧 Plugin 与 Consumer 保持 active。候选提交后旧 Plugin `Dispose` failure 会作为 replacement error 返回，但不能把已经发布的新实例伪装成未生效。

Runtime shutdown 禁止新 Load，按依赖图的反向方向停止 Consumer 后停止 Provider；无依赖关系时按 declaration 逆序回收。Connection Plugin 的 `Dispose` 取消 Echo lifecycle、等待 HTTP/WebSocket cleanup，并在调用方 cleanup deadline 到期时强制关闭 listener/downlink。

## 6. Typed Event 与 Waterfall

Event 与 Waterfall 是两个独立机制，不再通过 mode 枚举和 callback union 混成一个 Registry。

`EventDefinition[E Event]` 固定 canonical name、owner token 与 delivery policy。`EventObserver[E]` 是命名对象，通过 `ObserveEvent(context.Context, E) error` 接收已经发生的 fact。`DeliveryOrdered` 按 registration 顺序执行并聚合错误；`DeliveryParallel` 并发执行后聚合错误；`DeliveryBestEffort` 通过 Runtime reporter 隔离 Observer failure。Event 没有返回决策值，也不承担前置修改或短路。

`WaterfallDefinition[I WaterfallInput, O WaterfallOutput]` 固定一个可拦截动作。命名 `WaterfallMiddleware[I,O]` 通过 `Intercept` 包裹 `WaterfallNext[I,O]`；owner 提供 `WaterfallTerminal[I,O]` 执行真实 Workflow。每次调用先取得 root-to-current Middleware snapshot，再在锁外运行洋葱链；一个 `WaterfallNext` 对同一次调用只能 `Proceed` 一次。

Event 和 Waterfall registration 都是 Fiber 私有 Effect，Plugin 停止后不再进入新 snapshot。Publish/Run 的 `sourceScope` 表示发布者或调用者所在 Runtime 与可见性位置；global、ancestor、exact、sibling 和 descendant 的 admission 统一使用 `ScopeLineage`，不创建第二套作用域模型。

## 7. Factory Catalog 与 typed config

构造边界位于 `plugin/configuration` 与 `plugin/factory` 子包。能力 owner 定义嵌入 `configuration.Input` 的命名配置，`Configurator.Configure(Document)` 负责 strict decode、cross-field validation 并返回已经配置完成的 `Factory`；`Factory.Create` 只构造 Plugin。Catalog 只注册和查找 Configurator，不读取配置、不创建 Plugin，也不进入 Runtime 核心。

当前 strict JSON 边界分别负责：

1. token scan 拒绝任意层级 duplicate key；
2. `encoding/json.Decoder.DisallowUnknownFields` 完成 typed decode；
3. owner validator 检查空值、范围与字段组合；
4. Configurator 只把已经通过上述步骤的具体类型放入 configured Factory。

前两步不是重复验证：duplicate key 在标准 typed decode 中会被静默覆盖，必须由结构扫描单独拒绝；typed decode 则负责字段形态、Go 类型和未知字段。非 JSON 的 `!!js`、脚本表达式和多 JSON value 在入口直接失败，不进入 Factory 或 Runtime。

shipped Catalog 当前只有：

- `@deepseek-ai/dsh-host-apiproxy`；
- `@deepseek-ai/dsh-client-connection` 的 Host half；
- `@deepseek-ai/dsh-agent-default-model` 的 deployment default Provider；
- `@deepseek-ai/dsh-llm` 的 provider-neutral Runtime Provider；
- `@deepseek-ai/dsh-llm-deepseek` 的 direct DeepSeek Adapter Consumer；
- `@deepseek-ai/dsh-llm-retry` 的默认 RetryPolicy Consumer；
- `@deepseek-ai/dsh-session` 的内存 LiveStore Provider；
- `@deepseek-ai/dsh-session-persistence` 的 `SessionLogStore`，内部装配默认 SQLite fact Backend；
- `@deepseek-ai/dsh-session-projection` 的内存 Registry Provider；
- `@deepseek-ai/dsh-session-title` 的 log-backed Title Provider；
- `@deepseek-ai/dsh-system-prompt` 的 Registry/Assembly Provider；
- `@deepseek-ai/dsh-tools` 的 Native Registry/Execution Provider；
- `@deepseek-ai/dsh-agent` 的 live Registry/Inbox contract Provider；
- `@deepseek-ai/dsh-agent-loop` 的 concrete Agent lifecycle 与 request driver Provider；
- `@deepseek-ai/dsh-user-approval` 的 policy/audit Provider；
- `@deepseek-ai/dsh-user-questions` 的 question Provider Registry；
- `@deepseek-ai/dsh-tool-ask-user` 的 `ask_user_question` Tool Consumer；
- `@deepseek-ai/dsh-workspace` 的 Registry，内部装配默认 SQLite Workspace Backend；
- `@gorenx/dsh-web` 的极简内嵌 Web `http.Handler` Provider。

SQLite adapter 不单独注册 Factory，不提供 storage Service，也没有独立 Plugin Scope；Session Persistence 和 Workspace 两个能力插件各自构造并拥有自己的 adapter。Connection Factory 虽然沿用源 npm canonical name，但只实现服务端 Host carrier，不包含 `WebApiClient`、`ConnectionController` 或页面代码；它通过私有 `webFrontend` Service 消费根级 `web.Site`。原版 Web runtime、SDK、Tools Code Mode、ACP、MCP、Typert 与其他 Deferred 能力不在 Catalog 或依赖闭包。

## 8. 当前 server 组合流程

默认 declarations 按 Connection、API Proxy、Tool Ask User、Agent Default Model、LLM Retry、Session Title、Session Projection、Agent Loop、Approval、UserQuestions、Agent、LLM、DeepSeek、System Prompt、Tools、Session、Session Persistence、Workspace、Web 声明。Consumer 故意出现在部分 Provider 之前，以证明 Runtime 按 Service graph 而不是文件顺序工作：

```text
cmd/goren
  -> assembly.NewCatalog(Environment{cwd})
  -> assembly.DefaultSpecs(listen, version, sessionDB, workspaceDB)
  -> connection Factory.Create
  -> Connection StateWaiting (requires apiProxy/webFrontend)
  -> API Proxy StateWaiting (requires agents/agentDefaultModel/llm/sessions/sessionPersistence/sessionProjections/sessionTitle/userQuestions/workspaceRegistry)
  -> Tool Ask User StateWaiting (requires tools/userQuestions)
  -> Agent Default Model Factory.Create + Apply
       -> static deployment selection + Provide(agentDefaultModel)
  -> LLM Retry StateWaiting (requires agents)
  -> Session Title StateWaiting (requires sessions/sessionProjections/llm when LLM title is configured)
  -> Session Projection Factory.Create + Apply
       -> DriveRegistry + Provide(sessionProjections)
  -> Agent Loop StateWaiting (requires agents/sessions/llm/tools/systemPrompt)
  -> Approval StateWaiting (requires systemPrompt)
  -> UserQuestions Factory.Create + Apply
       -> live optional Agent resolver + Provide(userQuestions)
  -> Agent Factory.Create + Apply
       -> live Registry + Provide(agents)
  -> Runtime settles LLM Retry
       -> install provider-routed request retry Consumer
  -> LLM Factory.Create + Apply
       -> provider-neutral Runtime + Provide(llm)
  -> DeepSeek Factory.Create + Apply
       -> Require(llm)
       -> register configurable provider + adapter route
  -> System Prompt Factory.Create + Apply
       -> promptStore + promptAssembler + built-in sections
       -> Provide(systemPrompt)
  -> Runtime settles Approval
       -> policy/audit Runtime + Provide(approval)
  -> Tools Factory.Create + Apply
       -> Require(systemPrompt)
       -> toolStore + toolRegistry
       -> register ToolProvider projection with System Prompt
       -> Provide(tools)
  -> Runtime settles Tool Ask User
       -> Require tools/userQuestions
       -> register ask_user_question Tool
  -> Session Factory.Create + Apply
       -> MemoryStore + Provide(sessions)
  -> Runtime settles Session Persistence
       -> construct SQLite Backend + SessionLogStore
       -> Provide(sessionPersistence)
  -> Runtime settles Workspace
       -> construct SQLite Backend + DurableRegistry
       -> Provide(workspaceRegistry)
  -> Web Factory.Create + Apply
       -> embedded web.Site + Provide(webFrontend)
  -> Runtime settles Session Title
       -> register title projection + event/llm observers
       -> construct configured First-Prompt or All-Prompts LLMProvider inside the same Plugin
       -> Provide(sessionTitle)
  -> Runtime settles Agent Loop
       -> Require five capability Services
       -> register concrete Factory with agents
       -> Provide(agentLoop)
  -> Runtime settles API Proxy
       -> Require agents/agentDefaultModel/llm/sessions/sessionPersistence/sessionProjections/sessionTitle/userQuestions/workspaceRegistry
       -> apiproxy/session Gateway + WorkspaceGateway + host.describe + session.*/workspace.* methods
       -> InteractionGateway + approval/question pending/respond/replay
       -> real Mux/Host EventStreams
       -> Provide(apiProxy)
  -> Runtime settles Connection
       -> Require(apiProxy/webFrontend)
       -> NewHTTPHost with browser fallback handler
       -> pre-bind TCP listener
       -> Connection Plugin.Apply starts Echo Serve
       -> Provide(webServer)
  -> fixed TypeScript client can call HTTP/WebSocket contract
  -> browser can open the embedded main-conversation UI
```

预绑定 listener 让地址占用和权限错误在 Plugin activation 内同步失败，而不是让 goroutine 启动后才产生不可归属的异步错误。`apiProxy` 同时暴露 Connection 所需的 `RPCDispatcher` 与 `EventSource` facet；具体 `apiproxy.Catalog` 和 `EventStreams` 不越过 assembly boundary。

## 9. 隔离与后续能力进入

当前 Scope 已表达 Plugin instance ownership、effect-owned Child Scope、opaque lineage 和 scoped listener filter；System Prompt、Tools 与 Agent events 已直接复用它完成 overlay、restriction 与 subject isolation，LLM/DeepSeek route contribution 也由 Provider Scope 精确拥有。Agent 的详细边界见[14 Agent Registry、Inbox 与实时事件模块设计](./14-agent-registry-inbox-and-events.md)。当前尚未实现同一 Service 的 label isolation；只有出现真实多实例 resolution Consumer 时才扩展现有 Service resolution，不得另用 `context.Context.Value`、全局 map 或第二套 Registry。

LLM Runtime 与 DeepSeek Provider 的本地职责、调用链和失败边界见[13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)。

新能力进入 shipped composition 时必须同时提供 canonical Factory name、owner-defined typed config、Manifest dependencies、幂等 Plugin `Dispose`、失败 rollback 测试和 Excluded/Deferred 审计。Storage、Agent、Session 或 Tool 业务不能放入 assembly Factory 以绕开其能力 owner。
