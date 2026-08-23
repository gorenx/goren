# Goren 当前实现问题与一次性对象化重构方案

状态：重构实施中；`plugin/...` 底座已按目标契约完成包内验证，业务能力调用方尚未迁移

适用范围：`plugin`、`internal/assembly` 以及当前 Harness 主流程涉及的 Agent、Session、System Prompt、Tools、LLM、Interaction、API Proxy、Connection、Workspace、Credentials 能力包。

源兼容基线：DeepSeek Harness `47f943859bef60e4160492346772ded9b24f765a`。

Plugin Runtime 的详细目标模型、生命周期、Scope、Capability、Event 与 Waterfall 流程由 [Go Cordis 风格插件事件领域运行时设计方案](./Go_Cordis_风格插件事件领域运行时设计方案.md)拥有。本文只记录当前实现审计、跨能力对象化原则、一次性迁移范围和验收条件，不替代该运行时详细设计。

本文是一次性重构的设计与实施输入，不表示当前代码已经完成重构。当前权威职责与实施状态仍分别由 [02 Go 运行时架构与插件模型](./02-runtime-architecture-and-plugin-model.md)、[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)和[08 实施进度](./08-implementation-progress.md)拥有。重构落地时必须同步修改这些权威文档；在此之前不得把本文中的目标结构写成已实现事实。

## 1. 结论

原方案关于 `Fiber / Scope / Capability / Event / Waterfall / Effect / Domain Workflow` 的职责分离方向成立，但不能直接按原伪代码实现。最终设计作出以下决定：

1. 保留 Harness/Cordis 和仓库既有的 canonical 名称 `Plugin`，不再引入替代身份 `Daemon`。Plugin 对象通过 `Apply/Dispose` 拥有自身运行资源，`Fiber` 是 Runtime 对一次激活的生命周期记录。
2. `plugin` 是统一的 Plugin Runtime 模块和仓库级 ownership；`Runtime`、Fiber、Scope、Capability、Event、Hook 与 diagnostics 只能位于 `plugin/` 或其子包中，不能成为与 `plugin` 平级的新领域。`Runtime` 只组合和调度对象；业务主流程仍由 Agent、Session、Tools、LLM 等能力 owner 的命名 struct 和方法拥有。
3. 目标 Runtime、能力契约和领域泛型禁止使用 `any` 作为类型约束。泛型参数必须受 `Capability`、`RuntimeEvent`、`HookInput`、`HookOutput`、`SessionEventData`、`RequestPayload`、`ResponsePayload` 或 `Frame` 等有明确语义的 interface 约束。
4. 不用空壳 interface 机械替换 `any`。每个约束必须限制合法角色，例如 Capability 必须是长期可调用对象，Event 必须是已发生事实，Hook input/output 必须属于一个可拦截动作。
5. 长期依赖、策略、注册、资源、ID 生成、时钟、错误报告、流和后台任务必须是命名对象或 consumer-owned interface，不得存成函数字段。Plugin 资源由同一命名对象的 `Dispose` 清理；只有 Runtime 私有 effect release 等天然生命周期 seam 可以使用函数值。
6. Event 只传播已经提交的事实；`serial`、`bail`、`waterfall` 等能够 veto、选择或改变结果的控制语义迁入 Hook 或显式协调器。
7. 硬依赖继续由 `Manifest.Requires` 在 `Apply` 前结算。依赖缺失时 Fiber 等待，不调用 `Apply`；不通过 `Require` 返回特殊错误来猜测依赖并重跑半完成的启动流程。
8. Service Definition 继续使用 canonical name 加 owner token 的身份，不改成 `reflect.Type` 身份，也不允许调用方按同名重建 key。
9. 本次重构是一次原子切换：删除旧 API、旧 Func adapter、旧 callback 字段和旧 Event mode 路径；不保留 deprecated wrapper、兼容 alias、双 Registry 或新旧并行流程。
10. TypeScript 客户端协议、canonical service/event 名、Session 持久事件、HTTP/WebSocket envelope 和 DeepSeek Provider 行为不因内部对象重构而改变。

## 2. 设计边界

### 2.1 要解决的问题

本次重构同时解决两类问题：

- `plugin` 只有一个较大的 `Runtime` 和一个同时承担生命周期与注册入口的 `Scope`，但缺少清晰的 Fiber、Mount Transaction、Registry owner 和运行时 Context 对象。
- 其他能力包虽然已有若干命名 service struct，却大量把真实协作者和扩展行为保存为函数类型、函数字段或闭包，导致调用者、生命周期 owner、状态和失败边界不容易从类型结构中看出来。

### 2.2 不改变的边界

- 不重新划分 Harness 已有的 Service Definition / Provider / Consumer owner。
- 不把 Session、Agent、Tools、LLM 合并成一个 Runtime 大对象。
- 不把 Event 改成持久化 Event Store；Session append-only log 仍由 Session owner 管理。
- 不引入第二个 DI 容器、全局 service locator、标准库 `plugin`、Goja、反射式方法调用或运行时脚本。
- 不把 Scope 解释成权限、租户安全或进程隔离。
- 不以本次内部重构为理由改变 wire、数据库 schema 或持久事件 JSON。
- 不把 Runtime、Fiber、Scope、Capability、Event、Hook 或 diagnostics 提升为仓库根目录下与 `plugin` 平级的包；它们可以在 `plugin/` 内按稳定依赖方向划分子包。

## 3. 当前实现证据

### 3.1 当前启动调用链

当前默认启动的主动调用关系是：

```text
cmd/goren
  -> internal/assembly.NewCatalog
  -> assembly.Load
  -> plugin.Catalog.Create
  -> concrete Factory.DecodeConfig / New
  -> plugin.Runtime.Load
  -> Runtime.reconcile
  -> Runtime.activate
  -> Plugin.Apply
  -> Service Definition Require / Provide / Waterfall Use / Event Observe
```

对应实现位置：

- `internal/assembly/catalog.go` 组装 Factory、默认 `PluginSpec` 和加载顺序；
- `plugin/catalog.go` 用 `Factory[C any]` 与 `typedFactory[C any]`擦除具体配置；
- `plugin/runtime.go` 的 `Load`、`reconcile`、`activate`、`Unload`、`Replace`、`Shutdown` 同时负责声明、依赖结算、状态转换、贡献发布和清理；
- `plugin/scope.go` 同时保存 Service contribution、Event subscription、Effect 和 Child Scope；
- `plugin/service.go` 用 `ServiceKey[T any]` 暴露 `Provide` 与 `Require`；
- `plugin/events.go` 用一套 `EventKey[P, R any]` 承载 emit、parallel、serial、bail 和 waterfall 五种不同控制语义。

注册期与运行期必须区分：

```text
注册期
Plugin.Apply 主动创建能力对象并把 Service、Observer、Interceptor 注册到 Runtime；Plugin 自身资源保存在该对象上。

运行期
AgentLoop、Session、Tools 或 API Gateway 主动调用稳定能力对象；
能力 owner 在明确节点主动发布事实或运行 Hook；
Plugin 本身不会被 AgentLoop 逐个遍历调用。
```

### 3.2 当前实现已经正确的部分

本次重构不是推倒所有语义。以下基础应保留：

- `ServiceKey` 使用 canonical name 加私有 token，能够拒绝同名重建和错误类型擦除；
- `Manifest` 明确声明 `Provides`、`Requires` 和 `Optional`；
- `Apply` 失败会逆序回滚 Scope effect；
- Provider 消失时 Runtime 会先停止硬依赖 Consumer；
- replacement 先在 shadow Scope 准备候选，候选失败不破坏旧实例；
- Event listener 具有 Runtime-wide 注册序，Scope 过滤在 dispatch snapshot 时完成；
- `typedEventSubscription` 已避免把具体 callback 直接存成 `any`，但仍然存的是函数行为；
- 多数核心能力已有命名状态对象，例如 `session.MemoryStore`、`sessionprojection.DriveRegistry`、`workspace.Registry`、`agentloop.Runtime` 和 `tools` 内部 registry。

### 3.3 `plugin` 的主要问题

#### 3.3.1 Plugin 定义与运行实例没有显式分离

`pluginRecord` 实际承担 Fiber 的一部分职责，但它只是 `runtime.go` 内部记录：

- 没有公开、完整的 Fiber 生命周期对象；
- 没有父子 Fiber tree；
- Scope 同时承担可见性和 effect ownership；
- diagnostics 只能从多个内部字段拼接，不能直接观察一次运行实例的完整状态。

#### 3.3.2 `Runtime` 混合过多职责

`plugin.Runtime` 同时管理：

- Plugin declaration；
- Service Definition 与 Provider；
- 生命周期状态机；
- dependency settlement；
- replacement；
- Service publish/withdraw；
- Event definition；
- listener ordinal；
- shutdown ordering。

这些职责具有不同不变量和并发边界，应由 `FiberSupervisor`、`CapabilityDirectory`、`DependencyGraph`、`EventBus`、`HookRegistry` 和 `ScopeTree` 分别拥有，`Runtime` 只协调它们。

#### 3.3.3 `any` 出现在核心类型擦除边界

当前关键位置包括：

- `plugin.Runtime.serviceEntry.value any`；
- `plugin.Scope.serviceContribution.value any`；
- `ServiceKey[T any]`、`Factory[C any]`；
- `EventKey[P, R any]` 及全部 dispatch 泛型；
- `DecodeStrictConfig[C any]`。

这使“能注册什么”由运行时断言兜底，而不是由契约角色限制。它也允许 DTO、函数、临时值误入 Service Registry。

#### 3.3.4 Event 与 Hook 被一套 mode 混合

`plugin.EventMode` 同时包含：

- `emit`、`parallel`：事实或通知传播；
- `serial`、`bail`：决策和选择；
- `waterfall`：around interception。

这让包名和类型名无法说明“已经发生”还是“正在决定”。例如 `session/created` 当前通过 serial listener 错误 veto announcement，它实质上是 admission Hook，不是 created fact。

#### 3.3.5 Effect ownership 应保留，但不能成为插件作者接口

Cordis 的 effect 是 Fiber 对插件 callback、service、listener 和 Child Plugin 的统一所有权机制，不是一个业务能力。TypeScript 可以用 callback return、Promise 和 generator 表达 cleanup；Go 若再公开 `Effect.Setup` 与 `Disposer` 两个接口，会把 Runtime 内核泄漏给插件作者，并再次诱导匿名 setup/cleanup 对象。

目标 Go Runtime 只在 Fiber 内部保留 `fiberEffect` 和 release seam。Plugin 对象通过 `Apply` 直接建立并保存 Connection Host 等有状态资源，通过幂等 `Dispose` 负责 cancel、wait 和 force-close。Service、Waterfall、Event 和 Child Plugin registration 由 Runtime 私有生成 release 操作，同样进入 Fiber stack。插件作者 API 不包含 `Effect`、`Disposer` 或 `Context.Effect`。

### 3.4 其他能力包的回调与过程式热点

对非测试、非 sqlc generated Go 文件的静态扫描可以看到 58 个命名函数类型；最密集的是 `tools`、`agent`、`apiproxy`、`systemprompt` 和 `plugin`。问题不在函数数量本身，而在这些函数被长期保存为领域协作者或注册契约。

| Owner | 当前证据 | 责任问题 |
| --- | --- | --- |
| `agent` | `MaintenanceFunc`、`SetupFunc`、`ModelSelectionSource`、15 个 Event/Hook handler function type | Agent setup、模型来源、维护任务和扩展行为没有稳定对象身份 |
| `agentloop` | `tool_calls.go` 的 `commitReady`、`startCall`、`fillPool` 和 goroutine closure | Tool batch 的状态机藏在一个过程函数中，调度阶段和失败归属不清晰 |
| `session` | `LifecycleListener`、`AppendListener`、`Preparation.cleanup func()`、Clock/ObserverError 字段 | publication、observer、prepared ownership 和基础设施来源被回调化 |
| `session/persistence` | `liveWriter.write`、`report` 函数字段 | write-behind queue 同时保存 I/O 与错误处理过程，缺少 Port 对象 |
| `systemprompt` | `TextFunc`、`VariableProvider`、`ToolProvider`、`AssembleNext`、`AssembleHandler` | registry、resolution、assembly 和 interception 混在函数链里 |
| `tools` | `ExecutorFunc`、`OutputRendererFunc`、`ContentFinalizerFunc`、`ToolGuardFunc`、`ApprovalResolverFunc` 等 | 已有 interface 被 Func adapter 重新退化成匿名行为，Tool Runtime 又混合 registry、policy、execution、result assembly |
| `approval` / `userquestions` | Request waterfall callback、ID generator 函数字段、`AgentRegistryResolverFunc` | 决策请求、Provider directory 和可选能力读取缺少命名协调器 |
| `llmretry` | Random、NewRetryID、ObserverError 函数字段 | 重试协调器的外部来源没有 consumer-owned Port |
| `llm` / `llm/deepseek` | `ModelDiscoveryFunc`、Observer reporter、CurrentOptions/ResolveAPIKey/ResolveUserID/Now 函数字段 | Provider adapter 的配置、凭证、身份和时钟来源被闭包装配 |
| `apiproxy` | generic handler、`pendingEntry` 的 `decode/complete` 回调、EventStreams handler、stream queue `emit func` | method dispatch、pending correlation 和 downlink stream 没有独立对象生命周期 |
| `workspace` / `credentials` | Clock、NewID、ObserverError、LookupEnv 函数字段 | 可替换基础设施来源不是显式 Port |
| `internal/assembly` | 多个 Factory/Plugin 保存 env、filesystem、resolver closure，`apiProxyPlugin.Apply` 过程很长 | 组合根知道过多局部流程，并用闭包代替 owner-defined adapter |

### 3.5 根因

当前代码的共同根因是：

```text
为了方便注册或测试
  -> 把行为定义成 func
  -> 把 func 存进 Options / Registry / Entry
  -> 组合根用 closure 捕获依赖
  -> 状态和生命周期留在闭包外部
  -> 调用链只能逐层阅读 closure 才能恢复
```

最终结果不是“函数式代码本身不好”，而是长期职责没有对象 owner。一次功能往往跨越 Registry、callback、closure、Runtime dispatch 才能看完，业务流程不再集中于一个可命名、可测试的协调器。

## 4. 目标设计规则

### 4.1 何时必须使用命名 struct

以下角色必须是命名 struct：

- Runtime、Fiber、MountTransaction、ScopeTree、DependencyGraph；
- Registry、Directory、Coordinator、Supervisor、Scheduler、Assembler；
- 有状态的 Service、Gateway、Stream、Queue、Pending Call；
- 持有 context/cancel、锁、缓存、重试状态或资源句柄的对象；
- 一个完整用例或状态机的执行者；
- Effect、Registration、Worker、Server、Adapter；
- 组合多个 Port 后形成稳定行为的对象。

### 4.2 何时使用 interface

interface 只用于真实边界：

- Consumer 所需的最小 Capability；
- Repository、外部 Provider、Clock、ID Generator、Random Source、Failure Reporter；
- Event Observer、Hook Interceptor、Hook Terminal；
- Effect、Stream、Registration 等生命周期协议；
- Framework 或 external adapter 必须实现的端口。

interface 不用于：

- 给唯一 concrete service 机械加一层；
- 把 `any` 改名后继续接受任意类型；
- 把一个混合职责大对象拆成很多没有 owner 的碎片；
- 让 domain 反向依赖 assembly 或具体 adapter。

### 4.3 何时可以保留函数

以下函数用法可以保留，因为它们不是长期业务协作者：

- 方法内部的纯局部算法，例如排序比较、一次 `defer`、`sync.Once.Do` body；
- 由命名对象拥有并等待退出的 goroutine entry；
- Echo 要求的 `echo.HandlerFunc` 和 middleware closure，仅限 `internal/connection`；
- 标准库要求的 `context.CancelFunc`、`database/sql` 的 `...any` 参数和 `Scan(...any)`；
- 编译期不会被保存为领域状态的一次性小函数。

禁止保留：

- `type XxxFunc func(...)` 作为业务接口适配器；
- exported Options/Dependencies 中的函数字段；
- Registry entry 的 callback/handler/complete/decode 函数字段；
- composition root 捕获多个依赖的长期 closure；
- 返回裸 cleanup func 代表一个有状态资源。

### 4.4 `any` 使用规则

目标代码中，下列区域禁止 `[T any]`、`[P, R any]` 或以 `interface{}` 规避规则：

- `plugin` public/internal generic contract；
- Agent、Session、Tools、System Prompt、LLM 等能力 contract；
- API Proxy request/response/frame/pending generic；
- Session persisted event data generic；
- 领域 codec helper。

仅允许在不可控的外部技术边界使用 `any`：

- `database/sql` 查询参数和 Scan adapter；
- Echo/标准库接口的既定签名；
- 必须直接满足第三方接口的最薄 adapter。

允许项必须留在 adapter package，不能通过 type alias、Options 或 interface 泄漏到 owner contract。

## 5. 运行时落地约束

本节只说明原运行时详细设计落地到当前 Goren 时必须满足的类型和对象约束，不重复拥有完整 Runtime 流程。

物理结构必须先服从领域归属：Plugin Runtime 的全部契约和实现统一放在 `plugin/` 模块内。允许按职责建立 `plugin/fiber`、`plugin/scope`、`plugin/capability`、`plugin/event`、`plugin/waterfall`、`plugin/diagnostics` 等子包，也允许强耦合对象继续留在 `plugin` 包的不同文件中；禁止建立仓库根级 `runtime`、`fiber`、`scope`、`capability`、`event` 或 `waterfall` 包。详细目录由运行时设计文档的“推荐包结构”一节拥有。

这些子包只是同一 Plugin Runtime 模块内的实现边界，不是独立 bounded context。它们不能各自拥有 composition root、第二套 Runtime、第二棵 Scope tree 或独立 service locator。Agent、Session、Tools、LLM 等业务包仍按 Service Definition / Provider / Consumer 关系接入 `plugin`，不会因为拆包而变成 Plugin Runtime 的组成目录。

```text
Factory Catalog
      │
      │ strict decode + validate + construct
      ▼
Plugin Definition
      │
      │ Runtime.Load
      ▼
FiberSupervisor ─────────────── DependencyGraph
      │                               │
      ▼                               │
Fiber + MountTransaction              │
      │                               │
      ├── Context ── Scope ───────────┤
      ├── EffectStack                 │
      └── Contributions               │
             ├── CapabilityDirectory ─┘
             ├── EventBus
             └── HookRegistry
```

职责固定为：

```text
Plugin             系统组成定义
Fiber              Plugin 的一次运行实例和生命周期 owner
Context            当前 Fiber、Scope、MountTransaction 的运行句柄
Scope              可见性和路由，不拥有生命周期
Capability         稳定同步调用和依赖边
Event              已提交事实传播
Hook               可拦截动作
Effect              Runtime/Fiber 私有的可逆生命周期记录
Domain/Application 显式 Workflow、状态机和 invariant
```

### 5.1 Plugin 与 Factory

保留 `Plugin` 名称：

```go
type Plugin interface {
	Manifest() Manifest
	Apply(context.Context, *Context) error
	Dispose(context.Context) error
}
```

配置读取、配置实例化和 Catalog 查找分成三个责任：

```go
type Configurator interface {
	Name() string
	Configure(configuration.Document) (Factory, error)
}

type Factory interface {
	Name() string
	Create(context.Context) (Plugin, error)
}
```

每个 concrete Configurator 使用自己的命名 Config、strict decoder 和 validator，产生只保存已验证配置的 configured Factory。例如 Tools 构造链必须依次完成：

```text
configuration.Document
  -> tools.Config
  -> tools.ValidateConfig
  -> configured tools Factory
  -> toolsPlugin
```

Catalog 只注册和查找 Configurator，不执行 decode 或 construction；`configuration.Document` 不进入 Runtime、`Plugin.Apply` 或能力对象。

### 5.2 Fiber 与 FiberSupervisor

```go
type Fiber struct {
	identifier FiberID
	plugin     Plugin
	manifest   Manifest
	parent     *Fiber
	children   map[FiberID]*Fiber
	scope      *Scope
	state      FiberState
	lifetime   context.Context
	cancel     context.CancelCauseFunc
	effects    *EffectStack
}
```

`FiberSupervisor` 独占：

- Load、Unload、Replace、Shutdown；
- state transition；
- parent/child lifecycle；
- hard dependency start/stop ordering；
- waiting Fiber settlement；
- MountTransaction 创建与提交；
- Status/diagnostics snapshot。

`Runtime` 不再直接修改 Fiber 字段，只调用 Supervisor 方法。

### 5.3 Context 与 Scope

```go
type Context struct {
	context.Context
	runtime     *Runtime
	fiber       *Fiber
	scope       *Scope
	transaction *MountTransaction
}
```

Context 只提供运行时操作：

```text
当前 Fiber 身份
当前 Scope 身份
创建 Child Scope / Child Fiber
通过 owner-defined Definition 注册或解析能力
```

Context 不保存 `DB`、`LLM`、`Session`、`Tools` 等业务字段。Scope 只保存 opaque identity、parent lineage 和可见性元数据，不保存 disposer。

### 5.4 Runtime 私有 Effect 与 MountTransaction

Effect 是 Fiber 对 Plugin 激活副作用的统一所有权，但不属于公共 Go extension contract。Runtime 只需要私有 `fiberEffect` 记录 label、state、Scope、可选 registration 和 release seam；不定义公共 `Effect` 或 `Disposer`。

MountTransaction 是命名协调器。它在调用 `Plugin.Apply` 前先压入 Plugin lifecycle effect，其 release 调用同一对象的 `Dispose`；`Apply` 产生的 Service、Waterfall 和 Event registration 在 commit 前保持不可见。提交只原子发布 registration subset，然后把完整 stack 交给 Fiber。

```text
create Fiber + Plugin lifecycle effect
  -> Plugin.Apply
  -> stage registration Effects
  -> validate Manifest and conflicts
  -> atomically publish registrations
  -> Fiber owns the private Effect stack

Apply / validation failure
  -> withdraw any published registration
  -> reverse release registration Effects
  -> Plugin.Dispose partial state
  -> Fiber Failed
```

依赖未满足不进入上述流程，Fiber 保持 Waiting。

### 5.5 Capability：带语义约束的 Service Definition

Capability 是长期、可直接调用的对象角色：

```go
type Capability interface {
	RuntimeCapability()
}

type CapabilityBase struct{}

func (CapabilityBase) RuntimeCapability() {}
```

能力 owner 的 interface 必须嵌入 `plugin.Capability`：

```go
type LiveStore interface {
	plugin.Capability
	Create(context.Context, CreateRequest) (*Session, error)
	Find(SessionID) (*Session, bool)
	List() []*Session
}
```

Provider concrete struct 可以嵌入 `plugin.CapabilityBase`。这个约束的含义是“可注册为 Runtime Service 的长期调用对象”，它排除 DTO、primitive 和裸函数，不是 `any` 的改名。

Service Definition 使用 owner token，而不是 `reflect.Type`：

```go
type ServiceDefinition[C Capability] struct {
	ref ServiceRef
}

func DefineService[C Capability](canonicalName string) ServiceDefinition[C]

func (definition ServiceDefinition[C]) Provide(
	runtimeContext *Context,
	instance C,
) (Registration, error)

func (definition ServiceDefinition[C]) Require(
	runtimeContext *Context,
) (C, error)

func (definition ServiceDefinition[C]) Resolve(
	runtimeContext *Context,
) (C, bool)
```

调用方使用 definition object 的方法，不再调用全局 `Provide[T]`、`Require[T]`、`Resolve[T]`：

```go
modelRuntime, err := llm.Service.Require(runtimeContext)
if err != nil {
	return err
}

registration, err := agentloop.Service.Provide(runtimeContext, loopRuntime)
```

Runtime 的异构目录存 `capabilitySlot` interface；具体值保存在 `capabilitySlotOf[C Capability]`，typed resolve 只允许恢复同一个 owner definition 的 `C`。内部不保存 `any`，也不通过反射调用方法。

### 5.6 硬依赖、可选快照与 Live Reference

三种读取语义必须分开：

```text
Manifest.Requires
  硬依赖；缺失时不 Apply；Provider 消失时停止 Consumer。

ServiceDefinition.Resolve
  当前时刻可选快照；不创建 hard dependency。

ServiceDefinition.Reference
  命名 LiveCapability[C] 对象；用于 source-compatible 的可选 Provider 动态读取。
```

`Require` 只允许在 `Apply` 中读取 Manifest 已声明的 Service。若读取失败，说明 Runtime dependency settlement 不变量已损坏，应返回 invariant error；它不是正常的 `Blocked` 控制流。

当前 `ApprovalResolverFunc`、`AgentRegistryResolverFunc` 等闭包改为 consumer-owned Port，由 `LiveCapability` adapter 实现：

```go
type ApprovalSource interface {
	CurrentApproval() (approval.Approval, bool)
}
```

### 5.7 Event：只传播事实

```go
type RuntimeEvent interface {
	RuntimeEvent()
}

type EventObserver[E RuntimeEvent] interface {
	ObserveEvent(context.Context, E) error
}

type EventDefinition[E RuntimeEvent] struct {
	ref    EventRef
	policy DeliveryPolicy
}
```

Event Definition 提供对象方法：

```go
func (definition EventDefinition[E]) Observe(
	runtimeContext *Context,
	observer EventObserver[E],
) (Registration, error)

func (definition EventDefinition[E]) Publish(
	requestContext context.Context,
	source *Scope,
	fact E,
) error
```

允许的 delivery policy 只有：

- ordered：按注册序通知全部 observer，聚合错误；
- parallel：并行等待全部 observer，聚合错误；
- best-effort：通知全部 observer，错误交给 owner-defined reporter。

Event observer 是命名对象。EventBus snapshot listener 后释放锁，再调用 observer method；绝不在 Registry lock 下执行外部代码。

### 5.8 Hook：决策和 Waterfall

Hook 取代当前 Event mode 中的 serial、bail、waterfall：

```go
type HookInput interface {
	RuntimeHookInput()
}

type HookOutput interface {
	RuntimeHookOutput()
}

type HookOperation[I HookInput, O HookOutput] interface {
	ExecuteHook(context.Context, I) (O, error)
}

type HookChain[I HookInput, O HookOutput] interface {
	Proceed(context.Context, I) (O, error)
}

type HookInterceptor[I HookInput, O HookOutput] interface {
	Intercept(
		context.Context,
		I,
		HookChain[I, O],
	) (O, error)
}

type HookDefinition[I HookInput, O HookOutput] struct {
	ref HookRef
}
```

Hook Definition 提供 `Register` 和 `Run` 方法。Terminal 必须是命名 `HookOperation` 对象，不接受 terminal func；Interceptor 同样是对象。

内部 `hookChainOf[I, O]` 保存 cursor、typed interceptor slice 和 one-shot guard。`Proceed` 第二次调用返回 `ErrHookAlreadyProceeded`。Hook Registry 的异构表保存 `hookBucket` interface，具体 bucket 保持完整的 `I/O` 类型，不保存 `any`。

### 5.9 Event 与 Hook 的迁移分类

| 当前 canonical name | 最终角色 |
| --- | --- |
| `agent/created`、`agent/disposed`、`agent/status`、Inbox changes、`agent/error` | Event fact |
| `agent/pre-step`、`agent/request`、`agent/request-error`、`agent/turn-stopping` | Hook |
| `session/event`、`session/disposed` | Event fact |
| `session/created` | 保留 canonical name，改为拥有 admission terminal 的 `SessionCreated` Hook；不得再以同名 Event 重复发布 |
| `session/flush` | `FlushCoordinator` 对 `FlushParticipant` 的显式能力调用，不是 Event |
| `system-prompt/change` | Event fact |
| `system-prompt/assemble` | Hook |
| `tools/change`、`tools/result` | Event fact |
| `tools/pre-execute`、`tools/execute`、`tools/post-execute` | Hook |
| `approval/request` | Approval decision Hook；`ApprovalService` 主动运行该 Hook 并拥有默认策略 |

canonical 字符串可以继续保留，但它绑定的 Go Definition 类型必须表达正确语义；不得同时注册到 EventBus 和 HookRegistry。

## 6. 各能力包的对象化方案

### 6.1 `agent`

保留 owner：live Agent membership、Inbox、Agent-scoped extension contract。

目标对象：

- `Registry`：只拥有 live membership 和查询；
- `FactoryDirectory`：拥有 concrete Agent Factory 的单一注册；
- `AgentLifecycle`：拥有 exact Agent 的 publication/disposal；
- `AgentSetup`：命名 interface，替代 `SetupFunc`；
- `MaintenanceTask`：保留 interface，删除 `MaintenanceFunc`；
- `ModelSelectionSource`：改为 interface method，删除 function type；
- 每个 Event/Hook 使用命名 observer/interceptor object。

`Registry` 的业务接口不再接收 `*plugin.Scope` 或返回 `plugin.Disposer`。需要 scoped ownership 的注册由 Plugin Context 创建 registration Effect；只有显式 setup 副作用才实现公共 `Effect`。

### 6.2 `agentloop`

Agent Loop 继续是 Agent Turn 的 owner，但把一个大过程拆成有状态协调对象：

```text
AgentSupervisor
  -> TurnRunner
  -> StepRunner
  -> RequestRunner
  -> ToolBatchScheduler
  -> IdleCoordinator
```

主动调用链：

```text
AgentSupervisor.Run
  -> TurnRunner.Run
  -> StepRunner.Run
  -> PreStepHook.Run
  -> RequestRunner.Build
  -> AgentRequestHook.Run
  -> llm.GenerationService.Generate
  -> ToolBatchScheduler.Run
  -> tools.ExecutionService.Execute
  -> SessionAppender.Commit facts
```

`tool_calls.go` 中的 `commitReady`、`startCall`、`fillPool` 迁入 `ToolBatchScheduler` 的私有方法。Scheduler 持有 batch state、并发额度、结果顺序和 cancellation，不再靠 closure 共享局部变量。

### 6.3 `session`

Session 继续拥有 append-only log、seq、surface 和 durable fact semantics。

目标对象：

- `Session`：聚合内存事实与 surface invariant；
- `LiveSessions`：对 Consumer 提供 create/find/list；
- `SessionPublication`：拥有 prepare、enter、运行 `session/created` Hook、commit/rollback 的一次原子流程；
- `PreparedSession`：拥有 exact unpublished Session 和 provider reservation，提供 `Publish` / `Abort`，替代 `Preparation.cleanup func()`；
- `SessionEventDefinition[D SessionEventData]`：替代 `EventKey[D any]`；
- `SessionEventObserver`、`SessionLifecycleObserver`：对象化 observer；
- `FlushCoordinator` 与 `FlushParticipant`：显式 durability barrier。

Session append 调用链：

```text
业务 owner
  -> SessionEventDefinition.Append
  -> Session.commit
  -> assign seq/time + apply surface
  -> release Session lock
  -> publish SessionEventAppended fact
  -> Persistence observer records committed suffix
```

Persisted payload 必须实现 `SessionEventData`。primitive 或匿名 map 不能直接成为 persisted event data，必须使用 owner-defined named struct。

### 6.4 `session/persistence`、`projection`、`title`、`query`

- `SessionLogStore` 保留应用 owner；Backend 仍只做 storage I/O。
- `liveWriter.write/report` 改为 `BatchWriter` 与 `PersistenceFailureReporter` Port。
- `WriteBehindQueue` 独占 batching、timer、flush、close 状态机。
- `DriveRegistry` 保留命名对象；删除 `ChangeListenerFunc`，只接收 `ProjectionObserver`。
- Projection registration 由 Plugin Context 形成 Fiber-owned registration Effect；Projection Registry 不把 Plugin 生命周期类型泄漏到业务接口。
- Session Title 的 stop/report/clock/id 来源改为 interface；一个 title run 由 `TitleGeneration` 对象拥有。
- Query Service 与 SQLite index 保持当前 owner；仅把 Clock/Reporter 等长期 closure 替换为 Port，不把 SQL 的 `...any` 传播出 adapter。

### 6.5 `systemprompt`

拆成三个协作对象：

```text
ContributionRegistry
  拥有 Section、Context、Variable、ToolSchema contribution 和 Scope overlay。

Assembler
  拥有一次 assembly snapshot、排序、解析、渲染和 validation。

AssemblyHook
  拥有 canonical system-prompt/assemble interception。
```

`TextProvider` 可以保留为真实策略 interface，但删除 `TextFunc`。`VariableProvider` 和 `ToolProvider` 从 function type 改为 `VariableSource`、`ToolSchemaSource` interface。静态文本使用已有 `StaticText` 对象；动态来源必须是命名 provider。

`SystemPrompt` 大接口拆为 Consumer 实际需要的 `PromptAssembler` 与扩展 Plugin 需要的 `PromptContributions`，避免普通 Agent Loop Consumer 获得 mutation API。

### 6.6 `tools`

拆分当前混合 Runtime：

```text
DefinitionRegistry
  Tool Definition、Scope visibility、restriction、guard registration。

ExecutionService
  单个 Tool call 的 admission、timeout、invoke、result commit。

ExecutionScheduler
  sibling call 并发和 exclusive barrier。

ResultAssembler
  output validation、render、presentation、final content。
```

`Executor`、`OutputRenderer`、`PresentationProjector`、`ContentFinalizer`、`ConcurrencyClassifier`、`ToolGuard` 可以保留为 interface，因为它们表达真实策略；删除所有对应 `XxxFunc` adapter。`toolaskuser` 必须提供 `AskUserExecutor` 和 `AskUserRenderer` 命名 struct。

Tool 执行主动调用链：

```text
AgentLoop.ToolBatchScheduler
  -> tools.ExecutionScheduler
  -> tools.ExecutionService.Execute
  -> PreExecuteHook
  -> ExecuteHook
  -> Tool Executor object
  -> ResultAssembler
  -> PostExecuteHook
  -> Session tool/result append
  -> ToolsResult Event
```

### 6.7 `approval`、`userquestions`、`llmretry`

- `approval.Service` 继续拥有 policy 与 audit；`NewRequestID func` 改为 `RequestIDGenerator`；request decision 使用命名 interceptor/provider。
- `userquestions.QuestionService` 负责 validation 和调用当前 Provider；`ProviderDirectory` 负责唯一 provider registration；`AgentRegistryResolverFunc` 改为 `AgentDirectorySource`。
- `llmretry.RetryCoordinator` 负责一次 failed attempt 的 policy、history、delay、cancel 和 retry event；Random、RetryID、FailureReporter 全部是 consumer-owned interface。

### 6.8 `llm` 与 `llm/deepseek`

LLM 继续拥有所有已实现 adapter 的注册与路由，`Model.API`/provider route 选择实现；其他领域不得操作 Registry。

目标对象：

- `ProviderDirectory`：Provider metadata；
- `AdapterRegistry`：adapter route contribution；
- `GenerationService`：prepare/stream/assemble；
- `ModelDiscovery` interface，删除 `ModelDiscoveryFunc`；
- `ObserverFailureReporter` 改为 interface；
- DeepSeek adapter 依赖 `ConnectionOptionsSource`、`CredentialSource`、`UserIdentitySource`、`Clock` interface。

`internal/assembly/llm_deepseek.go` 中的 lazy identity closure 迁入 `anonymoususerid.Source` 命名对象；凭证解析迁入 consumer-side `DeepSeekCredentialSource` adapter。

### 6.9 `apiproxy` 与 `internal/connection`

API Proxy 的 transport-neutral 层不得以 callback 表示 route、pending response 或 stream。

目标对象：

- `MethodCatalog` 保存 `UnaryMethod` object；每个 method object 拥有 decode、gateway invocation、outcome encode；
- request/response 泛型分别约束为 `RequestPayload` 和 `ResponsePayload`；
- `PendingCallRegistry` 保存 `PendingCall` interface；typed pending call 用 channel 和状态对象结算，不保存 `decode/complete any` callback；
- `DownlinkHub` 保存 Subscriber 与 `FrameQueue[F Frame]`；
- `MuxStream`、`HostStream` 实现 pull-based `Next(context.Context)` 与 `Close`；
- `InteractionCoordinator` 拥有 approval/question correlation 和 settlement；
- RPC ID 来源与 observer reporter 是 interface。

最终流调用链：

```text
internal/connection WebSocket bridge
  -> apiProxy.EventSource.OpenMux / OpenHost
  -> DownlinkStream.Next
  -> encode RPCRequest
  -> websocket write
```

Echo HandlerFunc、WebSocket read/write callback 只存在于 `internal/connection`，不进入 API Proxy 或业务能力 contract。

### 6.10 `workspace`、`credentials` 与 `internal/assembly`

- Workspace Registry 的业务对象可以保留；Clock、WorkspaceIDGenerator、FailureReporter 改为 interface。
- Credentials Manager 继续拥有 env precedence 和 mutation；LiveStore 只存储。`LookupEnv func` 改为 `Environment` interface。
- `internal/assembly` 只构造 concrete objects、连接 Provider/Consumer、注册 Factory，不实现业务分支。
- 每个 assembly Plugin 的 `Apply` 先解析全部 required capability，再把它们传给 owner constructor；复杂装配由命名 `xxxAssembly` 对象完成，不在一个方法中堆积闭包。
- Factory 和 Plugin struct 不保存 env/filesystem 函数字段，改存 `Environment`、`DirectoryProvisioner`、`FailureReporter` 等对象。

## 7. 目标主流程

### 7.1 Plugin 激活

```text
Runtime.Load 主动调用 FiberSupervisor.Declare
  -> Supervisor 检查 Manifest.Requires
  -> 缺失：Fiber Waiting，不调用 Plugin.Apply
  -> 齐全：创建 Fiber + Context + MountTransaction
  -> Supervisor 调用 Plugin.Apply
  -> Plugin 构造命名能力对象、Observer、Interceptor 和显式 Effect setup 对象
  -> Transaction 验证并提交
  -> CapabilityDirectory 发布 Service
  -> Fiber Active
  -> Supervisor 结算后续 Waiting Fiber
```

### 7.2 Provider 消失与恢复

```text
Provider contribution withdrawal
  -> CapabilityDirectory 标记 unavailable
  -> DependencyGraph 找到直接和传递 Consumer
  -> FiberSupervisor 按 dependent-first 停止 Consumer
  -> Consumer 回到 Waiting
  -> Provider Fiber Dispose

新 Provider commit
  -> CapabilityDirectory 发布同一 Service Definition
  -> Supervisor 重新结算 Waiting Consumer
  -> 为每个 Consumer 创建全新的 MountTransaction
  -> 再次 Plugin.Apply
```

这里的重新 `Apply` 是一次新的完整激活，不是恢复旧调用栈，也不是从上一次 `Require` 后继续。

### 7.3 Agent 请求

```text
API Gateway 调用 Agent.Send
  -> AgentSupervisor 唤醒 Agent
  -> TurnRunner claim Inbox
  -> StepRunner 执行 PreStepHook
  -> RequestRunner 重建 Session messages
  -> RequestHook 生成 immutable CallConfig
  -> LLM GenerationService 调用 selected Adapter
  -> ToolBatchScheduler 调用 Tools
  -> Session 提交 facts
  -> committed facts 经 EventBus 通知 Projection/Persistence/API frames
```

谁主动调用谁在每一层都可从对象方法看出；Plugin 只在注册期把扩展对象挂到稳定 seam，不接管 Agent 主循环。

## 8. 一次性重构与删除范围

### 8.1 原子切换规则

本次重构采用“分支内破坏、领域内闭环、主线原子合入”的执行模型。

允许在专用重构分支中先直接替换 `plugin` 公共契约，因此尚未迁移的业务包可以暂时无法编译。这个破坏窗口是有边界的：

- 每个工作波次结束时，本波次 owner 及其已经迁移的依赖必须可编译、可运行测试；
- 已完成 owner 内不得保留旧接口、兼容 wrapper、双写、双 Registry 或新旧 Runtime 分支；
- `go test ./...` 在中间波次是剩余破坏面的诊断命令，不是阶段通过门槛；其失败必须只来自尚未迁移的已知调用者，不能掩盖已迁移 owner 的回归；
- 任一中间波次都不得单独合入主线或发布；只有所有调用者、装配和协议测试完成迁移并恢复全仓绿色后，整个 cutover 才能合入；
- 重构分支可以保留可回退的波次 checkpoint commit；最终以一个完整 PR/merge 进入主线。是否 squash 不影响原子切换语义，但主线不能出现不可编译中间态；
- 文档作为独立提交同步 `02`、`08`、`09` 及所有实际受影响模块的 `README.zh-CN.md`。

这里的“领域内可编译”指 owner 对应的 Go package subtree，例如 Plugin Runtime 的阶段门槛是 `go test ./plugin/...`，不是另建 `go.mod`。已经完成的 package subtree 必须持续保持绿色。

### 8.2 重构波次与阶段出口

迁移顺序按当前直接 import 和运行时装配依赖自底向上确定：

```text
plugin -> credentials
plugin -> llm -> session

session -> session/persistence
session -> session/projection -> session/title
session + session/persistence + session/title -> session/query
session -> workspace
llm + session -> systemprompt

llm + session + systemprompt -> agent
agent -> agentdefaultmodel
agent + llm + session + systemprompt -> approval
agent -> userquestions
agent + llm + session -> llmretry
agent + approval + llm + systemprompt -> tools
tools + userquestions -> toolaskuser
agent + llm + session + session/persistence + systemprompt + tools -> agentloop

上述稳定能力 -> apiproxy + apiproxy/session
上述全部模块 -> internal/assembly -> cmd/goren
```

图只表达迁移约束，不改变既有领域 ownership。横向分支在依赖满足后可以并行推进；`internal/assembly` 始终最后接线。

#### 波次 0：冻结语义和保存基线

开始破坏性修改前：

1. 保存当前可工作的 Git checkpoint；
2. 运行现有全仓测试、race、vet 和 build，记录真实失败而不是假定基线绿色；
3. 补齐 Plugin dependency、replacement、rollback、Scope admission、listener ordering 和 service identity characterization tests；
4. 固定必须删除的旧 symbol 清单，以及 HTTP、WebSocket、Session event、SQLite schema 和 canonical name 不得变化的清单。

阶段出口是当前行为有可执行证据，且后续失败可以区分为“预期 API 破坏”或“非预期语义回归”。

#### 波次 1：只重构 `plugin` 底座

一次完成 `plugin/` 内部重构：

1. 落定 `plugin` 根包与子包的最终结构，不创建顶层兄弟领域；
2. 建立 Plugin、Factory、FiberSupervisor、Fiber、Context、Scope、Effect/Disposer、MountTransaction 和 diagnostics 最终对象；
3. 分离 Capability、Event 与 Hook Registry，并使用带业务语义的泛型约束；
4. 实现 dependency waiting、失败回滚、replacement、dependent-first stop、child lifecycle 和确定性 dispatch；
5. 直接删除旧 `Factory[C any]`、`ServiceKey[T any]`、多 mode `EventKey`、函数式 Effect setup 和 callback Runtime；保留静态 `Disposer` 生命周期契约，不建立 `v2` 或兼容层；
6. 重写 `plugin` 自身测试，使测试只依赖标准库和 `plugin/...`，不得反向 import Agent、Session、Tools 或 LLM。

阶段门槛：

```text
go test ./plugin/...
go test -race ./plugin/...
go vet ./plugin/...
go build ./plugin/...
```

此时全仓其他包大面积编译失败是预期状态，但 `plugin/...` 必须独立绿色。全仓诊断结果必须能够归因到旧 Plugin API 的未迁移调用点。

#### 波次 2：基础能力 owner

按 `credentials -> llm -> session` 顺序迁移。每个 owner 在适配新 Plugin 契约的同时完成自身对象化，不先写临时 adapter、以后再重构第二次：

- `credentials`：保留 Manager/LiveStore ownership，删除长期函数字段；
- `llm`：完成 Adapter、ProviderDirectory、GenerationService、stream/result 对象边界，并同步 `llm/deepseek`；
- `session`：完成 Session、Store、publication、append、flush 和 stream object 边界。

每完成一个 owner，运行该 owner 的 focused test、race、vet 和 build，并重复运行所有先前已绿色 owner。阶段结束时至少保证：

```text
go test ./plugin/... ./credentials/... ./llm/... ./session
```

#### 波次 3：Session 周边和共享能力

在 `session` 核心稳定后迁移：

1. `session/persistence` 与 SQLite adapter；
2. `session/projection`；
3. `session/title`；
4. `session/query` 与 SQLite adapter；
5. `workspace` 与其 persistence adapter；
6. `systemprompt`。

每个包在迁移 Plugin API 时同步删除 Registry callback、Clock/ID/Reporter 函数字段和裸 cleanup，保留 Session 事实、durability barrier 与 SQL adapter ownership。阶段出口是上述 subtree 及波次 1、2 全部绿色。

#### 波次 4：Agent 主流程和交互能力

按依赖门槛迁移：

```text
agent
  ├── agentdefaultmodel
  ├── approval
  ├── userquestions
  └── llmretry

approval -> tools
tools + userquestions -> toolaskuser
agent + tools + 已稳定基础能力 -> agentloop
```

每个 owner 必须在一个波次内同时完成新 Capability/Event/Hook 接入和自身 callback 对象化。AgentLoop 只在所有下游能力已经稳定后迁移，不能临时承担 Approval、Retry、Tools 或 Session 的业务决策。

阶段出口是这些包与全部下游依赖的 focused test、race、vet 和 build 通过，Agent turn/step/request/tool/retry characterization tests 恢复编译并通过。

#### 波次 5：API、Connection 和装配

最后迁移最外层调用者：

1. `apiproxy` 与 `apiproxy/session` 的 Gateway、pending request 和 downlink stream 对象；
2. `connection` 与 `internal/connection` 的 transport adapter；
3. `internal/assembly` 的 Factory 注册、Provider 选择和完整装配；
4. `cmd/goren` 启动入口与 `web` 主会话集成。

`internal/assembly` 只负责创建 concrete adapter 和接线，不保留任何 `XxxFunc` 作为迁移逃生口。完成本波次后才要求 `go test ./...` 重新成为强制门槛。

#### 波次 6：删除证明与全仓恢复

1. 用 AST 检查和 symbol 搜索证明旧 API、旧 Func adapter、`any` 泛型和双路径已经消失；
2. 运行第 9 节全部验证，包括 contract、golden、cold recovery、keyless 和 Web build；
3. 更新权威设计、实施证据和各实际 package README；
4. 复核 Git diff，确认没有过渡代码、占位目录、协议漂移或与本次重构无关的修改；
5. 只有代码和文档各自形成完整可审查提交且全仓绿色后，才允许合入主线。

### 8.3 必须删除的旧形态

- 旧 `Disposer func(...)` alias、`EffectFunc` setup 和领域返回的裸 cleanup func；目标 `Disposer` interface 保留；
- `Factory[C any]`、`ServiceKey[T any]`、`EventKey[P, R any]`；
- 全局 `Provide[T]`、`Require[T]`、`Resolve[T]`；
- `NotifyHandler`、`DecisionHandler`、`WaterfallHandler`、`Next` 等函数类型；
- 领域包中的全部 `XxxFunc` adapter；
- exported Options/Dependencies 的 Clock、ID、Resolver、Reporter、Source 函数字段；
- `pendingEntry.decode/complete` 和 EventStreams handler callback；
- `ModeSerial`、`ModeBail`、`ModeWaterfall` 作为 Event mode；
- `LayerStore[K comparable, V any]`；三个 Registry 必须各自拥有不同的 Scope 语义；
- 只为兼容旧 API 而存在的 wrapper、alias、deprecated branch。

### 8.4 不允许的“伪重构”

以下做法不算完成：

- 把 `any` 改成 `Value interface{}`，但仍允许任意值；
- 给 closure 外面套一个只有 `Run()` 的匿名 adapter，却继续让状态由外部变量持有；
- 在 `plugin` 外新建顶层 `runtime`，或新建 `plugin/runtime/v2` 而让旧 `plugin` 继续运行；
- EventBus 和 HookRegistry 同时监听同一个 canonical seam；
- 保留 `XxxFunc` 供 assembly 使用；
- 只拆文件，不改变对象 owner 和调用方向；
- 通过新的 service locator 隐藏依赖。

## 9. 验证与完成定义

### 9.1 架构检查

在 `tests/architecture/` 增加 AST 检查：

- `plugin` 和业务能力包不得声明以 `any` 为约束的泛型；
- Plugin Runtime 的 Fiber、Scope、Capability、Event、Hook 和 diagnostics 实现不得落在与 `plugin` 平级的仓库根包；
- 除 allowlist adapter 外不得声明 `type XxxFunc func(...)`；
- exported Config/Options/Dependencies/Service struct 不得含函数字段；
- Capability interface 的普通业务方法不得接收 `*plugin.Scope` 或返回裸 disposer；
- Event Definition 不得有返回决策值；
- Hook input/output 必须满足相应语义 interface；
- `internal/assembly` 以外不得创建 concrete adapter；
- 旧 API symbol 必须完全消失；
- keyed struct literal 继续保持每行一个 field。

技术 allowlist 只能包含：

- `internal/connection` 的 Echo/coder websocket handler；
- SQLite adapter 为满足 `database/sql` 使用的 `...any`；
- 标准库定义的 `context.CancelFunc`；
- 方法内部不逃逸、不存储的局部函数。

### 9.2 Runtime 行为

必须覆盖：

- Manifest dependency 缺失时不调用 Apply；
- Apply/setup 失败零残留、统一 Effect stack 逆序回收；
- Provider 消失 dependent-first stop；
- Provider 恢复重新 Apply；
- replacement candidate 失败不影响 active old Fiber；
- Child Fiber 不超过 Parent 生命周期；
- Scope capability nearest-wins；
- Event current-to-ancestor admission；
- Hook root-to-current accumulation和确定性顺序；
- Hook Chain one-shot；
- Registry lock 外执行 Observer/Interceptor；
- shutdown、cancel、worker wait 和 disposer 幂等。

### 9.3 领域流程

必须为以下对象链提供直接测试：

- Agent Turn/Step/Request/Tool batch；
- Session publication、append、flush、cold recovery；
- System Prompt registry/assembly/hook；
- Tool registry/policy/execution/result；
- LLM provider routing、retry、stream assembly；
- Approval/UserQuestions settlement；
- API method dispatch、pending response、downlink reconnect；
- Workspace/Credentials lifecycle。

### 9.4 兼容与全仓验证

最终执行：

```text
go mod tidy
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
cd web && pnpm install --frozen-lockfile
cd web && pnpm run build
```

还必须保留并通过：

- 固定 TypeScript Client 到 Go HTTP/WebSocket 的 contract tests；
- TypeScript-to-Go golden fixtures；
- Session SQLite cold recovery；
- DeepSeek keyless recording 与 real-provider 自跳过 smoke；
- Web 主会话 create/list/history/prompt/respond/reconnect 主流程。

完成声明必须区分：实现完成、Go/race 验证、跨语言 contract 验证和真实环境验证。

## 10. 兼容影响

### 10.1 有意破坏的 Go 扩展 API

本次会破坏当前 Go public extension API，包括 Service/Event/Hook 注册、Factory、Effect、Scope ownership 和各领域 Func adapter。由于用户明确要求不保留过渡代码，调用者必须在同一次变更中迁移。

### 10.2 保持不变的外部契约

- TypeScript Client wire contract；
- `/api`、Mux WebSocket、Host WebSocket；
- RPC correlation、cancel、respond、receipt；
- canonical Service/Event/config 名；
- Session Event envelope、seq、surface 与 SQLite schema；
- LLM Provider/Model 路由和 DeepSeek HTTP/SSE 映射；
- Tool schema、result、Approval 和 Question wire shape。

### 10.3 文档归档

代码重构完成后：

1. 把 Runtime 决策合入 `02` 和 `09`；
2. 把各 owner 对象和调用链合入对应的 `10`–`23` 文档与 package `README.zh-CN.md`；
3. 在 `08` 更新实际证据和验证等级；
4. 删除本文或明确标记为已完成迁移记录，避免形成第二份长期权威设计。

## 11. 最终模型

```text
Plugin Definition
  -> FiberSupervisor 创建 Fiber
  -> Manifest 建立硬依赖
  -> Context + MountTransaction 安装命名对象
  -> ServiceDefinition 发布 Capability
  -> Consumer 解析一次后直接调用 interface
  -> Application Service / Coordinator 拥有显式 Workflow
  -> HookDefinition 在明确动作节点组合 Interceptor object
  -> Domain/Session 状态提交
  -> EventDefinition 发布已发生事实给 Observer object
  -> EffectStack 统一回收 Registration、Worker、Server 和 Adapter resource
```

一句话总结：

> **Plugin 负责系统组成，Fiber 负责运行实例与生命周期，Scope 负责可见性，Capability 负责稳定同步依赖，Hook 负责可拦截动作，Event 负责已提交事实，命名领域对象负责业务主流程；泛型只接受具有明确角色语义的类型，长期职责不再由回调和闭包承载。**
