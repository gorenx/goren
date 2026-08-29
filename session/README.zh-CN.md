# Session 领域模块

`session/` 集中承载 Session core 及其 persistence、projection、title、query 能力。Core 权威设计见[Session Core、唯一写协调与生命周期](./docs/design.zh-CN.md)，全仓实施状态见[08](../zh-CN/08-implementation-progress.md)；Projection/Title 见[18](../zh-CN/18-session-projection-and-title.md)，Persistence/SQLite 见[19](../zh-CN/19-session-persistence-and-sqlite.md)，Query/Search 见[23](../zh-CN/23-session-query-and-search.md)。本 README 只提供代码近邻导航，不复制 Core 契约。

Session 持久化并投影唯一 [`agentmessage`](../agentmessage/README.zh-CN.md) 协议的完整 JSON；本包拥有 append-only event、surface、replacement 与 recovery，不重新定义 Message、source 或 content。

## 目录与职责

| 路径 | 职责 | 不拥有 |
| --- | --- | --- |
| `session` | Header、append-only Event log、model-visible surface、live `LiveStore` 与生命周期事件 | Agent loop、API frame、数据库格式、repair policy |
| `session/persistence` | durable `Persistence` 服务、storage-only `Backend` port、write-behind、cold load 与 recovery 编排 | SQLite row/SQL、Agent 执行、Session seq 分配 |
| `session/persistence/sqlite` | SQLite Header/Event fact adapter、schema、transaction、revision、sqlc row mapping | Turn/Step/Tool repair 决策、projection fold |
| `session/query` | live-preferred corpus、exact read/filter/title/surface/trace、search reconciliation 与 cursor policy | browser visibility、SQLite row、canonical fact 写入 |
| `session/query/sqlite` | disposable SQLite FTS5 index、ranking、literal query、snippet、generation 与 sqlc mapping | Session recovery、live precedence、API wire policy |
| `session/projection` | 通用 projection unit registry、live fold、snapshot、checkpoint/restore 与 change feed | 具体 projection key、API DTO、数据库事务 |
| `session/projectioncache` | exact Session live checkpoint、per-ID record、cold suffix restore 与 Cache 生命周期 | Session facts、live membership、Release 状态机、Plugin Runtime |
| `session/title` | `session/title` 事实、fallback、rename/refresh、Provider 调度、First/All-Prompts LLM 策略与 title projection | 具体 LLM adapter、HTTP、wire handler、持久化 adapter |

Go 调用方对 Persistence 使用较短的 `sesspersist`，对其 SQLite adapter 使用 `sesssqlite`；公开包路径、领域类型与 Service 名不因导入缩写改变。Projection/Title 仍按各自调用点保留清晰的领域别名。

## 总体工作原理

```mermaid
flowchart LR
    A[Agent or Application] -->|Commit Batch or state-dependent WritePlan| B[Session Core]
    B -->|committed session/event| C[SessionLogStore]
    C -->|storage-only Backend calls| D[(SQLite facts)]
    B -->|committed session/event| E[Projection Registry]
    F[Title Service] -->|append session/title| B
    E -->|whole-value change| G[API Proxy]
    B -->|detached facts| G
    H[Agent Factory or API Gateway] -->|cold Prepare/Inspect| C
    C -->|prepare exact seed| B
    Q[Session Query Service] -->|live-preferred reads| B
    Q -->|cold observation| C
    Q -->|reconcile/search| I[(Disposable FTS5 index)]
    X[SessionSearchGateway] -->|fixed scope + visibility| Q
```

### Core

Session 的 durable Event 类型和 Plugin Runtime 的进程内 Event 是两套明确契约：Session Event 由具名 event key 构造 `EventDraft`，再由唯一 `Commit(context.Context, WritePlan)` 边界分配连续 `seq/time` 并规划 Surface transition；Plugin Event 由 `plugin.EventOf[E]` / `plugin.Publish` 分发，不进入 Session log。Event Sourcing 由 Session/Persistence owner 决定，Plugin Runtime 不保存或重放它。

其他领域只持有 `session.Context`、`LiveStore` 和 exact `Handle`，不能取得私有 `sessionContext`、`eventLog` 或 `sessionLifecycle`。`sessionContext` 是 `Context` 的具体对象，只单向组合 Event log 与 lifecycle；`eventLog` 只负责 seed、append-only Event、Surface 与 detached reads；`sessionLifecycle` 自己拥有单 Session FIFO、phase、exact membership token、publication eligibility 和共享 Release attempt。`memoryStore` 只拥有进程内 exact membership index、进入顺序和 Store `open/closing/closed` 生命周期，不被 Session 回指；`session.Plugin` 只适配 Service 发布并实现 Core 消费的 typed `eventPublisher`。完整状态机和交互流程见[Core 权威设计](./docs/design.zh-CN.md#8-livestore-与状态机)。

普通 producer 用 `session.Batch(drafts...)` 创建固定 plan，再调用 `Context.Commit`。只有完整 drafts 无法在 admission 前确定、必须基于 FIFO 队首最新 `Snapshot` 计算的业务 use case 才实现 `WritePlan.Build`；一个 use case 实现一次，不是每个 Event 实现。`Build` 一次返回完整 batch，`eventLog` 只暂存本次新 Event，并在必要时复制 Surface nodes 来验证全部 transition，成功后原子 append；它不会为每次固定写复制完整历史，也不存在 request-local Event FIFO 或部分 batch。LLM、Tool、网络、文件和数据库 I/O 必须在 Session FIFO 外执行。

### Persistence 与 SQLite

`session.LiveStore` 只回答当前进程有哪些 live Session；`session/persistence.Persistence` 回答跨进程事实、cold inspection、恢复与 resume preparation。具体 `SessionLogStore` 是有状态 Go 对象：监听 `Created`、`EventAppended`、`FlushRequested`、`Disposed`，编排 write-behind、load 与 repair；一个 exact live persistence state 关联 Session object、writer 与 durable cursor，prepared LRU/reservation 和 cold per-ID gate 保持独立。`Backend` 只映射 records、revision、repair marker 和事务。

Core `sessionContext` 与 `SessionLogStore` 不是同一层：前者是一个 exact Session 的公开能力对象，内部 lifecycle 排定 Commit、Flush 与 Release；后者是跨多个 Session 的 persistence coordinator，实现 `Persistence` capability 并调用 storage-only `Backend`。两者不共享状态或 mutex，只通过 committed `session/event` 和带 `WriteBarrier` 的 `session/flush` 握手。`SessionLogStore` 不能发起 Release 或修改 Session lifecycle，Core 也不能直接读取 durable cursor 或调用 SQLite。

正常 Agent Turn 在提交 `turn/end` 后调用 `session.LiveStore.Flush`。Flush 与 Commit、Release 进入同一个 Session FIFO；Flush 在队首取得 `WriteBarrier`，发布并等待 `session/flush` 后才归还 baton。`SessionLogStore` 至少将该 prefix 的 retained facts 提交给 Backend。这样 Agent Loop 不认识 SQLite，Release 不能越过仍在执行的 Flush，而不同 Session 仍可并行。

SQLite adapter 使用以下领域内结构：

```text
session/persistence/sqlite/
├── sql/schema.sql
├── sql/query.sql
├── sqlc.yaml
└── internal/dbsql/
```

从仓库根目录运行 `sqlc generate -f session/persistence/sqlite/sqlc.yaml`。根目录不保存 sqlc 配置，生成类型也不离开 adapter。第一次 batch 在同一事务中创建 Session row、追加 Event rows 并递增 revision；repair 在同一事务中删除 torn tail、追加 `SessionLogStore` 已决定的 closing facts。SQLite 不分配 seq，也不解释业务状态。

### Query 与 Search

`session/query.Service` 同时观察 live LiveStore 和 durable Persistence；同 ID Header 必须兼容，Event log 始终 live-preferred，同时保留 live/persisted availability。Exact read、filter、title、surface、event window 与 trace 直接在 detached logical log 上执行，不依赖 FTS。

Full-text search 才进入 `session/query.Index`。默认 SQLite adapter 保存可删除的 `indexed_sessions` 与 FTS5 documents；Service 根据 live identity/seq 或 persisted revision 计算 reconciliation delta，adapter 在事务内 replace/remove 并推进 generation。Opaque cursor 绑定 Service instance、normalized request 与 generation，相关 corpus 改变时返回 stale cursor。

Query SQLite 结构：

```text
session/query/sqlite/
├── sql/schema.sql
├── sql/query.sql
├── sqlc.yaml
└── internal/dbsql/
```

从仓库根目录运行 `sqlc generate -f session/query/sqlite/sqlc.yaml`。固定 schema/query 由 sqlc 生成；可选 metadata filter 的 FTS statement 留在 adapter 内参数化构造。该数据库不是 Session facts，删除后可从 LiveStore/Persistence 重建。

### Projection 与 Title

Projection `Registry` 只驱动 domain-owned `Unit`，不理解具体 state；`Changed` 显式控制 whole-value change，checkpoint 是可重建缓存而非事实。Title Service 把 fallback、Provider 或 user rename 统一追加为 `session/title`，以日志中的最新合法事件作为真相，再由 title unit 投影给 API Proxy。

Fallback observer 先返回，再由 Title Service 自己拥有的生命周期任务通过正常 `Commit` 重新准入；Session 不提供 after-event 写入口。Rename 会 supersede 自动生成并 pin；Refresh 明确解除 pin。First-Prompt 与 All-Prompts 是同一个 Session Title 插件内由 typed config 选择的 `LLMProvider` 策略对象，不是独立插件。该对象经 `LLMStreamer` 消费 provider-neutral LLM runtime，在分发前提交 `session/title-llm-request`，并负责输入/输出预算、超时、流组装与文本校验；`LogService` 仍拥有 revision、取消、迟到结果拒绝和最终 `session/title` 事实。

## 上下游与依赖方向

- 上游：Agent/Application use case、Agent Factory/Loop resume、API Gateway cold list/history/create/search、Plugin Scope 生命周期；
- 下游：Agent reconstruction、API Proxy、SQLite fact `Backend` 与 disposable Query `Index`；
- `session/title -> session/projection + llm + session`，`session/persistence -> session`，`session/query -> session + session/persistence`；core 不反向依赖子包；
- wire DTO、Echo/WebSocket、LLM adapter、sqlc row 和 SQLite driver 不进入 Session contract。

JSONL 与 SQLite 是同一 `Backend` port 的可替换事实存储方案，不是业务层的两个 Session 模型；当前默认组合选择 SQLite。`session/query/sqlite` 是独立可重建查询索引，不与事实库混为一体。

## 生命周期、错误与取消

Core `Create` 由 Prepare、Enter、Announce 组成；created listener 可 veto 并进入同一个 terminal Release。Event commit 后 observer error 被包含和报告，不回滚事实。publication 期间同步重入同一 Session 会拒绝。`sessionLifecycle` 在 Release admission 时安装唯一 `releaseAttempt`，从这一刻拒绝新 Commit/Announce/Flush；此前已经准入的操作继续按 FIFO 完成。`memoryStore` 随后执行 final Flush、移除 exact `liveEntry`、按 Created publication 状态决定是否发布 Disposed，再把结果交回 lifecycle。final flush 或 exact removal 失败后进入 `sessionReleaseFailed`，保持不可写、不可见并允许 Release 重试。多个 Release caller 通过同一 attempt 和 `sync.Once` 共享一个 terminal worker。

调用方以错误身份判断 exact membership：`ErrNotAttached` 表示传入的 Session 没有属于当前 `LiveStore` 的 exact membership，包括已经 detach、同 ID 的旧对象和非本包创建的对象；调用方不依赖错误文本分支。

`memoryStore` 使用 `storeOpen -> storeClosing -> storeClosed` 管理 Store 级准入和关闭，不创建第二个 lifecycle 对象。`Close` 使用共享 `closeAttempt`；进入 `storeClosing` 后拒绝新的 Prepare/Enter，并按进入顺序逆序 Release 已有 exact `liveEntry`。关闭失败不会重新开放准入，后续 `Close` 创建新 attempt，只重试仍处于 `sessionReleaseFailed` 的 Session。`session.Plugin.Dispose` 是 Runtime 适配入口，业务 Store 本身不依赖 Plugin 生命周期。

Persistence 对同一 Session ID 的 load/append/repair 串行化。write-behind 失败保留未提交 batch；显式 flush 和插件关闭会等待 drain，`Disposed` 只验证 final flush 已留下空 writer 并释放 exact owner。Scope teardown 先停止 listener admission，再 drain writer，最后关闭 Backend。未知 required event、非连续 seq、Header 不一致或不可解释状态明确失败；是否删除 torn tail、追加哪些 recovery facts只由 `SessionLogStore` 决定。

Query Service 串行化 observe/reconcile/search，使一个 page 与 cursor 绑定同一 index generation；取消、Persistence failure、source conflict、index failure、invalid/stale cursor 分开分类。Scope teardown 等待正在执行的 Query operation 后关闭 derived index。

调用方 Context 控制锁等待、读取、事务与 flush。Session dispose、Provider dispose、用户 rename 或更新 revision 会取消过期 title work；迟到结果在 append 前再次校验。慢客户端不参与这些事务，由 API Proxy 自己的 delivery queue 隔离。
