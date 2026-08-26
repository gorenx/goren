# Session Projection Cache 实施方案

状态：已执行

制定日期：2026-08-26

设计依据：[Session Projection Cache 最终设计方案](./SessionProjectionCache最终设计方案.md)

进度记录：[Session Projection Cache 实施进度矩阵](./SessionProjectionCache实施进度矩阵.md)

## 1. 文档职责

本文只规定实施顺序、模块级修改、破坏性删除、中间 Gate、提交边界和最终验收，不记录完成比例。

实际完成状态、代码证据、测试证据和剩余缺口只更新进度矩阵。

## 2. 实施原则

### 2.1 按模块实施

实施必须依次完成：

```text
Projection Cache 核心
  -> SQLite adapter
  -> Plugin/Factory/Assembly
  -> API list Unit
  -> session.list
  -> session.history
  -> Subagent ChildDirectory
  -> 全量验收
  -> 文档一次性同步
```

不允许先在 API Proxy 写 cache-shaped 临时逻辑，再回头补真实 Cache Service。

### 2.2 不保留过渡性代码

- 不保留两套 Projection Cache Service；
- 不把 checkpoint 同时双写 Session DB 和独立 Cache DB；
- 不保留新旧 Factory 或新旧 record schema 的 runtime wrapper；
- 不用 feature flag 同时运行旧 `session.list` 全 Inspect 和新 cache list 路径；
- 不让 `session/projection` 或 `session/persistence` 暂时承担 Cache Service；
- 不为了分阶段编译保留无调用的 callback、adapter 或 type alias。

Cache 缺失时的权威全量重放是永久 correctness fallback，不是过渡兼容代码。

### 2.3 Plugin 与业务分离

- `CheckpointCache` 是普通 Go 对象；
- `projectioncache/plugin.Plugin` 只适配 Runtime lifecycle 和 Event；
- `projectioncache/sqlite.Adapter` 只实现 CheckpointStore；
- Factory 只解码配置和构造未激活 Plugin；
- Assembly 只注册 Factory 和提供 typed default config。

### 2.4 未经要求不自动提交

每个阶段可以形成可复核 diff，但只有用户明确要求时才创建 Git commit。

## 3. 阶段 P0：固定契约和架构 Gate

### 目标

在生产代码开始迁移前，用编译期或 architecture test 固定依赖方向。

### 修改

1. 在 `tests/architecture` 增加 Projection Cache 边界检查：
   - `session/projection` 不导入 `projectioncache`、SQLite、API Proxy；
   - `session/persistence` 不导入 `projectioncache`；
   - `projectioncache` 核心不导入 `projectioncache/plugin`、Factory 或 SQLite；
   - Agent 和 AgentLoop 不导入 Projection Cache；
   - SQLite adapter 不导入 API Proxy 或 Subagent。
2. 固定新 Plugin canonical name：`@deepseek-ai/dsh-session-projection-cache`。
3. 固定 Factory config JSON 字段：`path`、`journalMode`、`writeEveryEvents`、`writeIntervalMs`。

### Gate P0

- architecture test 能在新包创建后精确检测依赖违规；
- 没有创建 placeholder package 或空 README。

## 4. 阶段 P1：Projection Cache 核心

### 目标

完成不依赖 Plugin Runtime 和 SQLite 的 `CheckpointCache`。

### 目标文件

```text
session/projectioncache/
  cache.go
  read.go
  write.go
  cache_test.go
  read_test.go
  write_test.go
```

文件可按实际职责合并，不为单个函数建文件。`cache.go` 只保留契约、数据结构和对象生命周期；`read.go` 拥有 cached/cold read；`write.go` 拥有 dirty state 和写入顺序。

### 实施内容

1. 定义 `Cache`、`CheckpointStore`、`CheckpointRecord`、`LogIdentity`、`FailureReporter`。
2. 实现 plain JSON 验证和 detached clone，禁止调用者修改内存 index。
3. 实现 `CheckpointCache.Open`：安装 LiveStore、Persistence、Registry、Store，加载 record index，开放准入。
4. 实现 `CachedSnapshot`。
5. 实现 `ColdSnapshot`、身份校验、suffix restore、全量 fallback 和 fail-soft write-back。
6. 实现每 Session object 的 write state、timer 和 single-writer。
7. 实现 checkpoint -> Flush -> Store -> memory 顺序。
8. 实现 detach 收口和 Close 准入/排空。

### 必要测试

- 每 Session 一 record，完整替换；
- 同次 checkpoint 所有 row seq 相同；
- `Changed=false` 仍推进 row seq；
- identity match/mismatch；
- StateVersion match/mismatch；
- cached snapshot 取可用 row 的最小 seq；
- bounded suffix cold restore；
- shrunk log 全量 fallback；
- malformed row 全量 fallback；
- zero Unit 的 one-event probe；
- Session not found；
- write-back failure 不使 read 失败；
- Flush 未完成时 Store 不得写入；
- overlapping threshold/timer/turn-end/detach 只有一个 writer；
- writer 期间新 event 不丢 dirty state；
- Close 停止 timer 并排空 in-flight。

### Gate P1

```text
go test ./session/projectioncache/...
go test -race ./session/projectioncache/...
```

## 5. 阶段 P2：SQLite/sqlc adapter

### 目标

为 CheckpointStore 提供独立、可丢弃、不混入 Session fact DB 的 SQLite 实现。

### 目标文件

```text
session/projectioncache/sqlite/
  adapter.go
  config.go
  schema.go
  rows.go
  sql/schema.sql
  sql/query.sql
  sqlc.yaml
  internal/dbsql/*
```

### 实施内容

1. 定义独立 application ID 和 schema version。
2. 建立 `session_projection_checkpoints` 单表。
3. `LoadAll` 在一个一致读中返回全部 record。
4. `Replace` 使用单行 UPSERT，不做 per-Unit patch。
5. 映射时验证 Session ID、CreatedAt、CWD、row version/seq 和 JSON。
6. 对已确认属于 Cache 的旧 schema 执行整库重建；对外来 DB 拒绝。
7. 运行 sqlc generate/check，不手改 generated package。

### 必要测试

- create/reopen/load-all；
- replace same Session；
- nullable CWD；
- application ID 冲突；
- known old cache schema rebuild；
- invalid JSON/check constraint；
- Context cancellation；
- Close 幂等；
- SQL row 不泄漏到 owner contract。

### Gate P2

```text
go test ./session/projectioncache/sqlite/...
go test -race ./session/projectioncache/sqlite/...
sqlc generate/check
git diff --check
```

## 6. 阶段 P3：Plugin、Factory 与 Assembly

### 目标

把已验证的 Cache 业务对象接入 Plugin Runtime，不把算法移入 Plugin。

### 目标文件

```text
session/projectioncache/plugin/plugin.go
session/projectioncache/factory/factory.go
session/projectioncache/factory/factory_test.go
internal/assembly/catalog.go
internal/assembly/*projection_cache*_test.go
```

### 实施内容

1. Plugin Manifest 提供 `projectioncache.Cache`。
2. Plugin Require `session.LiveStore`、`persistence.Persistence`、`projection.Registry`。
3. Plugin 订阅 `session.EventAppended`和 `session.Disposed`。
4. Event adapter 只调用 `CheckpointCache.Advance/Retire`，不直接写 SQLite。
5. Factory 严格解码 config，验证正整数阈值和 duration 上限。
6. Assembly 注册 Factory，默认路径为 Session DB 同目录下的 `session-projection-cache.sqlite`。
7. 默认值显式写入 `writeEveryEvents=200`、`writeIntervalMs=5000`。
8. `internal/assembly.Diagnostics` 实现 Cache 的 FailureReporter，不复用 Session Persistence 的 failure 类型。

### Gate P3

- Runtime 依赖结算确保 Cache 在 Session/Persistence/Projection 之后启动，在它们之前关闭；
- Factory unknown field/负数/零值/溢出测试通过；
- Apply 部分失败反向关闭已打开 Store；
- Plugin 与业务对象没有合并。

## 7. 阶段 P4：API Proxy 拥有的 list Projection Unit

### 目标

用通用 Projection Unit 表达 `session.list` 的 `blank/lastPromptAt`，不再在 cold list 扫描完整 Event Log。

### 实施内容

1. 在 `apiproxy/session` 定义 `sessionListMetadata` Unit 与严格 codec。
2. Unit key 为 `sessionListMetadata`，`StateVersion=1`。
3. Host Plugin Apply 注册 Unit，保存 exact `UnitHandle`；Dispose 逆序释放。
4. 注册失败时回滚 Host Plugin 后续资源。
5. 增加与固定 DSH 输入等价的 projection contract fixture。

### Gate P4

- direct user message 更新 `lastPromptAt`；
- plugin/command/title 事件不使 blank session 变为 non-blank；
- `turn/start` 使 blank 永久变为 false；
- `Changed=false` 后 row seq 仍推进。

## 8. 阶段 P5：`session.list`

### 目标

删除 cold Session 逐个 `Inspect`，用 Header + CachedSnapshot 构造列表。

### 实施内容

1. `apiproxy/host.Plugin` 将 Cache 作为 optional Service 传入 Session Gateway。
2. `sessionReader` 在消费端定义最小 cache 读接口，不依赖 Cache Plugin concrete type。
3. live row 用 Registry Snapshot；cold row 用 CachedSnapshot。
4. 删除 `visibleSessionSummaries` 的 cold `Persistence.Inspect`。
5. cache miss 使用 Header 构造保守 summary，不重新引入全日志 fallback。
6. Projection 读/解码错误对单 row fail-soft，通过 Host diagnostics 报告。
7. 保留 live-preferred merge；cold 计算期间 Session 变 live 时改用 live row。

### Gate P5

- 大量 cold Sessions 的 list 调用次数为一次 Header list + 内存 cache lookup；
- 测试用 Persistence fake 拒绝任何 `Inspect`；
- cache miss 不隐藏可能非空的 Session；
- 单 row projection 损坏不使 list 整体失败。

## 9. 阶段 P6：`session.history`

### 目标

让首页 Projection 从 checkpoint + suffix 恢复，并与反向分页事件使用同一 cut。

### 实施内容

1. 将 History 分为 tail-page 与 older-page 两条明确路径。
2. tail-page 先解析 live/cold Projection cut `E`。
3. cold Cache 路径用 `ColdSnapshot`，再以 `beforeSeq=E+1` 读页面。
4. 校验页面末尾 seq；日志缩短时重读收敛。
5. live 路径先 Registry Snapshot，再从 Session events 取截止到 `E` 的页面。
6. cache 未组合时保留一条权威全量 `Inspect + Restore`。
7. older page 不调用 Cache/Registry Restore，不返回 Projection block。
8. 删除现有独立 `historyProjections` 中对 cold Session 的无界 `Inspect`。

### Gate P6

- cache 命中时 cold history 不执行全日志 Inspect；
- `projections.asOfSeq == tail page 最后 event seq`；
- older page 不调用 Cache；
- concurrent append 不让 Projection 超过页面 cut；
- concurrent repair/shrink 不返回错位 cut；
- zero-event Session 返回 `asOfSeq=-1`和空 events；
- NotFound 继续映射现有 RPC error。

## 10. 阶段 P7：Subagent ChildDirectory

### 目标

在不改变 ChildDirectory 授权和 diagnostic 语义的前提下，对 cold child 使用 cached identity。

### 实施内容

1. ChildDirectory 定义 consumer-owned `ProjectionCache` 窄接口。
2. Subagent Plugin Manifest 把全局 `projectioncache.Cache` 声明为 Optional Service。
3. Plugin Apply 解析 Cache 并传入 ChildDirectory Enable。
4. `resolveCold` 先读 cached `subagent` value。
5. 实施 `identity.Seq >= SeedLength` gate。
6. miss/null/decode failure/stale seed 继续走现有 Inspect + Restore。
7. 保留每 child 失败隔离和 bounded cold-read concurrency。

### Gate P7

- cache hit 不调用 Inspect；
- ancestor descriptor 不被误当作 current child identity；
- cache 损坏不使整个 children/descendants 查询失败；
- cache absent 不改变现有 ChildDirectory 正确性；
- Subagent 业务包不导入 Cache Plugin adapter。

## 11. 阶段 P8：跨模块验收

### Focused Gate

```text
go test ./session/projection/...
go test ./session/projectioncache/...
go test ./apiproxy/session/...
go test ./apiproxy/host/...
go test ./subagent/internal/childdirectory/...
go test ./subagent/plugin/...
go test ./internal/assembly/...
go test ./tests/architecture/...
```

### Race Gate

```text
go test -race ./session/projectioncache/...
go test -race ./apiproxy/session/...
go test -race ./subagent/internal/childdirectory/...
go test -race ./internal/assembly/...
```

### Full Gate

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

API wire 与 Projection block 变更还必须运行：

- 固定 TypeScript Client 对 `session.list` 和 `session.history` 的 golden/differential fixture；
- Web production build；
- 大 Session 历史的集成性能断言，证明 cold tail 不再因 Projection 读取一定扫描完整日志。

## 12. 阶段 P9：文档一次性同步

只在 P0–P8 完成并确认后执行：

1. 新增真实 package 的 `README.zh-CN.md`，包含职责/非职责、Mermaid 互动图、失败/取消/关闭。
2. 更新 `18-session-projection-and-title.md`。
3. 更新 `16-session-api-gateway-and-live-frames.md`。
4. 更新 `19-session-persistence-and-sqlite.md`。
5. 更新 `08-implementation-progress.md`，只记总体状态和公共证据。
6. 更新 `zh-CN/README.md` 索引和权威关系。
7. 运行 Markdown 链接检查和 `git diff --check`。

## 13. 建议的可复核提交边界

如果用户明确要求提交，按以下边界形成独立 commit：

1. `feat(session): add projection checkpoint cache core`
2. `feat(session): add sqlite projection checkpoint store`
3. `feat(runtime): compose session projection cache plugin`
4. `perf(apiproxy): read session summaries from projection cache`
5. `perf(apiproxy): restore cold history projections from checkpoints`
6. `perf(subagent): resolve cold child identity from projection cache`
7. `docs(session): document projection cache design and evidence`

代码、测试和依赖文件随各自实现 commit；最后文档单独提交。未经用户要求不自动提交。

## 14. 完成定义

仅当以下条件全部满足时，进度矩阵才能标记为完成：

1. 最终设计中 PC-I01–PC-I14 全部由代码或测试固定。
2. 默认 composition 安装 Cache Plugin。
3. `session.list` cold rows 不再逐个 Inspect。
4. `session.history` cold tail Projection 不再无界全量 Inspect。
5. history events/projections 的 cut 一致性有并发测试。
6. Subagent cache hit 和 SeedLength gate 完成。
7. SQLite/sqlc、Plugin shutdown 和 failure containment 完成。
8. focused、race、full Go Gate、TypeScript fixture、Web build 和文档 Gate 均实际通过。
9. 进度矩阵记录真实证据，不用设计文档代替验收。
