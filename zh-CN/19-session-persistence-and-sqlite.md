# 19 Session Persistence 与 SQLite 事实存储设计

状态：Accepted

本文拥有 durable Session facts、cold inspection/load、resume preparation、recovery 编排、write-behind 与 SQLite/sqlc storage adapter 的跨模块设计。Session core 的 Header/Event/surface/live LiveStore 由[10](../session/docs/design.zh-CN.md)拥有；Agent resume 与 API 映射分别由[15](./15-agent-loop-and-request-driver.md)和[16](./16-session-api-gateway-and-live-frames.md)拥有；Projection/Title 由[18](./18-session-projection-and-title.md)拥有；实施状态与验证证据只见[08](./08-implementation-progress.md)。

## 1. 固定源基线

设计以固定 TypeScript commit `47f943859bef60e4160492346772ded9b24f765a` 的下列 owner 为证据：

| TypeScript owner | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/session/session-persistence/src/index.ts` | `session/persistence/contracts.go` | `Persistence` capability、stored/log view 与错误边界 |
| `coordinator.ts` | `session/persistence/session_log_store.go` | live/cold 协调、恢复、revision 与 preparation；Go 以对象身份命名为 `SessionLogStore` |
| `write-behind.ts` | `session/persistence/write_behind.go` | bounded-delay batch、flush、failure retention 与 drain |
| `invariant.ts` | `session/persistence/recovery.go` | Header/Event/seq/turn/tool invariant 与 recovery plan |
| `preparations.ts` | `preparedSessions`、`SessionLogStore` orchestration | bounded ready LRU、unpublished Session reservation、revision validation 与 publication handoff |
| `session-persistence-sqlite/src/index.ts` | `session/persistence/sqlite` | SQLite fact backend、事务、revision 与 row mapping |
| `schema.ts` / `invariant.ts` | `sql/schema.sql`、`schema.go` | schema identity/version、打开时验证与存储不变量 |

Go 不复制 Node file API、TypeScript 类型生成或浏览器 Connection。公共 wire 字段、Session event envelope 与 Service 名 `sessionPersistence` 保持 canonical。源 SQLite package 同时承担插件与后端角色；Go 将插件身份收敛为 `@deepseek-ai/dsh-session-persistence`，把 SQLite 保留为 composition root 内的 `Backend` adapter，避免基础设施实现进入 Service graph。

## 2. 边界与依赖方向

```mermaid
flowchart TD
    API[Session API Gateway] --> P[session/persistence.Persistence]
    LOOP[Agent Factory and Loop] --> P
    P --> LOG[SessionLogStore]
    LOG --> CORE[session LiveStore and Session]
    LOG --> PORT[session/persistence.Backend]
    SQLITE[session/persistence/sqlite] -. implements .-> PORT
    SQLITE --> SQLC[internal/dbsql]
    SQLC --> DB[(SQLite Session facts)]
```

依赖规则：

- `session` core 不依赖 persistence、SQLite、sqlc、API Proxy 或 Agent Loop；
- `Persistence` 是上游 Consumer 使用的完整 durability capability；
- `Backend` 是 `SessionLogStore` 拥有的 storage-only port，adapter 不决定业务恢复；
- SQLite/sqlc 类型止于 `session/persistence/sqlite`，映射后才返回 owner-defined contract；
- API Gateway、Agent Loop 不导入 SQLite driver，不按表结构编排 use case；
- JSONL、SQLite 和未来其他实现若进入，只能作为同一个 `Backend` port 的替换 adapter。

Plugin Runtime 只解析 Session Persistence capability：Factory 解码 `SessionPersistenceConfig`，构造当前内置 SQLite adapter 和 `SessionLogStore`，最终只 `Provide(sessionPersistence)`。SQLite 没有独立 Factory、Manifest、Service key 或依赖结算状态。这样更换 Backend 只改变 composition root 的构造选择，不要求 Agent、API、Workspace 或 Plugin Runtime 认识存储技术。

## 3. LiveStore、Persistence 与 Backend

| 抽象 | 回答的问题 | 生命周期 | 数据 |
| --- | --- | --- | --- |
| `session.LiveStore` | 当前进程有哪些已 attached 的 live Session？ | Plugin Scope / Session attachment | `session.Context`、live membership、created/event/flush/disposed |
| `Persistence` | 跨进程有哪些 durable Session？如何检查、修复并恢复？ | durability plugin | detached Header/Event logical view、preparation |
| `Backend` | 如何原子读取或写入物理 records？ | `SessionLogStore` | stored prefix/suffix、revision、repair marker |

这三者不能合并。LiveStore 中没有 cold record；Persistence 不能替代 live membership；Backend 不能创建 Session、选择 recovery event 或发布业务生命周期。

## 4. Persistence contract

| 方法 | 语义 |
| --- | --- |
| `Create` | 建立尚未 materialize 的 durable identity/cursor；不能覆盖已有事实 |
| `Append` | 验证连续 seq 后追加 detached events；不重新分配 seq |
| `Prepare` | 读取并提交必要 repair，再由 LiveStore 创建唯一 unpublished Session，保留到 Enter/Announce 或 Dispose |
| `Load` | 返回 validated logical log；cold open state 的 recovery 会提交到 Backend |
| `Inspect` | 返回 validated 逻辑视图，但不改写 cold store；可包含按规则推导的临时 closers |
| `ReadFrom` | 从指定 seq 返回逻辑后缀；不是面向消息的 history pagination |
| `List` / `ListSnapshots` | 枚举 cold Header，或 Header 加 source-qualified revision |
| `Locate` / `ReadRaw` | 仅对能暴露 per-Session artifact 的 Backend 可用；SQLite 明确不支持 raw artifact |

`Load` 与 `Inspect` 不带 limit，因为它们验证的是完整 Session log 不变量。`session.history` 的 `beforeSeq/maxMessages` 属于 API consumer，不能下推后改变 recovery 或 surface 语义。`ReadFrom` 使用 `Backend.LoadStoredFrom` 直接 seek 物理后缀，但仍验证 Header、format、起始 seq 连续性和 required event type；它不把部分日志伪装成可独立 replay 的完整 Session。

`Persistence` 的方法参数使用 `requestContext`、`identifier`、`metadata`、`fromSeq`、`beforeSeq` 等业务名表达调用契约。`Persistence.ReadRaw` 返回 `(RawArtifact, error)`：Backend 不支持 raw artifact 时返回 capability error，Session 不存在时返回 `NotFoundError`。storage-only `Backend` 的可选读取统一返回指针：非空表示 record 存在，`nil, nil` 表示不存在，`nil, err` 表示 I/O 或映射失败；不再把 `(value, found, error)` 三元组泄漏给每个调用点。`LoadStoredEventsBefore` 的一次分页读取使用 `EventPage`；原子追加和修复分别使用 `EventBatch` 与 `LogRepair`。Context 与 Session ID 仍作为调用边界和目标身份显式传递。

## 5. 正常写入与 durability

```mermaid
sequenceDiagram
    participant U as Agent/Application
    participant S as Session
    participant L as session.LiveStore
    participant C as SessionLogStore
    participant W as per-Session Writer
    participant B as Backend
    U->>S: Commit EventDraft
    S->>S: coordinator commits seq/time/surface
    S-->>C: session/event
    C->>W: enqueue detached Event
    W->>B: AppendBatch on delay/batch boundary
    U->>L: Flush after committed turn/end
    L->>S: acquire ordered WriteBarrier
    S-->>L: {sessionId, nextSeq}
    L->>C: session/flush(barrier)
    C->>W: flush through barrier and await
    W->>B: commit retained batch
    B-->>U: durable boundary acknowledged
```

Event 在进入 persistence listener 前已是 committed fact，因此 storage failure 不能倒转 Session memory。write-behind 必须保留失败 batch、保持原顺序并让显式 flush 报错；不能推进 durable cursor 或伪报成功。第一次 materialization 的 Header 和首批 Events 必须处于同一 Backend transaction。

正常 Agent driver 在提交 `turn/end` 后调用 `session.LiveStore.Flush`，并在它完成后才成功进入 successor Turn 或 idle convergence。LiveStore 先通过 Session coordinator 取得 ordered `WriteBarrier`，确保该 prefix 的 Event publication 已完成，再以 `session/flush` 并行等待 durability participant。`SessionLogStore` 必须至少把 `Seq < barrier.NextSeq` 的 retained facts 提交给 Backend；可以顺带提交更晚事实，但不能用“当前 pending 已空”替代指定 prefix 校验。Agent Loop 不直接调用 `Persistence` 或 SQLite。

用户取消当前 Turn 时，已提交的 aborted `turn/end` 仍必须越过 durability barrier，因此最终屏障保留 Context value 但不继承 Turn cancellation。其他显式 flush 的调用方取消或 Backend failure 仍返回错误；未落盘 batch 由 writer 保留，供后续 flush、dispose 或 shutdown drain。Session coordinator 不等待 Backend I/O。

每个 Session ID 有独立串行 gate，不同 Session 可并行。同一 live Session 只有一个 writer owner；duplicate listener、不同 seed prefix、CWD 冲突或 durable identity collision 明确失败。

`SessionLogStore` 只编排 use case，不直接持有多种 map 和缓存算法。运行时状态由四个对象按变化原因拆分：

| 状态对象 | 拥有内容 | 变化原因 |
| --- | --- | --- |
| `durableSessions` | durable Header、cursor、materialized 标记与 exact live owner | durable append、cold commit、live attach/detach |
| `liveWrites` | exact `*Session` 到 write-behind controller | live Session publication/disposal |
| `preparedSessions` | bounded ready LRU 与 exclusive reservation | cold inspect/prepare、revision 失效、publication handoff |
| `sessionGates` | per-ID serial gate 与 admission close | 同 ID operation 生命周期、Provider shutdown |

LRU 只保存可重用且尚未发布的完整 object graph；已 reservation 的对象移出 LRU，不能因容量淘汰而破坏 resume identity。`SessionLogStore` 仍负责决定何时 load、验证 revision、repair 和 commit，状态对象不调用 Backend 或产生业务事实。

## 6. Cold load、recovery 与 resume

```mermaid
sequenceDiagram
    participant A as API or Agent Factory
    participant C as SessionLogStore
    participant B as Backend
    participant S as session.LiveStore
    A->>C: Prepare(sessionId)
    C->>B: LoadStored
    B-->>C: Header + committed prefix + marker + revision
    C->>C: validate and derive repair
    C->>B: CommitRepair(marker, closing facts)
    C->>B: verify stable revision when required
    C->>S: Prepare(seed, metadata)
    S-->>C: exact unpublished Session
    C-->>A: Preparation
    A->>S: Enter then Announce
    S-->>C: session/created
    C->>C: bind reservation cursor to live writer
```

Recovery 检查：

- Header identity、format version 与 metadata canonical form；
- Event `seq` 从零连续、payload 为 lossless JSON、surface/provenance 合法；
- required event type 已由当前 build 注册；未知但 `ignorable=true` 的 extension 可跳过；
- torn physical tail 与 committed prefix 分离；
- open Turn/Step 和未闭合 Tool call 按 source invariant 生成 ordinary closing Events；
- repair 后完整日志再次通过相同 invariant。

Backend 只返回 marker 或 corruption 技术事实。`SessionLogStore` 决定删除哪段 torn tail、追加哪些 closers，并通过 `CommitRepair` 请求一个事务完成。`Prepare` 返回的必须是最终 Enter 的同一个 Session 对象；reservation 在 publication 或显式 Dispose 时释放，防止两个 Agent 同时恢复同一 identity。ready cache 使用固定容量 LRU；命中后先比较 durable revision，外部写入、repair 或本地 append 都使旧 source 失效。缓存只复用相同 revision 的 unpublished object graph，不是第二个真相。

## 7. SQLite/sqlc adapter

SQLite 是当前默认 Session fact Backend，不是插件或 projection cache。Projection Cache 使用独立的 `session-projection-cache.sqlite` 和 storage-only adapter，完整边界见[Session Projection Cache 最终设计](./SessionProjectionCache最终设计方案.md)。JSONL 若后续因 raw artifact、可移植导出或运维需求进入，是可替换 Backend；二者不双写，也不形成“JSONL 真相 + SQLite 影子”的强制架构。独立的 `session/query/sqlite` adapter 同样只是可从 facts 重建的查询索引。

### 7.1 目录所有权

```text
session/persistence/sqlite/
├── sql/schema.sql
├── sql/query.sql
├── sqlc.yaml
└── internal/dbsql/
```

`sqlc.yaml` 位于所属领域 adapter，不在仓库根目录。`schema.sql` 与 `query.sql` 是生成输入；`internal/dbsql` 是 repository-private 结果，不能手改或被业务包导入。

### 7.2 Schema

| 表 | 作用 |
| --- | --- |
| `persistence_state` | 单例 store identity，防止 revision 跨数据库误比较 |
| `sessions` | canonical Header fields、incarnation、monotonic revision |
| `events` | 以 `(session_id, seq)` 为主键的 Event envelope |

数据库使用 application id `0x44534850` 与 schema version 15。空数据库可初始化；已有对象但没有正确 identity/version、版本不匹配或 application id 不匹配时拒绝打开，不猜测 migration。

### 7.3 事务与 I/O

- 完整 prefix 读取在同一只读事务中取得 Header 与 Events；
- 后缀读取在同一只读事务中取得 Header、revision 与 `seq >= fromSeq` 的 Events；检测到 torn marker 时拒绝返回不完整后缀；
- `AppendBatch` 原子插入首个 Session row、Events 并递增 revision；
- `CommitRepair` 原子删除 tail、追加 closers 并递增 revision；
- `foreign_keys=ON`、`busy_timeout=5000`、`synchronous=FULL`；journal mode 由 typed config 在允许集合内选择；
- 数据库连接限制为一个，避免 adapter 内无意引入跨连接写并发；
- 新目录和文件使用 owner-only 权限；credentials 不写入数据库。

sqlc 只生成查询调用；事务边界由 `Backend` method contract 与 `SessionLogStore` use case 决定，不能藏进 SQL trigger。Row decoder 验证 JSON、integer range、nullable field、surface operation 和 ignorable marker，再映射成 detached domain types。

## 8. API 与 Agent 集成

- `session.list` 合并 live LiveStore 与 cold Persistence Header，并按 Session ID 去重；cold summary 优先读取 Projection Cache，不为每个 Header 执行完整 `Inspect`；
- `session.history` 对 live Session 读 detached events；cold tail 先由 Projection Cache 从 checkpoint + suffix 得到日志 cut，再用 `ReadEventsBefore` 从该 cut 向前读取最近完整 message groups；older page 只做反向分页，不读 cache；
- `session.create` 指定已有 cold identity 时走 `Prepare` 和 Agent Factory resume，不新建同 ID；
- 其他需要 Agent 的 `session.*` 调用可先恢复 cold Agent，再执行原 use case；
- Agent Loop 只接收已经 prepared 的 Session，恢复最后 Turn、Inbox、surface/header fold，并在下一次模型请求记录 `request/header(reason=resume)`；
- WebSocket reconnect baseline 仍由 list/history 和 Mux high-water 拥有，Persistence 不保存 socket、subscriber、pending callback 或 rpcId。

## 9. 生命周期、取消与失败

Persistence plugin 的 Scope 按 LIFO 停止：先释放 listener、拒绝新 admission，再 flush/关闭所有 writer，最后关闭 Backend。dispose 单个 Session 先 flush，再解除 live writer；durable facts 保留供 cold resume。

调用方 Context 控制 gate 等待、Backend read/write transaction 与显式 flush。定时 batch 使用服务生命周期；取消不能丢弃已经 committed 但尚未落盘的 batch。后台错误进入 observer/reporting，并在后续 flush 再返回；磁盘满、锁冲突、commit failure 和 shutdown timeout 都不能编码为空成功。

## 10. 后续能力进入规则

- 新 Backend 先实现同一 storage-only port，不复制 `SessionLogStore`/recovery；
- migration 只有在明确版本转换与 crash atomicity 后进入；不能自动接受未知 schema；
- projection checkpoint/query index 使用自己的 schema、owner 和 transaction，不向事实表加入 optional global model；
- raw artifact/export 是 capability，不为 SQLite 伪造 per-Session 文件；
- retention、fork、权限、workspace scope 和 search 由对应 use case owner 决定，Backend 只执行明确的 record operations；
- prepared cache、suffix seek、batch tuning 只能作为语义保持的优化；当前 bounded LRU 与 SQLite seek 已进入实现，后续调整不能绕过 revision/invariant。
