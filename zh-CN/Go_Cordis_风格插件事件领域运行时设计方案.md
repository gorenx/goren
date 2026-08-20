# Go Cordis 风格通用 Plugin 事件领域运行时设计方案

状态：Plugin 核心重构已实现，其他领域迁移尚未开始

## 1. 系统定位

本框架是一个面向 Go 服务开发的静态 Plugin Runtime：

    Plugin Runtime
      = Plugin 生命周期
      + typed Service 依赖
      + scoped 可见性
      + Waterfall 洋葱扩展
      + typed Event 事实分发
      + 可逆运行时贡献

DeepSeek Harness 和 Cordis 只用于核对责任与运行语义，不作为 Go API 和目录结构模板。Go 实现必须利用静态类型、接口和显式对象方法，不能翻译 TypeScript 的 Context 链、对象字面量服务或函数工厂。

Goren 是该框架的首个使用者，但 plugin 包不能依赖 Agent、Session、LLM、Tools、HTTP、数据库等业务或基础设施实现。

## 2. 设计目标

### 2.1 Plugin 就是服务模块对象

一个对象可以同时实现：

- Plugin 生命周期；
- 一个或多个业务 Service interface；
- EventObserver；
- WaterfallMiddleware。

不为同一个对象额外创建 ProviderPlugin、ModulePlugin、DefinePlugin 或只负责转发的包装类型。

### 2.2 业务主流程保持显式

Service 方法和应用 Workflow 仍然拥有主调用链。Waterfall 只包裹 owner 明确开放的动作，Event 只传播 owner 已经提交的事实。

### 2.3 Runtime 独占运行时状态

Fiber、Scope、依赖快照、Registry、Effect stack 和 contribution publication 全部由 Runtime 管理。Service 对象不接收或保存公开的 Plugin Context、Scope、Registration 或 disposer。

### 2.4 Go 静态类型安全

Service 使用具备业务含义的 interface；Event、Waterfall input/output 使用命名类型。公共泛型约束必须是 Service、Event、WaterfallInput、WaterfallOutput，不能使用 any 或空 interface。

### 2.5 失败可回滚

启动失败必须撤销本次批次已经激活的 Plugin。每个 Fiber 停止时先隐藏其 Runtime 贡献，再调用 Plugin.Dispose，不能留下孤儿 listener、middleware、service binding 或后台任务。

## 3. 非目标

首版不负责：

- Go 标准库 plugin、动态 so、WASM 或远程代码加载；
- Cordis Profile、JavaScript 配置求值或表达式插值；
- Event Store、消息队列、跨进程事件和 Event Sourcing 基础设施；
- 自动拦截任意 Go 方法；
- 通过字符串或任意类型实现全局 Service Locator；
- 为旧 Plugin API 保留兼容层；
- 把 Catalog、配置读取和 Plugin 实例化塞进 Runtime。

## 4. 统一术语

| 术语 | 含义 | 不负责 |
| --- | --- | --- |
| Runtime | Plugin 生命周期与贡献协调入口 | 配置读取、业务 Workflow |
| Plugin | 一个已构造的服务模块实例 | Runtime Registry |
| Manifest | Plugin 的完整静态贡献和依赖声明 | I/O、动态查找 |
| Base | Plugin 内嵌的私有激活锚点 | 业务状态、公开 Context |
| Fiber | Plugin 的一次激活实例 | 领域状态机 |
| Scope | Service、Event、Waterfall 的可见性边界 | 业务租户模型 |
| Effect | Runtime 私有的逆序清理记录 | 插件作者 API |
| Service | Plugin 提供的 typed 同步业务能力 | 远程 RPC |
| Provider | 实现并声明提供 Service 的 Plugin | Consumer Workflow |
| Dependency | Manifest 声明的 required 或 optional Service | 任意运行时查找 |
| Waterfall | owner 主动运行的 typed 洋葱扩展链 | 事实通知 |
| Middleware | Waterfall 中的命名对象 | 主业务 owner |
| Action | 一个可执行的下游动作；可以是 Runtime chain step 或最内层业务动作 | Middleware 策略 |
| Event | owner 提交后发布的 typed fact | 历史存储、前置决策 |
| Observer | 通过 Manifest 监听一个或多个 Event 类型的 Plugin | Event 持久化 |
| Catalog | 已编译 Factory 的名称索引 | Plugin 启动 |
| Factory | 拥有一个 Plugin 的具名配置、严格解码、校验和构造 | Runtime mount |

不再使用 Service Definition、Event Definition、Waterfall Definition、Registration、Consumer handle 和 Plugin Context 等术语。

## 5. 总体责任

### 5.1 Plugin

Plugin 只负责：

- 返回确定、无 I/O 的 Manifest；
- 在 Apply 中取得已声明的依赖并启动自身资源；
- 在 Dispose 中幂等关闭自身资源；
- 通过自己的业务方法执行 Workflow；
- 在逻辑提交后主动 Publish Event；
- 在明确扩展点主动 Run Waterfall。

Plugin 不负责注册和撤销 Service、Observer、Middleware。Runtime 根据 Manifest 自动完成。

### 5.2 Runtime

Runtime 负责：

- 批量接纳并校验 Manifest；
- 校验 Plugin 是否实现声明的 Service 和统一 EventObserver，以及 Waterfall contribution 是否持有有效 Middleware；
- 建立 Scope 与 Service 依赖关系；
- 按依赖拓扑激活 Fiber；
- 自动发布和撤销 Runtime 贡献；
- 管理 parent/child 生命周期；
- dependent-first stop；
- replacement；
- diagnostics；
- 逆序回滚和错误聚合。

Runtime 不读取配置、不访问 Catalog、不构造业务 Plugin，也不调用普通 Service 业务方法。

### 5.3 Catalog 与 Factory

配置来源、Catalog 和 Runtime 是三条独立责任链：

    configuration source
      -> Catalog.Lookup
      -> Factory.Create(raw JSON)
      -> Plugin instance
      -> Runtime.Start

Catalog 只负责 Factory 名称唯一性和查找。每个 Factory 拥有自己的具名 Config，负责严格解码、默认值、校验和 Plugin 构造；原始配置只能存在于配置入口和 Factory 构造边界。Plugin 只接收已校验的具名配置，Runtime 只接收已经构造完成的 Plugin。

    type Factory interface {
        Name() string
        Create(context.Context, json.RawMessage) (Plugin, error)
    }

    func NewCatalog() *Catalog
    func (*Catalog) Register(Factory) error
    func (*Catalog) Lookup(string) (Factory, error)
    func (*Catalog) Names() []string

## 6. 公共代码契约

### 6.1 Plugin 与 Base

    type Plugin interface {
        RuntimePlugin() *Base
        Manifest() Manifest
        Apply(context.Context) error
        Dispose(context.Context) error
    }

    type Base struct {
        // private activation binding
    }

每个 Plugin 以指针对象嵌入 Base。Base 只保存 Runtime 私有绑定，不公开 Fiber、Scope 或 Registry。

Apply 的 context.Context 只描述本次启动调用。长期后台任务使用：

    plugin.Lifetime(pluginInstance)

Runtime 在 Dispose 前取消 lifetime。

### 6.2 Manifest

    type Manifest struct {
        Name       string
        Provides   []ServiceType
        Requires   []ServiceType
        Optional   []ServiceType
        Events     []EventSubscription
        Waterfalls []WaterfallContribution
    }

Manifest 是完整声明：

- Provides：Runtime 自动把 Plugin 对象作为 Service Provider；
- Requires：Provider 不可用时阻止 Apply；
- Optional：Apply 时取得可见 Provider snapshot，不阻止启动；
- Events：Runtime 把每个声明的 Event 类型绑定到同一个 Plugin 级 EventObserver 入口；
- Waterfalls：Runtime 绑定 Plugin 在 contribution 中明确给出的 WaterfallMiddleware 对象。

Manifest 返回后，Plugin 不再执行 Provide、Observe、Use 等注册动作。

### 6.3 类型身份

    plugin.ServiceOf[Clock]()
    plugin.EventOf[CounterAdvanced]()
    plugin.WaterfallOf[Request, Response](middleware)

ServiceOf 和 EventOf 创建类型描述；WaterfallOf 同时携带 Plugin 拥有的 Middleware 对象。它们都不创建 Runtime Definition，也不持有注册状态。

内部使用 reflect.TypeFor[T] 建立 Go 类型身份，原因是删除全局 Definition 后，Provider 和 Consumer 只能通过共同的业务类型取得同一个键。反射仅用于类型键和校验：

- 不使用 reflect.Value 调用业务方法；
- 不通过反射创建 Plugin；
- 不通过反射解码业务值；
- Registry 中的值仍是 Service、Event 和 typed adapter。

各机制使用独立私有键：

    serviceKey   = Service interface type
    eventKey     = Event struct type
    waterfallKey = input type + output type

### 6.4 Service

业务 Service interface 嵌入 plugin.Service：

    type Clock interface {
        plugin.Service
        Now() time.Time
    }

Provider 对象直接实现 Clock 和 Plugin：

    type ClockPlugin struct {
        plugin.Base
    }

    func (*ClockPlugin) Manifest() plugin.Manifest {
        return plugin.Manifest{
            Name: "clock",
            Provides: []plugin.ServiceType{
                plugin.ServiceOf[Clock](),
            },
        }
    }

依赖方必须先声明 Requires，再在 Apply 中获取：

    func (serviceOwner *Scheduler) Apply(context.Context) error {
        clock, err := plugin.Require[Clock](serviceOwner)
        if err != nil {
            return err
        }
        serviceOwner.clock = clock
        return nil
    }

Require 不是任意 Service Locator：

- owner 必须是当前正在 Apply 的 Plugin；
- Service 必须存在于 Manifest.Requires；
- 结果来自 Runtime 已解析的当前 Fiber dependency snapshot；
- Apply 返回后 Require 关闭；
- 普通业务调用直接进入保存的 Go interface，不经过 Runtime。

Optional 使用 plugin.Resolve[S]，且只能解析 Manifest.Optional。

### 6.5 Event

Event 自己提供稳定名称和投递策略：

    type CounterAdvanced struct {
        Value int
    }

    func (CounterAdvanced) EventName() string {
        return "counter/advanced"
    }

    func (CounterAdvanced) EventDelivery() plugin.DeliveryPolicy {
        return plugin.DeliveryOrdered
    }

Observer Plugin 只实现一个统一入口：

    type EventObserver interface {
        ObserveEvent(context.Context, Event) error
    }

一个 Plugin 可以在 Manifest.Events 中声明多个 `EventOf[E]()`。Runtime 校验 Plugin 实现了 EventObserver，再把每个声明类型绑定到同一入口；未声明的 Event 不会送达，同一 Plugin 重复声明同一 Event 类型会在 admission 时失败。

Plugin 在统一入口中只做类型分派，真实业务处理保持为具名方法：

    func (observer *ProjectionPlugin) ObserveEvent(
        requestContext context.Context,
        fact plugin.Event,
    ) error {
        switch typedFact := fact.(type) {
        case CounterAdvanced:
            return observer.onCounterAdvanced(requestContext, typedFact)
        case CounterReset:
            return observer.onCounterReset(requestContext, typedFact)
        default:
            return fmt.Errorf("unsupported Event %q", fact.EventName())
        }
    }

事实 owner 在逻辑提交后发布：

    plugin.Publish(requestContext, serviceOwner, CounterAdvanced{
        Value: value,
    })

Event 路由从 source Scope 到 root Scope。一次发布先取得 Observer 快照，再在 Runtime 锁外调用 Observer。

DeliveryOrdered 顺序调用并返回首个错误；DeliveryParallel 并行调用并聚合错误；DeliveryBestEffort 并行调用、通过 EventFailureReporter 报告失败并向发布者返回成功。声明 BestEffort Observer 的 Runtime 必须配置 reporter，否则 Plugin admission 失败，不能静默丢失错误。

### 6.6 Waterfall

    type WaterfallAction[I WaterfallInput, O WaterfallOutput] interface {
        Execute(context.Context, I) (O, error)
    }

    type WaterfallMiddleware[I WaterfallInput, O WaterfallOutput] interface {
        Intercept(context.Context, I, WaterfallAction[I, O]) (O, error)
    }

Plugin 在 Manifest.Waterfalls 声明 `WaterfallOf[I, O](middleware)`，Runtime 绑定该 Plugin 拥有的具名 Middleware 对象。Middleware 可以就是 Plugin 本身，也可以是 Plugin 内部的独立策略对象。动作 owner 主动调用：

    plugin.Run(requestContext, serviceOwner, input, terminal)

Waterfall 从 root Scope 到 source Scope 组装。业务最内层动作与 Runtime 生成的 chain step 实现同一个 WaterfallAction，不为运行位置增加 Terminal 或 Next 接口。每层可以改写 input/output、短路或传播 error；每个 Runtime chain step 只能 Execute 一次。

## 7. 核心流程

### 7.1 Start

    Runtime.Start(all static Plugins)
      -> normalize every Manifest
      -> validate declared interfaces
      -> reject exact-Scope Service conflicts
      -> admit the whole batch
      -> resolve required Service graph
      -> activate providers before dependents
      -> return only when every Plugin is Active

调用方不需要手工排列 Provider 和依赖方顺序。

### 7.2 Fiber 激活

    create Fiber
      -> attach private Base activation
      -> resolve required and optional dependency snapshot
      -> register Plugin Dispose as first private Effect
      -> state = Starting
      -> call Plugin.Apply outside Runtime state lock
      -> validate contribution publication
      -> publish Service/Event/Waterfall atomically
      -> state = Active

Starting Plugin 允许 Require/Resolve 和 Lifetime，不允许 Publish、Run 或作为可见 Provider。

### 7.3 启动失败

    Apply or publication fails
      -> state = RollingBack
      -> cancel lifetime
      -> withdraw staged or published contributions
      -> call Dispose
      -> detach Base
      -> state = Failed

Runtime.Start 的任一 Plugin 失败时，整个静态批次按依赖安全顺序回滚。回滚完成后可以重新调用 Start。

### 7.4 停止

    Unload or Shutdown
      -> find hard and resolved optional dependents
      -> stop dependents and child Fibers first
      -> state = Stopping
      -> cancel lifetime
      -> reverse release Effect stack
           -> Waterfall
           -> Event
           -> Service
           -> Plugin.Dispose
      -> detach Base
      -> state = Stopped

Handle 不提供 Stop 方法。生命周期改变统一由 Runtime.Unload、Runtime.Replace 和 Runtime.Shutdown 发起。

### 7.5 Scope

| 机制 | 方向 | 语义 |
| --- | --- | --- |
| Service | source → root | 最近 Provider 覆盖祖先 |
| Event | source → root | 局部 Observer 先于祖先 |
| Waterfall | root → source | 外层默认策略包裹局部策略 |

Runtime.Start 创建 root Plugin。Runtime.MountChild(parentHandle, child) 创建一个真实 Child Fiber 和 Child Scope。Service 不保存 Scope，也不关心自己所在的 Scope。

### 7.6 Replacement

Replacement 必须保持完整 Manifest contract：

- Plugin name；
- Provides、Requires、Optional；
- Event subscriptions；
- Waterfall contributions。

Runtime 先在旧 Plugin 仍 Active 时准备候选 Plugin。候选 Apply 成功后，Runtime 停止依赖方，原子替换 Registry contribution，再 Dispose 旧 Plugin，并重新激活依赖方。候选失败时旧 Plugin 保持 Active。

## 8. Waterfall、业务提交与 Event

标准业务顺序：

    request
      -> Service method
      -> Run Waterfall
      -> innermost Action executes owner Workflow
      -> owner-defined logical commit
      -> Publish typed Event

Waterfall 发生在动作完成前，可以拒绝、短路和改写结果。Event 发生在逻辑提交后，只通知事实。

Event Sourcing 是业务 owner 的状态存储方式，不是第二类 Runtime Event：

    append owner event to authoritative log
      -> logical commit
      -> Publish the same typed fact when appropriate
      -> owner-defined flush if durability is required

Plugin Runtime 不保存 Event，不提供 replay、projection 或 flush。

## 9. 模块内部组织

plugin 是一个模块，不把核心责任拆成与 plugin 平级的目录：

    plugin/
      types.go
      runtime.go
      fiber.go
      scope.go
      effect.go
      service.go
      event.go
      waterfall.go
      factory/
      example/

核心 Runtime、Fiber、Scope、Effect 和 Registry 保留在同一个 plugin 包内，只按文件划分职责。factory 因为是配置解码和实例构造边界而作为子包；example 只展示 plugin 公共 API。

## 10. 如何实现一个 Plugin

### 10.1 必须实现

每个 Plugin：

1. 嵌入 plugin.Base；
2. 实现 Manifest；
3. 实现 Apply；
4. 实现幂等 Dispose。

### 10.2 按需实现

- 提供 Service：实现 owner-defined Service interface，并声明 Provides；
- 依赖 Service：声明 Requires/Optional，在 Apply 中 Require/Resolve；
- 监听 Event：实现统一 EventObserver，并通过多个 EventOf[E]() 声明接受的 Events；
- 扩展 Waterfall：实现或持有 WaterfallMiddleware[I, O]，并通过 WaterfallOf[I, O](middleware) 声明 Waterfalls；
- 发布 Event：在 owner-defined commit 后调用 Publish；
- 运行 Waterfall：在明确扩展点调用 Run；
- 子生命周期：由 composition root 使用 MountChild。

插件不需要实现与自身无关的接口。

### 10.3 配置

Plugin 不读取 raw config。它只接收已校验的命名配置：

    type Config struct {
        Address string
    }

Factory 负责把 raw config 严格解码为自己拥有的 Config，应用默认值、执行 Validate 并创建 Plugin。Plugin 看不到 raw config，Runtime 看不到任何配置。

## 11. 不变量

- I1：一个 Plugin 实例同一时刻最多绑定一个 Fiber；
- I2：Manifest 是确定、无 I/O 的完整声明；
- I3：同一 exact Scope 和 Service 类型最多一个 Active Provider；
- I4：Active Plugin 的 required dependencies 全部指向 Active Provider；
- I5：Require/Resolve 只在 Apply 中开放；
- I6：Starting、Stopping、Stopped、Failed Fiber 不参与 dispatch；
- I7：Service、Event、Waterfall 使用独立类型键和 Registry；
- I8：Runtime 不在状态锁内调用 Plugin、Observer、Middleware、Action 或 reporter；
- I9：Effect 只属于 Runtime，不进入插件作者 API；
- I10：停止顺序是 dependent-first、child-first、contribution-first、Dispose-last；
- I11：Event 是通知机制，不是 Event Store；
- I12：Waterfall 不自动发布 Event；
- I13：业务 Service interface 不暴露 Base、Fiber、Scope 或 Runtime；
- I14：公共泛型不使用 any 或 interface{}；
- I15：不存在 Context、Definition、Provide、Observe、Use、Registration 兼容路径。
- I16：一个 Plugin 只有一个 EventObserver 入口，可显式声明多个 Event 类型，但不能重复声明同一类型；

## 12. 当前验收边界

Plugin 底座重构期间允许仓库其他领域暂时无法编译，但以下范围必须独立通过：

    go test ./plugin/...
    go test -race ./plugin/...
    go vet ./plugin/...
    go test ./tests/architecture
    go build ./plugin/...
    git diff --check

example 必须覆盖：

- Service 直接由 Plugin 对象提供；
- Scope Service 继承和覆盖；
- Service owner 发布 Event、Observer 声明一个或多个监听类型；
- Plugin 声明其拥有的 Waterfall Middleware 和洋葱顺序。

完成 Plugin 契约确认后，再按领域逐个迁移 Goren 其他模块，不增加过渡性适配代码。
