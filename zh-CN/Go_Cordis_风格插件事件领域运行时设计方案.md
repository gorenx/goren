# Go Cordis 风格插件事件领域运行时设计方案

## 1. 设计背景

目标是设计一个适用于 Go 服务端、模块化单体、Agent Runtime 的 **插件组合运行时**。

它借鉴 Cordis 的几个核心思想：

```text
插件组合
依赖感知
Scope
事件
Waterfall
生命周期
Effect
Fiber
```

但不照搬 Cordis 的 API，也不把“Everything is Plugin / Event”作为设计目标。

框架必须同时满足两个目标：

```text
动态组合能力
+
领域职责聚合
```

也就是说：

> Runtime 负责把模块组合起来。

但：

> Domain/Application 仍然拥有显式业务 Workflow。

不能为了插件化，把一次完整业务流程拆成散落在 EventBus 和 Middleware 中的隐式控制流。

---

# 2. 核心设计原则

整个 Runtime 中只定义七个一等概念：

```text
Daemon
Fiber
Context
Scope
Capability
Event
Waterfall
```

它们分别解决不同问题：

```text
Daemon
= 系统组成单元

Fiber
= Daemon 的运行时实例和生命周期 Owner

Context
= Fiber 与 Runtime 交互的句柄

Scope
= 注册项的可见性和事件路由边界

Capability
= Daemon 之间的同步依赖与直接调用

Event
= 已发生事实的异步/解耦传播

Waterfall
= 主流程上的可拦截扩展点
```

再增加一个底层资源模型：

```text
Effect
= Fiber 创建的、退出时必须回收的资源
```

最终职责原则：

```text
Daemon       owns composition
Fiber        owns lifetime
Context      carries runtime identity
Scope        owns visibility
Capability   owns direct dependency
Event        owns facts
Waterfall    owns interception
Domain       owns workflow and invariant
```

最核心的一条：

> **Capability 是模块之间真正的主调用链。Event 和 Waterfall 都只能围绕主调用链扩展，不能取代主调用链。**

---

# 3. 总体架构

整个系统分成三层。

```text
┌──────────────────────────────────────────────┐
│             Application / Domain             │
│                                              │
│ OrderService / PaymentService / AgentLoop    │
│                                              │
│ 显式 Workflow / State Machine / Invariant    │
└───────────────────────┬──────────────────────┘
                        │
              Capability / Waterfall
                        │
              Event after facts occur
                        │
┌───────────────────────▼──────────────────────┐
│                Composition Runtime           │
│                                              │
│ Daemon                                       │
│ Fiber / Effect                               │
│ Context / Scope                              │
│ CapabilityRegistry                          │
│ DependencyGraph                             │
│ EventBus                                     │
│ WaterfallRegistry                           │
└───────────────────────┬──────────────────────┘
                        │
┌───────────────────────▼──────────────────────┐
│               Infrastructure                 │
│                                              │
│ PostgreSQL / Redis / HTTP / Queue / LLM ...  │
└──────────────────────────────────────────────┘
```

Runtime 内部最重要的是：

```text
                         Runtime
                            │
          ┌─────────────────┼─────────────────┐
          │                 │                 │
      Fiber Tree        Scope Tree        Registries
          │                 │                 │
      lifecycle          visibility          ├─ Capability
      ownership          routing             ├─ Event
                                             └─ Waterfall
                            │
                     DependencyGraph
```

---

# 4. Runtime 各组件职责

## 4.1 Runtime

Runtime 是整个插件系统的内核。

它不负责领域业务。

它负责：

```text
Daemon mount / unmount
Fiber 创建和销毁
Scope 管理
Capability 注册和解析
Capability 依赖跟踪
Event listener 注册和路由
Waterfall middleware 注册和执行
Effect 生命周期
依赖失效传播
Blocked Fiber 唤醒
运行时诊断
```

核心数据结构：

```go
type Runtime struct {
	rootFiber *Fiber
	rootScope *Scope

	fibers map[FiberID]*Fiber

	scopes *ScopeTree

	capabilities *CapabilityRegistry
	dependencies *DependencyGraph

	events *EventBus
	hooks  *WaterfallRegistry

	sequence atomic.Uint64

	mu sync.RWMutex
}
```

Runtime 不应该出现：

```text
Order logic
Payment logic
Agent reasoning
HTTP business handler
```

这些都属于上层领域。

---

# 5. Daemon：系统组成单元

## 5.1 定义

Daemon 是：

> 可以被 Runtime 挂载的一块系统能力。

例如：

```text
PostgreSQLDaemon
RedisDaemon
OrderDaemon
PaymentDaemon
HTTPDaemon
NotificationDaemon
RiskDaemon
AgentDaemon
LLMDaemon
MemoryDaemon
```

接口：

```go
type Daemon interface {
	Name() string
	Start(*Context) error
}
```

不设计：

```go
Stop()
```

停止统一由 Fiber + context cancellation + Effect 完成。

因此一个 Daemon：

```text
Daemon
   │
   │ Runtime.Mount
   ▼
Fiber
   │
   │ Daemon.Start(ctx)
   ▼
Running
```

---

# 6. Fiber：Daemon 的运行时实例

Daemon 是静态定义。

Fiber 是动态运行实例。

```text
Daemon definition
      │
      │ Mount
      ▼
Fiber instance
```

## 6.1 Fiber 数据结构

```go
type FiberID uint64

type FiberState uint8

const (
	FiberPending FiberState = iota
	FiberStarting
	FiberRunning
	FiberBlocked
	FiberStopping
	FiberStopped
	FiberFailed
)

type Fiber struct {
	ID   FiberID
	Name string

	Daemon Daemon

	State FiberState

	Parent   *Fiber
	Children map[FiberID]*Fiber

	Scope *Scope

	ctx    context.Context
	cancel context.CancelFunc

	effects *EffectStack

	waiting []CapabilityKey

	mu sync.Mutex
}
```

Fiber 负责：

```text
运行状态
父子关系
Cancellation
Effect ownership
Capability ownership
Event subscription ownership
Waterfall registration ownership
Dependency lifecycle
```

Fiber 不负责：

```text
业务状态
Order 状态机
Agent Turn 状态机
Payment Workflow
```

---

# 7. Fiber Tree

Runtime 维护一棵生命周期树。

例如：

```text
Root Fiber
│
├── PostgreSQL Fiber
│
├── Order Fiber
│
├── Payment Fiber
│
├── HTTP Fiber
│
├── Risk Fiber
│
└── Notification Fiber
```

Agent 系统也可以：

```text
Root Fiber
│
└── Agent Runtime Fiber
    │
    ├── LLM Fiber
    ├── Memory Fiber
    ├── Tools Fiber
    └── Session Fiber
```

父 Fiber 停止：

```text
Parent Fiber
      ↓
stop dependent child Fibers
      ↓
cancel context
      ↓
dispose effects
```

因此 Fiber Tree 是：

> **Structured Lifecycle Tree**

而不是业务流程树。

---

# 8. Context：Runtime 交互句柄

每个 Fiber 启动时都会获得一个 Context。

```go
type Context struct {
	context.Context

	runtime *Runtime
	fiber   *Fiber
	scope   *Scope

	txn *MountTxn
}
```

Context 表示：

```text
我属于哪个 Fiber
我当前位于哪个 Scope
我正在操作哪个 Runtime
我当前是否处于 Mount Transaction
```

提供：

```go
func (c *Context) Runtime() *Runtime
func (c *Context) Fiber() *Fiber
func (c *Context) Scope() *Scope
```

Context 不暴露：

```go
ctx.Postgres
ctx.Redis
ctx.Order
ctx.LLM
ctx.Memory
```

否则最终会变成 God Object。

所有业务依赖通过 Capability 获取。

---

# 9. Effect：资源生命周期

Daemon.Start() 中创建的所有 Runtime-managed 资源，都必须变成 Effect。

例如：

```text
Capability registration
Event subscription
Waterfall middleware
Timer
Goroutine
File watcher
Network listener
Child Fiber
```

接口：

```go
type Effect interface {
	Dispose(context.Context) error
}
```

方便函数注册：

```go
type EffectFunc func(context.Context) error

func (f EffectFunc) Dispose(ctx context.Context) error {
	return f(ctx)
}
```

Fiber 内部：

```go
type EffectStack struct {
	items []Effect
	mu    sync.Mutex
}
```

清理必须逆序：

```text
register A
register B
start C

Fiber Stop

C.Dispose
B.Dispose
A.Dispose
```

---

# 10. Mount Transaction

Daemon.Start() 不是直接修改 Runtime。

所有注册首先进入一个临时事务。

```go
type MountTxn struct {
	fiber *Fiber

	effects []Effect

	capabilities []*CapabilityEntry
	subscribers  []*EventSubscriber
	middlewares  []*WaterfallEntry

	bindings []CapabilityBinding
	waiting  []CapabilityKey

	committed bool
}
```

原因：

假设：

```text
Provide OrderService
On OrderCreated
Use BeforeCreateOrder
Start Worker
Worker 初始化失败
```

不能留下前三个注册。

因此：

```text
Daemon.Start
     │
     ▼
 MountTxn
     │
 ┌───┴────┐
 │        │
成功      失败
 │        │
commit   rollback
 │        │
Running  Blocked / Failed
```

---

# 11. Scope：逻辑可见性空间

Scope 不负责生命周期。

Scope 只负责：

```text
Capability visibility
Waterfall visibility
Event routing
```

## 11.1 数据结构

```go
type ScopeID uint64

type Scope struct {
	ID   ScopeID
	Name string

	Parent *Scope

	Children map[ScopeID]*Scope

	Depth int
}
```

```go
type ScopeTree struct {
	root *Scope

	mu sync.RWMutex
}
```

例如普通服务：

```text
Global
   │
Tenant A
   │
Request A1
```

Agent：

```text
Global
   │
Coding Preset
   │
Agent A
```

---

# 12. Fiber 和 Scope 的区别

必须严格区分：

```text
Fiber
= 谁拥有

Scope
= 谁能看到
```

例如：

```text
Risk Fiber
    │
    │ owns
    ▼
Risk Middleware
    │
    │ visible in
    ▼
Tenant A Scope
```

默认注册规则：

```text
Owner = ctx.Fiber
Scope = ctx.Scope
```

所以：

```text
Context
  │
  ├── Fiber → lifecycle ownership
  │
  └── Scope → visibility / routing
```

---

# 13. Scoped Layer Store

Capability 和 Waterfall 都需要按 Scope 存储注册项。

Runtime 内部提供通用 LayerStore。

```go
type LayerStore[K comparable, V any] struct {
	global map[K]V
	scoped map[ScopeID]map[K]V

	mu sync.RWMutex
}
```

逻辑结构：

```text
Layer Store
│
├── Global Layer
│
├── Tenant A Layer
│
├── Tenant B Layer
│
└── Agent A Layer
```

不同 Registry 对 Layer 的使用语义不同。

Capability：

```text
nearest scope shadows parent
```

Waterfall：

```text
parent + current accumulate
```

Event：

```text
不 merge registration
根据 publisher scope 做 listener admission
```

---

# 14. Capability：系统的主依赖模型

Capability 是整个架构最重要的部分。

Capability 表示：

> 一个 Daemon 对外提供、其他 Daemon 可以直接调用的能力。

例如：

```go
type OrderRepository interface {
	Save(context.Context, *Order) error
}

type OrderService interface {
	Create(
		context.Context,
		CreateOrder,
	) (*Order, error)
}

type Mailer interface {
	Send(context.Context, Mail) error
}
```

业务代码只看到普通 Go interface。

---

# 15. CapabilityKey

调用方不需要手动创建字符串 Key。

Runtime 内部使用类型生成 CapabilityKey。

```go
type CapabilityKey struct {
	Type reflect.Type
}
```

```go
func capabilityKeyOf[T any]() CapabilityKey {
	return CapabilityKey{
		Type: reflect.TypeOf((*T)(nil)).Elem(),
	}
}
```

例如：

```go
Provide[OrderRepository](ctx, repo)
```

Runtime 内部得到：

```text
CapabilityKey {
    Type = OrderRepository interface type
}
```

因此公共 API 不出现：

```text
"order.repository"
"order.service"
```

这种字符串依赖。

---

# 16. CapabilityEntry

```go
type CapabilityID uint64

type CapabilityEntry struct {
	ID CapabilityID

	Key CapabilityKey

	Value any

	Owner FiberID
	Scope ScopeID

	State CapabilityState

	Sequence uint64
}
```

```go
type CapabilityState uint8

const (
	CapabilityStaged CapabilityState = iota
	CapabilityActive
	CapabilityDraining
	CapabilityRemoved
)
```

其中：

```text
Owner
= 谁负责它的生命周期

Scope
= 谁能够解析到它
```

---

# 17. CapabilityRegistry

```go
type CapabilityRegistry struct {
	entries map[CapabilityKey]map[ScopeID]*CapabilityEntry

	mu sync.RWMutex
}
```

例如：

```text
OrderRepository

Global:
    PostgresOrderRepository

TenantA:
    TenantOrderRepository

AgentA:
    none
```

---

# 18. Capability 的三个 API

Capability 有三个核心操作：

```go
Provide[T]()
Require[T]()
Resolve[T]()
```

它们不是同一个语义。

---

# 19. Provide 流程

API：

```go
func Provide[T any](
	ctx *Context,
	impl T,
) error
```

例如 PostgreSQLDaemon：

```go
func (d *PostgreSQLDaemon) Start(ctx *Context) error {
	db, err := sql.Open(...)
	if err != nil {
		return err
	}

	Effect(ctx, func(context.Context) error {
		return db.Close()
	})

	repo := NewPostgresOrderRepository(db)

	return Provide[OrderRepository](ctx, repo)
}
```

## 19.1 Provide 内部流程

```text
PostgreSQL Fiber
        │
        │ Provide[OrderRepository]
        ▼
derive CapabilityKey
        │
        ▼
create CapabilityEntry
        │
        ├── Owner = PostgreSQL Fiber
        └── Scope = ctx.Scope
        │
        ▼
put into MountTxn as Staged
        │
        ▼
Daemon.Start success
        │
        ▼
MountTxn.Commit
        │
        ▼
CapabilityRegistry
        │
        ▼
CapabilityActive
        │
        ▼
notify DependencyGraph
```

只有 Commit 后：

```text
Capability 才对其他 Fiber 可见
```

避免 consumer 看到一个最终启动失败的 Provider。

---

# 20. Require 流程

Require 表示：

> 当前 Fiber 必须依赖这个 Capability 才能运行。

API：

```go
func Require[T any](
	ctx *Context,
) (T, error)
```

只允许在：

```text
FiberStarting
```

阶段使用。

例如：

```go
func (d *OrderDaemon) Start(ctx *Context) error {
	repo, err := Require[OrderRepository](ctx)
	if err != nil {
		return err
	}

	service := NewOrderService(ctx, repo)

	return Provide[OrderService](ctx, service)
}
```

## 20.1 Require 内部流程

```text
Order Fiber
    │
    │ Require[OrderRepository]
    ▼
derive CapabilityKey
    │
    ▼
CapabilityRegistry.Resolve(scope, key)
    │
    ├── Current Scope
    ├── Parent Scope
    └── Root Scope
    │
    ▼
找到 CapabilityEntry?
    │
 ┌──┴────────────┐
 │               │
Yes              No
 │               │
 ▼               ▼
return Value   register waiter
 │               │
 ▼               ▼
create         return
Dependency    ErrCapabilityUnavailable
Binding
```

---

# 21. Resolve 流程

Resolve 表示：

> 当前执行过程中，可选地查找一个 Capability。

API：

```go
func Resolve[T any](
	ctx *Context,
) (T, bool)
```

例如：

```go
metrics, ok := Resolve[Metrics](ctx)

if ok {
	metrics.Record(...)
}
```

Resolve：

```text
不会把 Fiber 变成硬依赖 Consumer
不会导致 Fiber Blocked
不会创建 DependencyBinding
```

因此：

```text
Require
= hard dependency

Resolve
= optional / late binding
```

---

# 22. Capability Scope Lookup

假设：

```text
Global
  │
TenantA
  │
RequestA
```

Capability：

```text
Global:
OrderRepository → PostgresRepository

TenantA:
OrderRepository → TenantRepository
```

RequestA 调用：

```go
Require[OrderRepository](ctx)
```

查找：

```text
RequestA
   ↓ miss
TenantA
   ↓ hit
TenantRepository
```

所以 Capability 可见性方向：

```text
Global
   ↓
Parent
   ↓
Child
```

读取策略：

```text
Current → Parent → Root
```

nearest scope wins。

---

# 23. CapabilityDependencyGraph

Require 成功后，Runtime 创建显式依赖关系。

```go
type CapabilityBinding struct {
	Consumer FiberID
	Provider FiberID

	Capability CapabilityID
	Key        CapabilityKey
}
```

DependencyGraph：

```go
type DependencyGraph struct {
	byConsumer map[FiberID][]CapabilityBinding
	byProvider map[FiberID][]CapabilityBinding

	waiting map[CapabilityKey]map[FiberID]struct{}

	mu sync.RWMutex
}
```

例如：

```text
PostgreSQL Fiber
        │
        │ provides
        ▼
OrderRepository
        │
        │ Require
        ▼
Order Fiber
        │
        │ provides
        ▼
OrderService
        │
        │ Require
        ▼
HTTP Fiber
```

Runtime 最终得到：

```text
HTTP Fiber
   ↓ depends on
Order Fiber
   ↓ depends on
PostgreSQL Fiber
```

---

# 24. Capability 缺失流程

假设：

```text
OrderDaemon
```

先于 PostgreSQLDaemon 启动。

流程：

```text
Runtime.Mount(OrderDaemon)
        ↓
create Order Fiber
        ↓
FiberStarting
        ↓
OrderDaemon.Start(ctx)
        ↓
Require[OrderRepository]
        ↓
registry miss
        ↓
DependencyGraph.waiting[
    OrderRepository
] += Order Fiber
        ↓
ErrCapabilityUnavailable
        ↓
MountTxn.Rollback
        ↓
FiberBlocked
```

之后：

```text
Runtime.Mount(PostgreSQLDaemon)
        ↓
PostgreSQL Fiber Starting
        ↓
Provide[OrderRepository]
        ↓
Commit
        ↓
CapabilityActive
        ↓
DependencyGraph finds waiter
        ↓
wake Order Fiber
        ↓
OrderDaemon.Start()
        ↓
Require success
        ↓
Provide[OrderService]
        ↓
FiberRunning
```

这就是 Reactive Dependency。

---

# 25. Capability 直接调用流程

Runtime 只负责：

```text
发现 Capability
建立依赖
管理生命周期
```

真正调用之后，不再经过 Runtime。

例如：

```go
repo, err := Require[OrderRepository](ctx)

...

repo.Save(callCtx, order)
```

调用路径：

```text
OrderService
    │
    │ Go interface call
    ▼
PostgresOrderRepository
```

不是：

```text
OrderService
   ↓
Runtime.invoke()
   ↓
CapabilityRegistry
   ↓
PostgresRepository
```

因此 Capability Runtime 不侵入业务调用链。

这是一个重要设计原则：

> **Resolve once, direct call afterwards.**

---

# 26. Event：事实传播模型

Event 表示：

> 一件事情已经发生。

例如：

```go
type OrderCreated struct {
	OrderID string
	UserID  string
}
```

Event 不用于：

```text
CreateOrder command
调用 Repository
控制 Workflow
veto 已发生事实
```

---

# 27. EventSubscriber

```go
type SubscriberID uint64

type EventSubscriber struct {
	ID SubscriberID

	Type reflect.Type

	Owner FiberID
	Scope ScopeID

	Mode DispatchMode

	Sequence uint64

	Handler any
}
```

EventBus：

```go
type EventBus struct {
	subscribers map[reflect.Type][]*EventSubscriber

	mu sync.RWMutex
}
```

---

# 28. Event 注册流程

API：

```go
func On[T any](
	ctx *Context,
	handler func(context.Context, T) error,
) error
```

NotificationDaemon：

```go
func (d *NotificationDaemon) Start(
	ctx *Context,
) error {

	return On[OrderCreated](
		ctx,
		func(
			callCtx context.Context,
			e OrderCreated,
		) error {
			return d.send(callCtx, e)
		},
	)
}
```

内部：

```text
Notification Fiber
        │
        │ On[OrderCreated]
        ▼
create EventSubscriber
        │
        ├── Owner = Notification Fiber
        └── Scope = ctx.Scope
        │
        ▼
MountTxn
        │
        ▼
Commit
        │
        ▼
EventBus
```

同时创建：

```text
Subscription Effect
```

Fiber 停止：

```text
unsubscribe automatically
```

---

# 29. Event 发布流程

API：

```go
func Publish[T any](
	ctx *Context,
	event T,
) error
```

OrderService：

```go
Publish(
	s.runtimeCtx,
	OrderCreated{
		OrderID: order.ID,
	},
)
```

流程：

```text
OrderService
    │
    │ Publish
    ▼
derive event type
    │
    ▼
publisher scope = ctx.Scope
    │
    ▼
calculate:
current scope + ancestors
    │
    ▼
snapshot eligible listeners
    │
    ▼
release EventBus lock
    │
    ▼
dispatch handlers
```

---

# 30. Scoped Event Routing

假设：

```text
Global
   │
Tenant A
   │
Request A
```

Request A 发布事件。

监听器：

```text
Request A listener     ✓
Tenant A listener      ✓
Global listener        ✓

Tenant B listener      ✗
Request B listener     ✗
```

所以 Event propagation：

```text
Child
  ↑
Parent
  ↑
Global
```

和 Capability 正好相反：

```text
Capability visibility
Global → Child

Event visibility
Child → Global
```

---

# 31. Event Dispatch Mode

支持：

```go
type DispatchMode uint8

const (
	DispatchSerial DispatchMode = iota
	DispatchParallel
	DispatchBestEffort
)
```

语义：

```text
Serial
= 顺序调用，错误停止

Parallel
= 并行调用，聚合错误

BestEffort
= 尽量执行所有 listener
```

但不管哪种：

> Event 仍然只是“事实”。

Dispatch Mode 不能改变 Event 的领域语义。

---

# 32. Waterfall：可拦截扩展模型

Waterfall 用于：

> 某个动作即将执行，允许其他 Daemon 插入行为。

例如：

```text
BeforeOrderCreate
BeforePayment
BeforeHTTPRequest
BeforeToolExecute
BeforeLLMRequest
```

它不是 Event。

Event：

```text
事情已经发生
```

Waterfall：

```text
事情正在发生
```

---

# 33. Waterfall 类型

```go
type Next[I, O any] func(
	context.Context,
	I,
) (O, error)

type Middleware[I, O any] func(
	context.Context,
	I,
	Next[I, O],
) (O, error)
```

Hook 定义：

```go
type Hook[I, O any] struct {
	ID   uint64
	Name string
}
```

例如：

```go
var BeforeCreateOrder =
	NewHook[CreateOrder, *Order](
		"order.before-create",
	)
```

---

# 34. WaterfallEntry

```go
type WaterfallEntry struct {
	ID uint64

	HookID uint64

	Owner FiberID
	Scope ScopeID

	Order    int
	Sequence uint64

	Middleware any
}
```

Registry：

```go
type WaterfallRegistry struct {
	entries map[uint64][]*WaterfallEntry

	mu sync.RWMutex
}
```

---

# 35. Waterfall 注册流程

RiskDaemon：

```go
func (d *RiskDaemon) Start(ctx *Context) error {
	return Use(
		ctx,
		BeforeCreateOrder,
		100,
		d.checkRisk,
	)
}
```

内部：

```text
Risk Fiber
    │
    │ Use(BeforeCreateOrder)
    ▼
create WaterfallEntry
    │
    ├── Owner = Risk Fiber
    ├── Scope = ctx.Scope
    └── Order = 100
    │
    ▼
MountTxn
    │
    ▼
Commit
    │
    ▼
WaterfallRegistry
```

Fiber Stop 时自动移除。

---

# 36. Waterfall Scope Lookup

假设：

```text
Global
   │
Tenant
   │
Request
```

注册：

```text
Global:
Tracing middleware

Tenant:
Risk middleware

Request:
Debug middleware
```

Request 执行 Hook：

```text
Tracing
   ↓
Risk
   ↓
Debug
   ↓
Terminal
```

Waterfall 的 visibility 是：

```text
Global
   ↓
Parent
   ↓
Current
```

和 Capability 类似。

不同的是：

Capability：

```text
nearest provider wins
```

Waterfall：

```text
所有可见 middleware accumulate
```

---

# 37. Waterfall 执行流程

```go
result, err := Run(
	ctx,
	BeforeCreateOrder,
	cmd,
	coreCreate,
)
```

流程：

```text
derive current Scope
        ↓
collect visible middleware
        ↓
Global → Parent → Current
        ↓
sort:
Scope depth
Order
Sequence
        ↓
compose onion
        ↓
execute outer middleware
        ↓
...
        ↓
terminal domain logic
        ↓
unwind
```

例如：

```text
Request
   ↓
Tracing
   ↓
Auth
   ↓
Risk
   ↓
Promotion
   ↓
Core
   ↑
Promotion
   ↑
Risk
   ↑
Auth
   ↑
Tracing
```

Middleware 可以：

```text
修改 input
调用 next
不调用 next
short-circuit
修改 output
处理 error
```

---

# 38. Next 的执行约束

默认规定：

> 每个 Middleware 的 `next()` 只能调用一次。

否则：

```go
next(ctx, input)
next(ctx, input)
```

可能重复执行：

```text
数据库写入
Tool call
外部请求
```

因此 Runtime 应用 one-shot guard：

```go
type nextGuard struct {
	called atomic.Bool
}
```

第二次调用：

```text
ErrNextAlreadyCalled
```

---

# 39. Capability / Event / Waterfall 三种通信方式

这是整个架构最重要的区别。

## Capability

语义：

```text
“我需要你完成一个能力。”
```

特点：

```text
1 → 1
显式调用
同步依赖
有返回值
形成 DependencyGraph
```

例：

```text
HTTP → OrderService
OrderService → OrderRepository
AgentLoop → LLM
```

---

## Event

语义：

```text
“这件事情已经发生。”
```

特点：

```text
1 → N
解耦
事实传播
不属于同步主调用链
```

例：

```text
OrderCreated
    ├── Notification
    ├── Audit
    └── Analytics
```

---

## Waterfall

语义：

```text
“我要执行这个动作，你可以介入。”
```

特点：

```text
1 → pipeline → 1
可修改
可 veto
可 short-circuit
around semantics
```

例：

```text
Order.Create
    ↓
Auth
    ↓
Risk
    ↓
Core
```

---

# 40. 三者组合后的业务 Workflow

最终业务代码应该长这样：

```text
HTTP Handler
     │
     │ Capability call
     ▼
OrderService.Create
     │
     │ Waterfall
     ▼
BeforeCreateOrder
     │
     ▼
Domain Logic
     │
     │ Capability call
     ▼
OrderRepository.Save
     │
     ▼
Transaction Commit
     │
     │ Event
     ▼
OrderCreated
     │
     ├── Notification
     ├── Audit
     └── Analytics
```

核心 Workflow 仍然属于：

```text
OrderService.Create
```

而不是 EventBus 或 Waterfall。

---

# 41. 完整示例：订单服务

下面用同一个订单系统把整个 Runtime 从启动到关闭全部串起来。

系统由：

```text
PostgreSQLDaemon
OrderDaemon
RiskDaemon
NotificationDaemon
HTTPDaemon
```

组成。

---

# 42. 领域接口

Repository：

```go
type OrderRepository interface {
	Save(context.Context, *Order) error
}
```

Service：

```go
type OrderService interface {
	Create(
		context.Context,
		CreateOrder,
	) (*Order, error)
}
```

Event：

```go
type OrderCreated struct {
	OrderID string
	UserID  string
}
```

Hook：

```go
var BeforeCreateOrder =
	NewHook[CreateOrder, *Order](
		"order.before-create",
	)
```

---

# 43. PostgreSQLDaemon

```go
type PostgreSQLDaemon struct{}

func (d *PostgreSQLDaemon) Name() string {
	return "postgresql"
}

func (d *PostgreSQLDaemon) Start(
	ctx *Context,
) error {

	db, err := sql.Open(...)
	if err != nil {
		return err
	}

	Effect(ctx, func(context.Context) error {
		return db.Close()
	})

	repo := NewPostgresOrderRepository(db)

	return Provide[OrderRepository](ctx, repo)
}
```

启动后：

```text
PostgreSQL Fiber
        │
        ▼
CapabilityRegistry

OrderRepository
        │
        ▼
PostgresOrderRepository
```

---

# 44. OrderDaemon

```go
type OrderDaemon struct{}

func (d *OrderDaemon) Name() string {
	return "order"
}

func (d *OrderDaemon) Start(
	ctx *Context,
) error {

	repo, err :=
		Require[OrderRepository](ctx)

	if err != nil {
		return err
	}

	service := NewOrderService(
		ctx,
		repo,
	)

	return Provide[OrderService](
		ctx,
		service,
	)
}
```

启动后依赖：

```text
Order Fiber
     │
     │ requires
     ▼
OrderRepository
     │
     │ provided by
     ▼
PostgreSQL Fiber
```

同时：

```text
Order Fiber
     │
     │ provides
     ▼
OrderService
```

---

# 45. RiskDaemon

```go
type RiskDaemon struct{}

func (d *RiskDaemon) Name() string {
	return "risk"
}

func (d *RiskDaemon) Start(
	ctx *Context,
) error {

	return Use(
		ctx,
		BeforeCreateOrder,
		100,
		func(
			callCtx context.Context,
			cmd CreateOrder,
			next Next[
				CreateOrder,
				*Order,
			],
		) (*Order, error) {

			if highRisk(cmd) {
				return nil, ErrRiskRejected
			}

			return next(callCtx, cmd)
		},
	)
}
```

它不需要依赖：

```text
OrderDaemon
```

只需要往当前 Scope 的：

```text
BeforeCreateOrder Waterfall
```

贡献 Middleware。

---

# 46. NotificationDaemon

```go
type NotificationDaemon struct{}

func (d *NotificationDaemon) Name() string {
	return "notification"
}

func (d *NotificationDaemon) Start(
	ctx *Context,
) error {

	return On[OrderCreated](
		ctx,
		func(
			callCtx context.Context,
			e OrderCreated,
		) error {

			return sendOrderNotification(
				callCtx,
				e,
			)
		},
	)
}
```

Notification 不参与创建订单。

它只响应：

```text
OrderCreated
```

---

# 47. HTTPDaemon

```go
type HTTPDaemon struct{}

func (d *HTTPDaemon) Start(
	ctx *Context,
) error {

	orderService, err :=
		Require[OrderService](ctx)

	if err != nil {
		return err
	}

	server := NewHTTPServer()

	server.POST(
		"/orders",
		func(req Request) Response {

			order, err :=
				orderService.Create(
					req.Context(),
					toCommand(req),
				)

			return toResponse(order, err)
		},
	)

	return RunServer(ctx, server)
}
```

最终 DependencyGraph：

```text
PostgreSQL Fiber
       │
       ▼
OrderRepository
       │
       ▼
Order Fiber
       │
       ▼
OrderService
       │
       ▼
HTTP Fiber
```

Risk 和 Notification 不在这条硬依赖链中：

```text
Risk Fiber
    │
    └── Waterfall contribution

Notification Fiber
    │
    └── Event listener
```

这点非常重要。

---

# 48. 系统启动完整流程

即使按乱序挂载：

```go
runtime.Mount(HTTPDaemon)
runtime.Mount(OrderDaemon)
runtime.Mount(RiskDaemon)
runtime.Mount(NotificationDaemon)
runtime.Mount(PostgreSQLDaemon)
```

也可以正常工作。

## 第一步：HTTP

```text
HTTP Fiber Starting
      ↓
Require[OrderService]
      ↓
missing
      ↓
FiberBlocked
```

## 第二步：Order

```text
Order Fiber Starting
      ↓
Require[OrderRepository]
      ↓
missing
      ↓
FiberBlocked
```

## 第三步：Risk

没有硬依赖：

```text
Use(BeforeCreateOrder)
      ↓
commit
      ↓
Risk Fiber Running
```

## 第四步：Notification

```text
On[OrderCreated]
      ↓
commit
      ↓
Notification Fiber Running
```

## 第五步：PostgreSQL

```text
PostgreSQL Fiber
      ↓
Provide[OrderRepository]
      ↓
commit
      ↓
Capability active
      ↓
wake Order Fiber
```

Order 重试：

```text
Require[OrderRepository]
      ↓
success
      ↓
Provide[OrderService]
      ↓
commit
      ↓
Order Fiber Running
      ↓
wake HTTP Fiber
```

HTTP 重试：

```text
Require[OrderService]
      ↓
success
      ↓
start HTTP server
      ↓
HTTP Fiber Running
```

最终：

```text
PostgreSQL Running
Order      Running
HTTP       Running
Risk       Running
Notification Running
```

---

# 49. 一次请求完整执行流程

用户：

```text
POST /orders
```

执行：

```text
HTTP Handler
     │
     │ direct Go interface call
     ▼
OrderService.Create
     │
     ▼
Run(BeforeCreateOrder)
     │
     ├── Risk Middleware
     │
     ▼
Core Create Logic
     │
     ▼
OrderRepository.Save
     │
     ▼
PostgreSQL
     │
     ▼
commit
     │
     ▼
Publish(OrderCreated)
     │
     ├── Notification listener
     └── Other listeners
```

三个 Runtime 机制分别出现：

```text
HTTP → OrderService
= Capability

OrderService → Risk
= Waterfall

OrderService → Notification
= Event
```

这就是整个框架的核心执行模型。

---

# 50. Scope 示例：Tenant Override

假设：

```text
Global
│
└── Tenant A
```

Global：

```text
OrderRepository
→ DefaultPostgresRepository
```

Tenant A：

```text
OrderRepository
→ TenantARepository
```

Tenant A 的 Order Fiber：

```text
Require[OrderRepository]
```

查找：

```text
Tenant A
   ↓
TenantARepository
```

其他 Tenant：

```text
Current
  ↓ miss
Global
  ↓
DefaultPostgresRepository
```

因此：

```text
一个 CapabilityRegistry
+
多个 Scoped Layer
```

就可以实现局部 override。

不需要：

```text
每个 Tenant 一套 Runtime
```

---

# 51. Scope 示例：Waterfall

Global：

```text
TracingMiddleware
```

Tenant A：

```text
RiskMiddleware
```

Request：

```text
DebugMiddleware
```

Request 内执行：

```text
Tracing
   ↓
Risk
   ↓
Debug
   ↓
Domain Core
```

Scope 越具体：

```text
越靠近 terminal
```

同一个 exact scope 内再通过：

```text
Order
Sequence
```

排序。

---

# 52. Scope 示例：Event

Tenant A Request 发布：

```text
OrderCreated
```

路由：

```text
Request listeners     ✓
Tenant A listeners    ✓
Global listeners      ✓

Tenant B listeners    ✗
```

因此同一棵 Scope Tree：

```text
Capability:
向下可见

Waterfall:
向下继承

Event:
向上冒泡
```

---

# 53. Fiber Stop 流程

假设停止：

```text
PostgreSQL Fiber
```

由于：

```text
Order Fiber
requires
OrderRepository
```

并且：

```text
HTTP Fiber
requires
OrderService
```

Runtime 必须先处理 dependents。

完整流程：

```text
Stop PostgreSQL Fiber
        ↓
State = FiberStopping
        ↓
mark its Capability Draining
        ↓
DependencyGraph:
find Order Fiber
        ↓
Stop Order Fiber
        ↓
mark OrderService Draining
        ↓
find HTTP Fiber
        ↓
Stop HTTP Fiber
        ↓
HTTP effects disposed
        ↓
Order effects disposed
        ↓
remove OrderService
        ↓
PostgreSQL effects disposed
        ↓
close DB
        ↓
remove OrderRepository
        ↓
PostgreSQL Stopped
```

实际停止顺序：

```text
HTTP
 ↓
Order
 ↓
PostgreSQL
```

即依赖图逆序。

---

# 54. Capability Provider 消失后的状态

PostgreSQL 消失后：

```text
Order Fiber → Blocked
waiting OrderRepository

HTTP Fiber → Blocked
waiting OrderService
```

Risk：

```text
Running
```

Notification：

```text
Running
```

因为它们没有硬依赖 PostgreSQL。

这就是：

```text
Capability Dependency
```

和：

```text
Event / Waterfall Contribution
```

必须分开的原因。

---

# 55. Provider 恢复流程

随后挂载：

```text
SQLiteDaemon
```

它：

```go
Provide[OrderRepository](ctx, sqliteRepo)
```

Runtime：

```text
SQLite Fiber
    ↓
OrderRepository Active
    ↓
wake Order Fiber
    ↓
OrderDaemon.Start()
    ↓
Require → SQLiteRepository
    ↓
Provide OrderService
    ↓
wake HTTP Fiber
    ↓
HTTP Running
```

此时业务对象重新绑定到：

```text
SQLiteRepository
```

不需要对已有对象做动态指针替换。

---

# 56. Hot Replace 流程

如果希望低停机替换：

```text
PostgreSQL V1
     ↓
PostgreSQL V2
```

建议流程：

```text
Mount V2 Fiber
     ↓
V2 initialization ready
     ↓
prepare replacement Capability
     ↓
mark V1 Draining
     ↓
switch active provider
     ↓
restart dependent Fibers
     ↓
dependents Require again
     ↓
bind V2
     ↓
stop V1
```

即：

```text
prepare
→ switch
→ rebind
→ dispose
```

不要：

```text
stop old
→ hope new works
```

---

# 57. Daemon 子模块流程

Daemon 可以挂载子 Daemon：

```go
child, err := ctx.Runtime().Mount(
	ctx.Fiber(),
	ctx.Scope(),
	childDaemon,
)
```

形成：

```text
Agent Fiber
   │
   ├── Memory Fiber
   ├── Tools Fiber
   └── Session Fiber
```

父 Fiber Stop：

```text
stop children first
```

因此可以构建局部插件树。

---

# 58. Worker 流程

Daemon 不应该直接：

```go
go worker()
```

提供：

```go
func Go(
	ctx *Context,
	fn func(context.Context) error,
) error
```

内部：

```text
Fiber Context
    ↓
context.WithCancel
    ↓
goroutine
    ↓
TaskEffect
```

Fiber Stop：

```text
cancel Fiber Context
        ↓
worker receives Done
        ↓
wait worker exit
        ↓
dispose next Effect
```

这样不会留下孤儿 goroutine。

---

# 59. Runtime.Mount 完整流程

```go
func (r *Runtime) Mount(
	parent *Fiber,
	scope *Scope,
	daemon Daemon,
) (*Fiber, error)
```

完整过程：

```text
Runtime.Mount
     ↓
allocate FiberID
     ↓
create Fiber
     ↓
attach parent
     ↓
create Fiber context
     ↓
FiberPending
     ↓
FiberStarting
     ↓
create MountTxn
     ↓
daemon.Start(ctx)
     │
     ├── Require
     ├── Resolve
     ├── Provide
     ├── On
     ├── Use
     ├── Effect
     └── Go
     ↓
result
 ┌───┼───────────────────┐
 │   │                   │
OK  CapabilityMissing   OtherError
 │   │                   │
 ▼   ▼                   ▼
Commit Rollback         Rollback
 │   │                   │
 ▼   ▼                   ▼
Running Blocked         Failed
```

---

# 60. Runtime.Stop 完整流程

```go
func (r *Runtime) StopFiber(
	ctx context.Context,
	fiber *Fiber,
) error
```

流程：

```text
Running / Blocked
       ↓
Stopping
       ↓
prevent new registrations
       ↓
mark provided capabilities Draining
       ↓
stop hard dependents
       ↓
cancel fiber.Context
       ↓
stop child Fibers
       ↓
dispose Effects reverse order
       ↓
remove capability bindings
       ↓
remove waiting dependencies
       ↓
detach from parent
       ↓
Stopped
```

必须保证：

```text
Stop is idempotent
```

---

# 61. 核心并发规则

Registry 操作统一遵循：

```text
lock
 ↓
modify / snapshot
 ↓
unlock
 ↓
execute user code
```

绝不允许：

```text
registry lock
      ↓
user callback
```

否则可能导致：

```text
deadlock
reentrant registration
stop deadlock
event deadlock
```

---

# 62. Runtime Invariants

## 62.1 Fiber

```text
Stopped Fiber 不允许拥有 Active Capability。

Stopped Fiber 不允许拥有 Active Event listener。

Stopped Fiber 不允许拥有 Active Waterfall entry。

Child Fiber 生命周期不能超过 Parent Fiber。
```

## 62.2 Capability

```text
同一个 exact Scope
+
同一个 CapabilityKey
只能存在一个 Active Provider。
```

```text
Required Provider 消失后
Consumer Fiber 不能继续 Running。
```

## 62.3 Mount

```text
Daemon.Start 失败后
不能遗留部分注册。
```

## 62.4 Effect

```text
Effect 必须按照逆序 Dispose。
```

## 62.5 Scope

Capability：

```text
Current → Parent → Root
```

Waterfall：

```text
Root → Parent → Current
```

Event：

```text
Current → Parent → Root
```

---

# 63. 错误模型

```go
var (
	ErrCapabilityUnavailable =
		errors.New("capability unavailable")

	ErrCapabilityConflict =
		errors.New("capability conflict")

	ErrFiberStopped =
		errors.New("fiber stopped")

	ErrNextAlreadyCalled =
		errors.New("waterfall next already called")
)
```

可以定义：

```go
type CapabilityUnavailableError struct {
	Key   CapabilityKey
	Scope ScopeID
}
```

Runtime 通过类型判断：

```text
CapabilityUnavailable
```

意味着：

```text
Blocked
```

普通 error：

```text
Failed
```

---

# 64. Runtime Diagnostics

框架应该能够直接输出：

## Fiber Tree

```text
root
├── postgres [running]
├── order [running]
├── http [running]
├── risk [running]
└── notification [running]
```

## Capability Graph

```text
postgres
   └── OrderRepository
          ↓
       order
          └── OrderService
                 ↓
               http
```

## Scope Tree

```text
global
├── tenant-a
│   └── request-1
└── tenant-b
```

## Waterfall

```text
BeforeCreateOrder

global:
  tracing [10]

tenant-a:
  auth [50]
  risk [100]
```

## Event subscribers

```text
OrderCreated

global:
  audit

tenant-a:
  notification
```

这对排查插件系统非常重要。

---

# 65. 推荐包结构

```text
runtime/
├── runtime.go
├── context.go
│
├── daemon/
│   └── daemon.go
│
├── fiber/
│   ├── fiber.go
│   ├── state.go
│   ├── effect.go
│   ├── mount_txn.go
│   └── worker.go
│
├── scope/
│   ├── scope.go
│   ├── tree.go
│   └── layer_store.go
│
├── capability/
│   ├── key.go
│   ├── entry.go
│   ├── registry.go
│   ├── provide.go
│   ├── require.go
│   ├── resolve.go
│   └── dependency_graph.go
│
├── event/
│   ├── bus.go
│   ├── subscriber.go
│   └── dispatch.go
│
├── waterfall/
│   ├── hook.go
│   ├── entry.go
│   ├── registry.go
│   ├── middleware.go
│   └── compose.go
│
└── diagnostics/
    ├── fiber.go
    ├── capability.go
    ├── scope.go
    ├── event.go
    └── waterfall.go
```

---

# 66. MVP 实现范围

第一阶段实现：

```text
Runtime
Daemon
Context
Fiber
Effect
MountTxn

Scope
ScopeTree

CapabilityKey
CapabilityRegistry
Provide
Require
Resolve
DependencyGraph

EventBus
On
Publish

Waterfall
Use
Run
```

这样就已经形成完整 Runtime。

---

# 67. 第二阶段

增加：

```text
Blocked Fiber 自动重启
Provider disappearance propagation
Fiber dependency reverse shutdown
Worker manager
Diagnostics
Runtime trace
```

---

# 68. 第三阶段

增加：

```text
Capability hot replacement
Scope presets
动态配置
Fiber restart policy
Health check
Dependency timeout
```

---

# 69. 第四阶段

再考虑：

```text
WASM Daemon
Subprocess Daemon
Remote Capability
Durable Event
Outbox
Distributed EventBus
Control Plane
```

这些都应该建立在现有 Runtime 语义之上。

---

# 70. 最终架构模型

整个框架最终可以压缩成：

```text
                            Runtime
                               │
            ┌──────────────────┼──────────────────┐
            │                  │                  │
        Fiber Tree         Scope Tree        Communication
            │                  │                  │
        lifecycle           visibility       ┌────┼─────┐
        ownership           routing          │    │     │
            │                  │         Capability Event Waterfall
            │                  │             │
            └──────────┬───────┘             │
                       │                     │
                    Context                  │
                       │                     │
                     Daemon ─────────────────┘
```

Capability 再展开：

```text
Provider Daemon
      │
      ▼
Provider Fiber
      │
      │ Provide[T]
      ▼
CapabilityRegistry
      │
      │ Scope Lookup
      ▼
Require[T]
      │
      ▼
Consumer Fiber
      │
      ▼
DependencyGraph
      │
      ▼
Direct Go Interface Call
```

Event：

```text
Domain Fact
    │
    │ Publish
    ▼
EventBus
    │
scope admission
    │
 ┌──┼─────────┐
 ▼  ▼         ▼
A   B         C
```

Waterfall：

```text
Domain Action
    │
    ▼
Run Hook
    │
    ▼
Global Middleware
    ↓
Parent Middleware
    ↓
Current Middleware
    ↓
Domain Terminal
```

---

# 71. 最终系统执行模型

用一句完整的话描述：

> **Runtime 通过 Daemon 组合系统；Daemon 挂载后形成 Fiber；Fiber 通过 Context 在某个 Scope 下提供和依赖 Capability；Capability 建立系统同步依赖图，并在解析后直接通过 Go interface 调用；领域 Workflow 在必要节点执行 Waterfall，允许其他 Daemon 介入；领域事实完成后通过 EventBus 向 Scope 祖先传播；Fiber 统一拥有 Capability、Listener、Middleware、Worker 等 Effect，并在依赖失效或卸载时按依赖图和生命周期树完成结构化销毁。**

再压缩为：

```text
Daemon
  ↓
Fiber
  ↓
Capability Graph
  ↓
Explicit Domain Workflow
  ↓
Waterfall Extension
  ↓
Domain State Change
  ↓
Event Propagation
```

这才是整个框架真正的主流程。

它既不是：

```text
Everything is Event
```

也不是：

```text
Everything is Plugin
```

而是：

> **Plugin/Daemon 负责组合，Capability 负责依赖，Domain 负责主流程，Waterfall 负责扩展，Event 负责事实传播，Fiber 负责生命周期，Scope 负责可见性。**