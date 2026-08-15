# 09 Plugin Runtime 与 Server Assembly 模块设计与实现

状态：Accepted

本文拥有 `plugin` 与 `internal/assembly` 的职责、Go 类型模型、上下游流程和生命周期。全局依赖方向与 Child Scope 使用规则由[02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)拥有；System Prompt 与 Tools 的消费语义分别由[11 System Prompt Registry 与 Assembly 模块设计](./11-system-prompt-registry-and-assembly.md)和[12 Tools Registry 与执行流水线模块设计](./12-tools-registry-and-execution-pipeline.md)拥有；当前实施证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 源职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留的职责 |
| --- | --- | --- |
| `vendor/cordis/src/registry.ts` 的 `Plugin`、`RegistryService` | `plugin.Plugin`、`Factory[C]`、`Catalog` | 静态 Factory 注册、Plugin 创建和实例跟踪 |
| `vendor/cordis/src/fiber.ts` 的 `Fiber`、`effect`、`FiberState` | `plugin.Runtime`、`Scope`、`Disposer`、`State` | dependency settlement、effect ownership、rollback、unload |
| `vendor/cordis/src/reflect.ts` 的 `provide`、`notify` | `ServiceKey[T]`、`Provide`、`Require` | Service Definition、唯一 Provider、Consumer 重启 |
| `vendor/cordis/src/events.ts` | `EventKey[P,R]` 与五种 typed dispatch | listener ownership、顺序、bail 与 middleware control |
| `vendor/cordis/src/context.ts` | `Scope` | Plugin instance 的资源与 contribution owner |
| `packages/core/scope/src/index.ts`、`store.ts` | `Scope.Child`、`ScopeKey`、`ScopeLineage` | opaque child identity、祖先链、effect ownership 与 scoped event admission |
| `packages/host/apiproxy` | `internal/assembly` 的 API Proxy Plugin | 提供 `apiProxy` Service |
| `packages/client/connection` 的 Host half | `internal/assembly` 的 Connection Plugin | 消费 `apiProxy`，挂载 HTTP/WebSocket carrier |
| `packages/core/system-prompt` | `internal/assembly` 的 System Prompt Plugin | 提供 `systemPrompt` Service |
| `packages/core/tools` | `internal/assembly` 的 Tools Plugin | 消费 `systemPrompt`，提供 `tools` Service 并注册 schema projection |

Go 不复制 Proxy property lookup、decorator、declaration merging、npm module loader、Profile evaluator 或 `!!js`。这些机制在 Go 中分别由显式 interface、泛型自由函数、静态 Catalog 和 typed config 取代；Service/Provider/Consumer、事件 mode 和 effect 生命周期不因语言变化而合并。

## 2. 模块边界

### 2.1 `plugin`

`plugin` 是公开扩展 contract，拥有：

- `Plugin`、`Manifest`、`Factory[C]`、`Catalog`；
- `Runtime` 的 Plugin declaration、Service graph settlement、replacement 与 shutdown；
- 每次 `Apply` 的 `Scope`、LIFO effect stack 和 diagnostics；
- owner-defined `ServiceKey[T]`、`EventKey[P,R]` 与 typed 注册/dispatch；
- JSON Factory 边界的 strict typed decode helper。

`plugin` 不拥有 HTTP、Agent turn、Session Event、Tool policy、LLM wire、存储事务或具体 Provider config。它只协调已经由各能力 owner 定义的 contract。

### 2.2 `internal/assembly`

`internal/assembly` 是 shipped server composition owner，拥有：

- 当前可实例化 Factory 的白名单；
- process-derived `Environment` 与 Factory 输入 `PluginSpec`；
- 当前 included server 的默认 declaration 集合；
- Session Provider、System Prompt Provider、Tools Consumer/Provider、API Proxy Consumer/Provider 与 Connection Consumer Plugin；
- 多 Plugin 启动失败时的 composition rollback。

它不重新解释 HTTP/RPC contract，也不把 Excluded/Deferred capability 注册为占位 Factory。外部扩展通过自定义 composition root 静态加入公开 Factory，而不是修改 Runtime 内部 map。

### 2.3 `cmd/goren`

命令入口只解析首期 CLI typed fields、解析工作目录、创建 Catalog/Runtime、加载 shipped declarations、等待 signal 并触发 bounded shutdown。它不直接注册 API route、创建 Echo、解析 Plugin 私有配置或持有 Service 实例。

## 3. Service Definition 与依赖结算

每项 Service 由 owner package 创建并导出唯一 `ServiceKey[T]`。key 包含 canonical source name 和私有 token；`T` 通常是能力 interface。Manifest 使用擦除后的 `ServiceRef` 声明 `Provides`、`Requires` 与 `Optional`，业务调用仍通过 `Provide[T]` / `Require[T]` 保持静态类型。

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

active Service 的 disposer 被显式调用时，Runtime 先停止直接与传递 Consumer，再撤回 Registry entry；Provider Plugin 可以在同一 active Scope 重新 `Provide`，Runtime 随后重新激活等待的 Consumer。Plugin unload 使用相同的 dependent-first 规则，但最终移除 Provider declaration。

## 4. Scope、effect 与失败回滚

每次 `Plugin.Apply` 获得一个独立 Scope。`Provide`、Event listener 和 `Scope.Effect` 都在该 Scope 中登记 disposer；外部资源由 Effect setup 立即获取，setup 成功必须返回非空 disposer。

```text
Apply
  -> acquire effect A
  -> provide Service
  -> acquire effect B
  -> failure
  -> rolling-back
  -> dispose B
  -> withdraw pending Service
  -> dispose A
  -> failed
```

disposer 幂等、接受 cleanup context，并严格按登记逆序运行。候选 Scope 在 `Apply` 与 contribution invariant 全部成功前不进入全局 Service/Event view，因此启动失败不会留下 route、listener、goroutine 或 Service。`PluginStatus` 暴露 ID、canonical name、State、live effect label 和最后一次 lifecycle error，不暴露锁、内部表或业务对象。

Scope 进入 `stopping` 后，listener 不会一次性提前消失；每个 listener 直到自己的 disposer 执行才停止参与 dispatch。这样同一 Scope 中排在 listener 之后登记的业务 effect 可以在 LIFO teardown 时发布最终 disposed/flush edge，然后再注销 listener。未激活的 rollback Scope 仍不会进入全局 Event view。

Root Plugin Scope 的 `Target()` 是 global zero key。`Scope.Child(label)` 生成 opaque key 并记录 parent lineage；Child 继承所属 Plugin 已声明的 Service dependency，但不能提供 root Service。Child 本身是 parent-owned effect，支持提前释放，parent teardown 也会按 LIFO 释放其嵌套 effect。scoped event 读取同一 lineage：global、祖先和 exact listener 可见，sibling 与 descendant listener 不可见。

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

候选失败只回滚候选，旧 Plugin 与 Consumer 保持 active。候选提交后旧 disposer failure 会作为 replacement error 返回，但不能把已经发布的新实例伪装成未生效。

Runtime shutdown 禁止新 Load，按依赖图的反向方向停止 Consumer 后停止 Provider；无依赖关系时按 declaration 逆序回收。Connection Plugin disposer 取消 Echo lifecycle、等待 HTTP/WebSocket cleanup，并在调用方 cleanup deadline 到期时强制关闭 listener/downlink。

## 6. Typed Event modes

`EventKey[P,R]` 固定 canonical name、dispatch mode 与 owner token。payload/result type 由 key 的泛型参数确定；`EventHandler[P,R]` 是三种 handler named function type 的封闭编译期集合。Go 的 union type set 只能作为泛型约束，不能作为可调用的运行时值，因此 Runtime 不伪造一个统一 `Invoke` 方法。`typedEventSubscription[P,R,H]` 保留具体 callback；异构订阅只通过 `eventSubscription` 共享 lifecycle metadata，dispatch 恢复精确泛型订阅类型。callback 不经过 `any` 或反射。

| Mode | Go handler 与控制语义 |
| --- | --- |
| `emit` | 同步按 listener 注册顺序全部执行，聚合 error，不短路 |
| `parallel` | 全部启动、等待全部结束，再聚合 error |
| `serial` | 顺序执行，首个 `Decision.Bail=true` 或 error 停止 |
| `bail` | 同步顺序决策，首个 `Decision.Bail=true` 或 error 停止 |
| `waterfall` | outer-to-inner middleware；只有调用 `Next` 才进入下游/terminal |

`Decision.Bail` 与 `Value` 分离，因为 `false`、`0`、空字符串等零值可能是合法的最终拒绝或选择；`Bail=false` 才表示当前 listener 不作决定。listener 是 Scope-owned effect，手动 disposer 或 Plugin unload 后不再参与 dispatch。

需要先改变状态再发通知的 owner 可在 commit 前通过 `CaptureEmitFrom` 固定 `EmitSnapshot`，commit 后再调用 `Dispatch`。这样 listener 的并发注册/卸载不会改变一次已经开始的 publication；snapshot 仍保存精确 `NotifyHandler[P]`，不引入反射或无类型 callback。

`EmitFrom(sourceScope, key, payload)` 从长期 Service 的 Scope 找到 Runtime 并通知全部存活 listener；`EmitScopedFrom(sourceScope, key, selectedKey, payload)` 仍从同一 Service Scope 找 Runtime，但只通知 global、`selectedKey` 祖先和 exact listener。Tools 因而用前者发布可能影响所有 Agent view 的 `tools/change`，用后者发布只属于一次 Agent scope 的 `tools/result`。`sourceScope` 表示事件发布者所在 Runtime，`selectedKey` 表示本次业务 view，不是两个 Service 实例。

## 7. Factory Catalog 与 typed config

`Factory[C]` 的能力 owner 定义命名配置 `C`、strict decoder、cross-field validation 与构造函数。Catalog 只保存已注册的 decoder/constructor closure；`json.RawMessage` 在 `Catalog.Create` 后不再进入 Plugin。

当前 strict JSON 边界分别负责：

1. token scan 拒绝任意层级 duplicate key；
2. `encoding/json.Decoder.DisallowUnknownFields` 完成 typed decode；
3. owner validator 检查空值、范围与字段组合；
4. `Factory.New` 只接收已经通过上述步骤的具体类型。

前两步不是重复验证：duplicate key 在标准 typed decode 中会被静默覆盖，必须由结构扫描单独拒绝；typed decode 则负责字段形态、Go 类型和未知字段。非 JSON 的 `!!js`、脚本表达式和多 JSON value 在入口直接失败，不进入 Factory 或 Runtime。

shipped Catalog 当前只有：

- `@deepseek-ai/dsh-host-apiproxy`；
- `@deepseek-ai/dsh-client-connection` 的 Host half；
- `@deepseek-ai/dsh-llm` 的 provider-neutral Runtime Provider；
- `@deepseek-ai/dsh-llm-deepseek` 的 direct DeepSeek Adapter Consumer；
- `@deepseek-ai/dsh-session` 的内存 Store Provider；
- `@deepseek-ai/dsh-system-prompt` 的 Registry/Assembly Provider；
- `@deepseek-ai/dsh-tools` 的 Native Registry/Execution Provider。

其中 Connection Factory 虽然沿用源 npm canonical name，但只实现服务端 Host carrier，不包含 `WebApiClient`、`ConnectionController` 或浏览器代码。Web UI、SDK、Tools Code Mode、ACP、MCP、Typert 与其他 Deferred 能力不在 Catalog 或依赖闭包。

## 8. 当前 server 组合流程

默认 declarations 按 Connection、API Proxy、LLM、DeepSeek、System Prompt、Tools、Session 声明。Connection/API Proxy/Session 链故意采用 Consumer-before-Provider 顺序，以证明 Runtime 按 Service graph 而不是文件顺序工作；DeepSeek 和 Tools 分别在 LLM 与 System Prompt 激活后消费其 Service：

```text
cmd/goren
  -> assembly.NewCatalog(Environment{cwd})
  -> assembly.DefaultSpecs(listen, version)
  -> connection Factory.Create
  -> Connection StateWaiting (requires apiProxy)
  -> API Proxy StateWaiting (requires sessions)
  -> LLM Factory.Create + Apply
       -> provider-neutral Runtime + Provide(llm)
  -> DeepSeek Factory.Create + Apply
       -> Require(llm)
       -> register configurable provider + adapter route
  -> System Prompt Factory.Create + Apply
       -> promptStore + promptAssembler + built-in sections
       -> Provide(systemPrompt)
  -> Tools Factory.Create + Apply
       -> Require(systemPrompt)
       -> toolStore + toolRegistry
       -> register ToolProvider projection with System Prompt
       -> Provide(tools)
  -> Session Factory.Create + Apply
       -> MemoryStore + Provide(sessions)
  -> Runtime settles API Proxy
       -> Catalog + host.describe + empty cancellable EventStreams
       -> Provide(apiProxy)
  -> Runtime settles Connection
       -> Require(apiProxy)
       -> NewHTTPHost
       -> pre-bind TCP listener
       -> Effect starts Echo Serve
       -> Provide(webServer)
  -> existing TypeScript client can call HTTP/WebSocket contract
```

预绑定 listener 让地址占用和权限错误在 Plugin activation 内同步失败，而不是让 goroutine 启动后才产生不可归属的异步错误。`apiProxy` 同时暴露 Connection 所需的 `RPCDispatcher` 与 `EventSource` facet；具体 `apiproxy.Catalog` 和 `EventStreams` 不越过 assembly boundary。

## 9. 隔离与后续能力进入

当前 Scope 已表达 Plugin instance ownership、effect-owned Child Scope、opaque lineage 和 scoped listener filter；System Prompt 与 Tools 已直接复用它完成 overlay、restriction 与 scoped event，LLM/DeepSeek route contribution 也由 Provider Scope 精确拥有。Agent instance 后续继续复用同一 identity。当前尚未实现同一 Service 的 label isolation；只有出现真实多实例 resolution Consumer 时才扩展现有 Service resolution，不得另用 `context.Context.Value`、全局 map 或第二套 Registry。

LLM Runtime 与 DeepSeek Provider 的本地职责、调用链和失败边界见[13 Harness LLM Runtime 与 DeepSeek Provider 模块设计](./13-harness-llm-runtime-and-deepseek-provider.md)。

新能力进入 shipped composition 时必须同时提供 canonical Factory name、owner-defined typed config、Manifest dependencies、全部 effect disposer、失败 rollback 测试和 Excluded/Deferred 审计。Storage、Agent、Session 或 Tool 业务不能放入 assembly Factory 以绕开其能力 owner。
