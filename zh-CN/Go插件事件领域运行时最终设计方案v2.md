# Go 插件事件领域运行时最终设计方案

## 1. 目标

设计一个面向 Go 服务端、模块化单体和 Agent Runtime 的插件化运行时。

它需要同时解决：

- 模块动态组合
- 能力依赖
- 插件生命周期
- 动态卸载
- Scope 隔离
- 事件传播
- Waterfall / Onion 扩展
- 后台任务管理
- 依赖热变更
- Runtime 可观测性

但必须保证：

> **插件化不能破坏领域聚合。**

因此框架采用明确的职责划分：

```text
Module      → 系统组成
Fiber       → 生命周期
Capability  → 同步能力调用
Event       → 已发生事实传播
Hook        → 可拦截扩展点
Scope       → 可见性与路由
Effect      → 资源回收
Domain      → 业务流程和 invariant
```

核心原则：

```text
Module owns composition.
Fiber owns lifetime.
Scope owns visibility.
Capability owns direct invocation.
Event owns facts.
Hook owns interception.
Domain owns workflow and invariants.
```

---

# 2. 整体架构

```text
                        Application
                             │
                       Domain Module
                             │
               ┌─────────────┼─────────────┐
               │             │             │
          Capability        Hook          Event
         direct call     interception     facts
               │             │             │
               └─────────────┼─────────────┘
                             │
                           Runtime
                             │
              ┌──────────────┼──────────────┐
              │              │              │
         Fiber Tree      Scope Tree     Registries
              │              │              │
          lifetime        visibility    Capability
          ownership        routing       Event
                                         Hook
```

Runtime 本质上维护两棵树：

```text
Fiber Tree
= 运行时生命周期结构

Scope Tree
= 逻辑可见性结构
```

二者不等价。

---

# 3. Module

`Module` 是静态模块定义。

例如：

```text
Order
Payment
HTTP
PostgreSQL
Redis
Risk
Notification
LLM
Memory
```

接口：

```go
type Module interface {
	Name() string
	Mount(*Context) error
}
```

例如：

```go
type OrderModule struct{}

func (m *OrderModule) Name() string {
	return "order"
}

func (m *OrderModule) Mount(ctx *Context) error {
	repo, err := Resolve[OrderRepository](ctx)
	if err != nil {
		return err
	}

	service := NewOrderService(repo)

	return Provide[OrderService](ctx, service)
}
```

`Module`：

```text
只是定义
    ↓ mount
Fiber 才是运行实例
```

即：

```text
Module
  │
  │ Mount
  ▼
Fiber
```

---

# 4. Fiber

## 4.1 定义

Fiber 是：

> **Module 的一次运行时实例，以及它创建的所有资源的生命周期 Owner。**

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
```

核心结构：

```go
type Fiber struct {
	ID   FiberID
	Name string

	Module Module

	Parent   *Fiber
	Children map[FiberID]*Fiber

	Scope *Scope

	State FiberState

	ctx    context.Context
	cancel context.CancelFunc

	effects *EffectStack

	mu sync.Mutex
}
```

---

# 5. Fiber Tree

例如：

```text
Root Fiber
│
├── PostgreSQL Fiber
│
├── Order Fiber
│   ├── capability registration
│   ├── hook registration
│   └── worker
│
├── Payment Fiber
│
└── Notification Fiber
    └── event subscription
```

如果一个 Module 加载子 Module：

```text
Parent Fiber
    │
    └── Child Fiber
```

父 Fiber 销毁：

```text
Parent Stop
   ↓
Child Stop
   ↓
Effects Dispose
```

形成 structured lifecycle。

---

# 6. Fiber 状态机

```text
                  dependency missing
                         │
                         ▼
Pending ─────────────→ Blocked
  │                      │
  │ ready                │ dependency ready
  ▼                      ▼
Starting ─────────────→ Running
  │                      │
  │ error                │ stop
  ▼                      ▼
Failed                Stopping
                         │
                         ▼
                      Stopped
```

第一版不实现复杂 `Suspend/Resume`。

依赖消失默认策略：

```text
required capability disappears
            ↓
dependent Fiber stops
            ↓
enters Blocked
            ↓
capability appears
            ↓
restart
```

---

# 7. Effect

Effect 表示 Fiber 创建出来、必须被回收的东西。

例如：

```text
Capability registration
Event subscription
Hook middleware
Goroutine
Timer
File watcher
Network listener
Child module
```

接口：

```go
type Effect interface {
	Dispose(context.Context) error
}
```

函数包装：

```go
type EffectFunc func(context.Context) error

func (f EffectFunc) Dispose(ctx context.Context) error {
	return f(ctx)
}
```

Fiber 内部维护：

```go
type EffectStack struct {
	items []Effect
	mu    sync.Mutex
}
```

释放顺序：

```text
register A
register B
start C

stop:

C
B
A
```

必须逆序销毁。

---

# 8. Context

Context 是 Module 与 Runtime 交互的运行时句柄。

```go
type Context struct {
	runtime *Runtime
	fiber   *Fiber
	scope   *Scope
}
```

接口：

```go
func (c *Context) Runtime() *Runtime
func (c *Context) Fiber() *Fiber
func (c *Context) Scope() *Scope
func (c *Context) Done() <-chan struct{}
```

Context 自己不要演化成：

```go
ctx.Redis
ctx.DB
ctx.Order
ctx.User
ctx.LLM
ctx.Memory
```

这种 God Object。

业务能力统一通过：

```go
Resolve[T](ctx)
```

在该运行时设计中，`Resolve[T](ctx)` 并不是一个“魔法函数”，而是一个**基于 Scope + CapabilityRegistry 的类型化查找过程**。

它的本质是：

> 在当前 Context 所处的 Scope 链中，按“最近优先原则”查找类型 T 对应的 Capability 实现。

---

## 1. Resolve 的输入

```text
Resolve[T](ctx)
```

等价于：

```text
T = 目标能力类型（interface）
ctx = 当前运行上下文（包含 Scope + Fiber + Runtime）
```

---

## 2. Resolve 的查找路径

当调用：

```go
repo, err := Resolve[OrderRepository](ctx)
```

内部执行流程如下：

```text
1. ctx.Scope() → 获取当前 Scope
2. ScopeChain(scope) → 获取 Scope 链（从当前到 root）
3. 对每一层 Scope：
      查询 CapabilityRegistry
      key = reflect.TypeOf(T)
4. 找到第一个匹配的实现 → 返回
```

---

## 3. Scope 链示例

假设当前结构：

```text
Global
  ↓
TenantA
  ↓
AgentA   ← 当前 Scope
```

注册情况：

```text
Global:   OrderRepository = PostgresRepo
TenantA:  OrderRepository = MySQLRepo
AgentA:   (none)
```

Resolve 过程：

```text
AgentA → 无
TenantA → 命中 MySQLRepo ✅（最近优先）
```

返回：

```go
MySQLRepo
```

---

## 4. CapabilityRegistry 内部结构

核心存储：

```go
map[reflect.Type][]entry
```

entry：

```go
type entry struct {
    value  any
    scope  ScopeID
    owner  FiberID
    seq    uint64
}
```

---

## 5. Resolve 伪代码

```go
func Resolve[T any](ctx *Context) (T, error) {
    var zero T
    typ := reflect.TypeOf((*T)(nil)).Elem()

    scopeChain := ctx.runtime.scopes.Chain(ctx.scope)

    for _, scope := range scopeChain {
        entry := ctx.runtime.capabilities.Lookup(typ, scope.ID)
        if entry != nil {
            return entry.value.(T), nil
        }
    }

    return zero, ErrCapabilityUnavailable
}
```

---

## 6. 关键语义

### ✅ 1. 最近 Scope 优先

不是全局搜索，而是：

```text
Current → Parent → Root
```

---

### ✅ 2. Capability 是“覆盖式”的

不是多实现列表，而是：

```text
同一 Scope 内：冲突
不同 Scope：覆盖
```

---

### ✅ 3. Resolve 不触发创建

它只是：

```text
lookup
```

不是：

```text
factory / lazy init
```

---

## 7. 与 DI 容器的区别

| 传统 DI | Resolve[T]  |
| ----- | ----------- |
| 全局容器  | Scope 层级容器  |
| 单例优先  | 最近 Scope 优先 |
| 显式绑定  | 隐式覆盖        |
| 静态结构  | 动态 runtime  |

---

## 8. 一句话总结

```text
Resolve[T](ctx) = 在 Scope 链中按最近优先查找 T 的 Capability 实现
```

---

如果你愿意，我可以继续帮你补一层更关键的设计：

> Resolve 如何和 Fiber 生命周期绑定（避免“悬挂依赖”问题）\
> 或者\
> Resolve 如何触发 Blocked → Ready 的 reactive restart 机制

获得。

---

# 9. Scope

## 9.1 定义

Scope 负责：

> **可见性 + 路由。**

不负责生命周期。

```go
type ScopeID uint64

type Scope struct {
	ID   ScopeID
	Name string

	Parent   *Scope
	Children map[ScopeID]*Scope

	Depth int
}
```

例如普通服务：

```text
Global
  │
Tenant
  │
Request
```

Agent Runtime：

```text
Global
  │
Preset
  │
Agent
```

模块：

```text
Global
  │
Application
  │
Module
```

---

# 10. Fiber 与 Scope

二者核心区别：

```text
Fiber
= 谁拥有资源

Scope
= 谁能看到资源
```

默认：

```text
registration owner
=
ctx.Fiber()

registration scope
=
ctx.Scope()
```

即：

```text
Context
   │
   ├── Fiber → ownership
   │
   └── Scope → visibility
```

这样大多数情况下二者自然绑定，但架构上保持独立。

---

# 11. Scope Tree

例如：

```text
Global
│
├── Coding Preset
│   ├── Agent A
│   └── Agent B
│
└── Research Preset
    └── Agent C
```

核心操作：

```go
type ScopeTree struct {
	root *Scope

	mu sync.RWMutex
}

func (t *ScopeTree) Child(
	parent *Scope,
	name string,
) *Scope

func (t *ScopeTree) Chain(
	scope *Scope,
) []*Scope
```

例如 Agent A：

```text
Chain =
Global
→ CodingPreset
→ AgentA
```

---

# 12. Layer Store

Scope 本身不会自动隔离数据。

真正负责 Scoped Registration 的是：

```text
LayerStore
```

通用结构：

```go
type LayerStore[K comparable, V any] struct {
	global map[K]V
	scoped map[ScopeID]map[K]V

	mu sync.RWMutex
}
```

例如：

```text
Capability Layers

Global
├── Logger
└── Metrics

CodingPreset
└── LLM

AgentA
└── Memory
```

---

# 13. Scope Merge

读取一个 Scope 的 effective view：

```text
Global
  +
Parent
  +
Current
```

nearest scope wins。

例如：

```text
Global:
LLM → DefaultLLM

Preset:
LLM → CodingLLM

Agent:
LLM → AgentLLM
```

Agent 最终：

```text
LLM → AgentLLM
```

但三份注册都存在。

因此 Scope 更接近：

> hierarchical overlay

而不是：

> 每个 Scope 一套完整 Service Container。

---

# 14. Capability

这里不再设计：

```text
ServiceKey
CapabilityKey
Provider interface
```

业务能力直接使用普通 Go interface。

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
```

这是 Capability。

---

# 15. Capability Registry

Runtime 内部通过 Go type 标识 Capability。

```go
type capabilityEntry struct {
	Type reflect.Type

	Value any

	Scope ScopeID
	Owner FiberID

	Sequence uint64
}
```

Registry：

```go
type CapabilityRegistry struct {
	layers *LayerStore[
		reflect.Type,
		capabilityEntry,
	]

	mu sync.RWMutex
}
```

---

# 16. Provide

模块向 Runtime 提供一个能力：

```go
func Provide[T any](
	ctx *Context,
	impl T,
) error
```

例如：

```go
repo := NewPostgresOrderRepository(db)

Provide[OrderRepository](ctx, repo)
```

Runtime 得到：

```text
Capability:
OrderRepository

Implementation:
PostgresOrderRepository

Owner:
PostgreSQL Fiber

Scope:
Current Scope
```

---

# 17. Resolve

消费者：

```go
func Resolve[T any](
	ctx *Context,
) (T, error)
```

例如：

```go
repo, err := Resolve[OrderRepository](ctx)
```

解析顺序：

```text
Current Scope
    ↓
Parent
    ↓
Global
```

nearest provider wins。

---

# 18. Capability Dependency Graph

Provide / Resolve 自动构造依赖图：

```text
PostgreSQL Fiber
       │
       │ provides
       ▼
OrderRepository
       │
       │ consumed by
       ▼
Order Fiber
```

Runtime 因此知道：

```text
谁提供能力
谁消费能力
Provider 消失影响谁
```

不要求 Module 手写：

```go
Requires() []Dependency
```

---

# 19. Missing Capability

如果：

```go
Resolve[OrderRepository](ctx)
```

找不到：

```go
ErrCapabilityUnavailable
```

Runtime 捕获：

```text
Order Fiber Starting
       ↓
Resolve missing
       ↓
rollback Setup Effects
       ↓
record dependency
       ↓
Fiber Blocked
```

之后：

```text
PostgreSQL Module mounted
        ↓
Provide[OrderRepository]
        ↓
dependency graph lookup
        ↓
wake Order Fiber
        ↓
Mount again
```

形成 Reactive Dependency。

---

# 20. Capability 冲突

同一个 exact scope：

```text
OrderRepository A
OrderRepository B
```

默认直接：

```text
ErrCapabilityConflict
```

不要第一版引入：

```text
priority
weight
random selection
```

因为这会让依赖解析变得隐式。

Scope override 已经足够：

```text
Global Repository
        ↓
Tenant Repository
```

---

# 21. Event

Event 表示：

> **已经发生的事实。**

例如：

```go
type OrderCreated struct {
	OrderID string
	UserID  string
}
```

发布：

```go
Publish(ctx, OrderCreated{
	OrderID: order.ID,
})
```

监听：

```go
On[OrderCreated](
	ctx,
	func(
		callCtx context.Context,
		event OrderCreated,
	) error {
		return notify(event)
	},
)
```

---

# 22. Event Registry

```go
type subscriber struct {
	Type reflect.Type

	Scope ScopeID
	Owner FiberID

	Sequence uint64

	Handler any
}
```

同样：

```text
Scope
= routing

Owner Fiber
= lifecycle
```

---

# 23. Scoped Event Propagation

Scope Tree：

```text
Global
  │
Preset
  │
AgentA
```

AgentA Publish：

```text
AgentA listener    ✓
Preset listener    ✓
Global listener    ✓

AgentB listener    ✗
```

也就是：

```text
Capability visibility:

Global
   ↓
Preset
   ↓
Agent


Event visibility:

Agent
   ↑
Preset
   ↑
Global
```

一句话：

> Registration inherits down.\
> Event propagates up.

---

# 24. Event Dispatch

事件不会：

```text
先全局发布
→ 再二次路由
```

而是发布时直接知道 publisher Scope：

```text
Publish
   ↓
publisher scope
   ↓
calculate ancestor set
   ↓
select listeners
   ↓
dispatch
```

监听器筛选发生在一次 dispatch 内。

---

# 25. Event Dispatch Mode

建议：

```go
type DispatchMode uint8

const (
	DispatchSerial DispatchMode = iota
	DispatchParallel
	DispatchBestEffort
)
```

但是 Event 的语义始终是：

```text
fact already happened
```

Listener 不允许：

```text
修改事实
veto 事实
控制主流程
```

需要这些能力时用 Hook。

---

# 26. Live Event 与 Durable Event

Runtime EventBus 第一版只负责：

```text
in-process live event
```

持久事件单独抽象：

```go
type EventStore interface {
	Append(
		context.Context,
		Envelope,
	) error

	Load(
		context.Context,
		StreamID,
	) ([]Envelope, error)
}
```

不要把：

```text
EventBus
```

和：

```text
Event Sourcing
```

绑定死。

---

# 27. Hook / Waterfall

Hook 表示：

> **某个动作正在发生，可以被扩展、修改、拦截。**

例如：

```text
BeforeOrderCreate
BeforePayment
BeforeHTTPRequest
BeforeToolExecute
BeforeLLMRequest
```

这是和 Event 最重要的区别：

```text
Hook:
尚未完成，可以修改

Event:
已经完成，只能响应
```

---

# 28. Hook 类型

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

Hook：

```go
type Hook[I, O any] struct {
	Name string
}
```

---

# 29. Onion / Waterfall

例如：

```text
Auth
Risk
Promotion
Core
```

执行：

```text
Request
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
```

Middleware 可以：

```text
modify
veto
short-circuit
before/after
```

例如：

```go
func RiskMiddleware(
	ctx context.Context,
	cmd CreateOrder,
	next Next[CreateOrder, *Order],
) (*Order, error) {

	if highRisk(cmd) {
		return nil, ErrRejected
	}

	return next(ctx, cmd)
}
```

---

# 30. Hook Registry

```go
type hookEntry struct {
	Hook reflect.Type

	Scope ScopeID
	Owner FiberID

	Order    int
	Sequence uint64

	Middleware any
}
```

排序：

```text
Order ASC
Sequence ASC
```

保证 deterministic。

不要让执行结果依赖随机插件加载时序。

---

# 31. Domain 主流程

最重要的原则：

> **Hook 是主流程上的扩展点，不是主流程本身。**

例如：

```go
func (s *OrderService) Create(
	ctx context.Context,
	cmd CreateOrder,
) (*Order, error) {

	order, err := RunHook(
		s.runtimeCtx,
		BeforeCreateOrder,
		cmd,
		func(
			ctx context.Context,
			cmd CreateOrder,
		) (*Order, error) {

			order := NewOrder(cmd)

			if err := s.repo.Save(
				ctx,
				order,
			); err != nil {
				return nil, err
			}

			return order, nil
		},
	)

	if err != nil {
		return nil, err
	}

	_ = Publish(
		s.runtimeCtx,
		OrderCreated{
			OrderID: order.ID,
		},
	)

	return order, nil
}
```

完整 workflow 仍然能够从 `OrderService.Create()` 一眼看出来：

```text
BeforeCreate Hook
      ↓
Create Aggregate
      ↓
Persist
      ↓
OrderCreated Event
```

这就是和过度事件化架构的核心区别。

---

# 32. Fiber 与注册行为

所有注册自动成为 Effect。

例如：

```go
On[T](ctx, handler)
```

内部：

```text
register listener
       ↓
create unsubscribe Effect
       ↓
attach to ctx.Fiber()
```

因此：

```text
Fiber Stop
   ↓
unsubscribe listener automatically
```

Provide：

```text
Provide capability
      ↓
registration Effect
      ↓
Fiber Stop
      ↓
remove capability
```

Hook 同理。

---

# 33. Goroutine

插件不能直接随意：

```go
go worker()
```

建议：

```go
Go(ctx, func(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil

		default:
			...
		}
	}
})
```

`Go()`：

```text
Fiber Context
      ↓
child goroutine
      ↓
Task Effect
```

Fiber Stop：

```text
cancel context
      ↓
goroutine exit
      ↓
wait done
```

这样避免插件卸载后 goroutine 泄漏。

---

# 34. Mount 过程

```text
runtime.Mount(module)
        ↓
create Fiber
        ↓
create Fiber context
        ↓
State = Starting
        ↓
module.Mount(ctx)
        ↓
Provide / On / Use / Go
        ↓
temporary EffectStack
        ↓
success?
   ┌────┴─────┐
  yes         no
   │           │
commit      rollback
   │           │
Running   Blocked/Failed
```

---

# 35. Mount 必须事务化

例如：

```text
Provide capability
Subscribe event
Register hook
Start worker
      ↓
worker start failed
```

不能留下前三项。

所以 Starting 阶段：

```text
temporary effects
```

只有 Mount 成功：

```text
commit effects
```

否则：

```text
reverse rollback
```

---

# 36. Stop

Fiber Stop：

```text
Running
   ↓
Stopping
   ↓
cancel context
   ↓
stop child fibers
   ↓
dispose effects in reverse order
   ↓
remove dependency edges
   ↓
Stopped
```

必须保证：

```text
Stop is idempotent
```

---

# 37. Capability 消失

例如：

```text
Postgres Fiber
     ↓ stop
OrderRepository removed
     ↓
dependency graph
     ↓
Order Fiber affected
```

默认：

```text
Order Fiber Stop
       ↓
Blocked
```

如果新的：

```text
SQLite Fiber
```

Provide：

```text
OrderRepository
```

则：

```text
Blocked Order Fiber
       ↓
restart
```

---

# 38. Hot Replacement

以后实现热替换时，不建议：

```text
stop old
↓
start new
```

而建议：

```text
mount new
↓
ready
↓
switch registration
↓
notify dependency graph
↓
stop old
```

即：

> prepare → switch → dispose

---

# 39. Runtime

最终：

```go
type Runtime struct {
	rootFiber *Fiber
	rootScope *Scope

	capabilities *CapabilityRegistry
	events       *EventBus
	hooks        *HookRegistry

	scopes *ScopeTree

	dependencies *DependencyGraph

	sequence atomic.Uint64

	mu sync.RWMutex
}
```

---

# 40. Runtime 最核心的数据结构

```text
Runtime
│
├── FiberTree
│   └── ownership / lifecycle
│
├── ScopeTree
│   └── visibility / routing
│
├── CapabilityRegistry
│   └── synchronous dependencies
│
├── EventBus
│   └── fact propagation
│
├── HookRegistry
│   └── interception pipelines
│
└── DependencyGraph
    └── reactive activation
```

---

# 41. 最终 API

Module：

```go
type Module interface {
	Name() string
	Mount(*Context) error
}
```

Mount：

```go
fiber, err := runtime.Mount(module)
```

Capability：

```go
Provide[T](ctx, implementation)

Resolve[T](ctx)
```

Event：

```go
On[T](ctx, handler)

Publish[T](ctx, event)
```

Hook：

```go
Use[I, O](ctx, hook, middleware)

RunHook[I, O](
	ctx,
	hook,
	input,
	terminal,
)
```

Lifecycle：

```go
Effect(ctx, cleanup)

Go(ctx, worker)
```

Scope：

```go
child := runtime.NewScope(
	parent,
	"tenant-a",
)

scopedCtx := ctx.WithScope(child)
```

Module composition：

```go
ctx.Mount(module)
```

---

# 42. 一个完整系统示例

```text
Root Fiber
│
├── PostgreSQL Module
│      │
│      └─ Provide[OrderRepository]
│
├── Order Module
│      │
│      ├─ Resolve[OrderRepository]
│      └─ Provide[OrderService]
│
├── Risk Module
│      │
│      └─ Use[BeforeCreateOrder]
│
├── Notification Module
│      │
│      └─ On[OrderCreated]
│
└── HTTP Module
       │
       └─ Resolve[OrderService]
```

请求：

```text
HTTP Handler
     │
     ▼
OrderService.Create()
     │
     ▼
BeforeCreateOrder Hook
     │
     ├── Auth
     ├── Risk
     └── Promotion
     │
     ▼
Order Domain
     │
     ▼
Repository
     │
     ▼
OrderCreated Event
     │
     ├── Notification
     ├── Audit
     └── Analytics
```

这里的控制流非常明确：

```text
HTTP → Capability → Domain
```

横切逻辑：

```text
Hook
```

解耦事实：

```text
Event
```

插件生命周期：

```text
Fiber
```

逻辑隔离：

```text
Scope
```

---

# 43. 不应该做的事情

## 不要 Everything is Event

错误：

```text
HTTP
 ↓
CreateOrderEvent
 ↓
Order Listener
 ↓
Repository Listener
```

正确：

```text
HTTP
 ↓
OrderService.Create()
```

---

## 不要 Everything is Plugin Hook

核心领域规则：

```text
order state transition
payment invariant
transaction boundary
```

必须有明确 owner。

不能全部依赖插件组合产生。

---

## 不要把 Context 做成 God Object

错误：

```go
ctx.DB
ctx.Redis
ctx.Order
ctx.Payment
ctx.LLM
ctx.Logger
```

正确：

```go
Resolve[T](ctx)
```

---

## 不要把 Scope 当安全沙箱

Scope 只保证：

```text
logical visibility
event routing
```

不保证：

```text
authorization
memory isolation
process isolation
tenant security
```

---

# 44. 包结构

```text
runtime/
├── runtime.go
├── context.go
│
├── module/
│   └── module.go
│
├── fiber/
│   ├── fiber.go
│   ├── state.go
│   └── effect.go
│
├── scope/
│   ├── scope.go
│   ├── tree.go
│   └── layers.go
│
├── capability/
│   ├── registry.go
│   ├── provide.go
│   ├── resolve.go
│   └── dependency.go
│
├── event/
│   ├── bus.go
│   ├── subscriber.go
│   └── dispatch.go
│
├── hook/
│   ├── hook.go
│   ├── middleware.go
│   └── registry.go
│
├── worker/
│   └── task.go
│
└── diagnostics/
    ├── fiber_tree.go
    ├── scope_tree.go
    └── dependency_graph.go
```

---

# 45. MVP

第一阶段只实现：

```text
Module
Fiber
Effect
Context
Scope
LayerStore
CapabilityRegistry
Provide / Resolve
EventBus
Hook / Waterfall
Go worker
```

第二阶段：

```text
Reactive dependency
Blocked Fiber restart
Hot replacement
Runtime inspection
Fiber tree visualization
Scope tree visualization
Dependency graph
```

第三阶段再考虑：

```text
WASM Module
Subprocess Module
Remote capability
Durable Event
Outbox
Distributed EventBus
Runtime control plane
```

---

# 46. 最终核心模型

可以最终浓缩成：

```text
                         Runtime
                            │
              ┌─────────────┼─────────────┐
              │             │             │
           Fiber          Scope       Registries
              │             │             │
          lifecycle     visibility        │
          ownership       routing         │
              │             │        ┌────┼────┐
              │             │     Capability Event Hook
              │             │
              └──────┬──────┘
                     │
                  Context
                     │
                   Module
```

而整个框架最重要的设计判断是：

> **Module 是组合单元，而不是业务语义单元。**

> **Fiber 是运行实例，而不是业务执行流。**

> **Capability 是明确调用，而不是事件。**

> **Event 是事实，而不是命令。**

> **Hook 是扩展点，而不是 Workflow。**

> **Scope 是可见性，而不是对象实例隔离。**

> **领域服务和领域对象仍然拥有真正的业务 Workflow 与 invariant。**

最终目标不是：

```text
Everything is a plugin.
```

而是：

```text
Everything can be composed,
but every semantic still has a clear owner.
```
