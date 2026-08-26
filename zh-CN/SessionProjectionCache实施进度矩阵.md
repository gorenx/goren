# Session Projection Cache 实施进度矩阵

状态：实现完成，Go/Race/Build 已验证

最后核对：2026-08-26

分支：`refactor/agent-lifecycle`

设计依据：[Session Projection Cache 最终设计方案](./SessionProjectionCache最终设计方案.md)

实施顺序：[Session Projection Cache 实施方案](./SessionProjectionCache实施方案.md)

## 1. 文档职责

本文只记录当前工作分支上的真实完成状态、实现证据、测试证据和剩余缺口，不重复设计决策或实施步骤。

本矩阵拥有 Projection Cache 的细项状态和证据；全局 [`08-implementation-progress.md`](./08-implementation-progress.md)只保留能力汇总和公共验证结果。

## 2. 状态和证据等级

执行状态：

- `待实施`：设计已确定，生产代码未修改；
- `进行中`：已有实质代码变更，但阶段 Gate 未全部通过；
- `待验证`：目标代码存在，但要求的 focused/race/integration Gate 未实际运行；
- `已完成`：目标代码、直接调用方和阶段 Gate 全部完成；
- `阻塞`：存在需要外部决策或环境变化才能解除的条件；
- `已推迟`：明确不进入当前实施范围。

证据等级：

- `Source Verified`：已核对固定 DSH commit 的 symbol、测试和调用点；
- `Documented`：只有设计或实施文档，不声明生产能力；
- `Implemented`：当前源码已存在实现，但不声明全部验证通过；
- `Go Verified`：相关 focused 或 full Go tests 已实际运行通过；
- `Race Verified`：适用的 race Gate 已通过；
- `Contract Verified`：固定 TypeScript fixture/golden 已通过；
- `Acceptance Verified`：focused、race、full、contract、build 和文档 Gate 全部通过。

## 3. 当前总览

| 范围 | 待实施 | 进行中 | 待验证 | 已完成 | 阻塞 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 设计与源核对 | 0 | 0 | 0 | 3 | 0 |
| Cache 核心 | 0 | 0 | 0 | 6 | 0 |
| SQLite/Factory/Plugin | 0 | 0 | 0 | 5 | 0 |
| API Proxy | 0 | 0 | 0 | 4 | 0 |
| Subagent | 0 | 0 | 0 | 2 | 0 |
| 验收与文档同步 | 0 | 0 | 0 | 4 | 0 |

当前结论：Projection Cache Service、独立 SQLite store、默认 Plugin 装配及 API list/history、Subagent ChildDirectory 三个消费路径均已实现。cache miss、row 不兼容和 cache write failure 仍保留权威日志恢复，不形成第二事实源。

## 4. 已完成的设计与源核对

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| PC-D01 | 固定 DSH `session-projection-cache` 职责、record、写入点和 cold ladder | 已完成 | Source Verified | 固定 commit `47f943859bef60e4160492346772ded9b24f765a` 的 `src/index.ts`、`src/spec.ts`、`tests/cache.spec.ts` |
| PC-D02 | Goren 职责边界、数据不变量和调用关系 | 已完成 | Documented | [`SessionProjectionCache最终设计方案.md`](./SessionProjectionCache最终设计方案.md) |
| PC-D03 | 破坏性实施顺序、模块 Gate 与完成定义 | 已完成 | Documented | [`SessionProjectionCache实施方案.md`](./SessionProjectionCache实施方案.md) |

## 5. 已有前置能力

| ID | 前置能力 | 当前状态 | 证据等级 | 代码证据 |
| --- | --- | --- | --- | --- |
| PC-B01 | Projection Unit/Registry/live fold/checkpoint | 已实现 | Implemented | `session/projection/registry.go`、`types.go` |
| PC-B02 | RestoreFloor/ViewCheckpoint/Restore | 已实现 | Implemented | `session/projection/restore.go` |
| PC-B03 | Session durable Flush boundary | 已实现 | Implemented | `session/memory_store.go`、`session/registration.go`、`session/persistence/session_log_store.go` |
| PC-B04 | Persistence suffix read `ReadFrom` | 已实现 | Implemented | `session/persistence/contracts.go`、`session_log_store.go`、`sqlite/adapter.go` |
| PC-B05 | Persistence reverse tail read `ReadEventsBefore` | 已实现 | Implemented | `session/persistence/contracts.go`、`session_log_store.go`、`sqlite/adapter.go` |
| PC-B06 | API cold history whole-message reverse pagination | 已实现 | Implemented | `apiproxy/session/read.go`、`read_test.go` |

本表只表示当前源码存在；本次文档任务没有重跑这些模块的 Go/race/full Gate，因此不把证据等级提升为当前轮次 Verified。

## 6. Cache 核心进度

| ID | 交付项 | 状态 | 证据等级 | 预期实现证据 |
| --- | --- | --- | --- | --- |
| PC-C01 | Cache/CheckpointStore/Record/Identity 契约 | 已完成 | Go Verified | `session/projectioncache/cache.go`、`cache_test.go` |
| PC-C02 | 内存 record index、detached clone 与同生命周期 record replacement | 已完成 | Race Verified | `session/projectioncache/cache.go`、`read.go`、replacement tests |
| PC-C03 | CachedSnapshot 身份/版本/最低 watermark | 已完成 | Go Verified | `session/projectioncache/read.go`、`TestCachedSnapshotUsesIdentityCompatibleRowsAndMinimumCut` |
| PC-C04 | ColdSnapshot suffix/full fallback/write-back | 已完成 | Go Verified | `session/projectioncache/read.go`、cold restore/shrink/zero-Unit/failure tests |
| PC-C05 | threshold/interval/turn-end/detach single-writer | 已完成 | Race Verified | `session/projectioncache/write.go`、`write_test.go` |
| PC-C06 | Close 准入、timer 停止与 in-flight 排空 | 已完成 | Race Verified | `session/projectioncache/cache.go`、close tests |

## 7. SQLite、Plugin 与 Assembly 进度

| ID | 交付项 | 状态 | 证据等级 | 预期实现证据 |
| --- | --- | --- | --- | --- |
| PC-S01 | 独立 SQLite schema/application identity | 已完成 | Go Verified | `session/projectioncache/sqlite/schema.go`、`sql/schema.sql`、adapter tests |
| PC-S02 | sqlc LoadAll/Replace adapter | 已完成 | Go Verified | `session/projectioncache/sqlite/adapter.go`、`sql/query.sql`、私有 `internal/dbsql`；sqlc 重生成无差异 |
| PC-P01 | Plugin adapter 发布 Cache 并翻译 Session Events | 已完成 | Go Verified | `session/projectioncache/plugin/plugin.go`、assembly dependency settlement |
| PC-P02 | 严格 Factory config 与 Diagnostics adapter | 已完成 | Go Verified | `session/projectioncache/factory/factory.go`、factory tests、`internal/assembly/diagnostics.go` |
| PC-P03 | 默认 Assembly 路径、Factory 注册与 PluginSpec | 已完成 | Go Verified | `internal/assembly/catalog.go`、assembly tests、cold restart E2E |

## 8. API Proxy 进度

| ID | 交付项 | 状态 | 证据等级 | 预期实现证据 |
| --- | --- | --- | --- | --- |
| PC-A01 | API-owned `sessionListMetadata` Unit 与 codec | 已完成 | Go Verified | `apiproxy/session/list_projection.go`、`list_projection_test.go` |
| PC-A02 | Host Plugin Unit registration 和 Cache dependency | 已完成 | Go Verified | `apiproxy/host/plugin.go`、assembly tests |
| PC-A03 | `session.list` cold row 移除逐 Session Inspect | 已完成 | Go Verified | no-Inspect、conservative miss 和 cache decode tests |
| PC-A04 | `session.history` cut-first ColdSnapshot + reverse page | 已完成 | Go Verified | `history_cache_test.go`、`session_persistence_e2e_test.go` |

## 9. Subagent 进度

| ID | 交付项 | 状态 | 证据等级 | 预期实现证据 |
| --- | --- | --- | --- | --- |
| PC-U01 | ChildDirectory consumer-owned cache 窄接口与 optional wiring | 已完成 | Go Verified | `subagent/internal/childdirectory/service.go`、`subagent/plugin/plugin.go` |
| PC-U02 | cached identity + SeedLength gate + per-child fallback | 已完成 | Go Verified | `resolve.go`、`cache_test.go`；缓存快路径与日志恢复路径已拆分 |

## 10. 验收与文档进度

| ID | 交付项 | 状态 | 证据等级 | 预期证据 |
| --- | --- | --- | --- | --- |
| PC-V01 | focused Go tests | 已完成 | Go Verified | Cache、SQLite、API、Subagent、Assembly、Architecture tests 均通过 |
| PC-V02 | race/full Go Gate | 已完成 | Race Verified | `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...` |
| PC-V03 | sqlc 与 Web build | 已完成 | Implemented | cache sqlc 重生成无差异；`cd web && pnpm run build` 通过；本次未改变 wire schema |
| PC-V04 | package README 与全局文档一次性同步 | 已完成 | Documented | 三个 package README 及 `16`、`18`、`19`、`08`、`zh-CN/README` |

## 11. 设计外能力与仓库级缺口

### 11.1 明确未进入本次范围

跨进程同 Session 并发写、并发 `ColdSnapshot` singleflight、checkpoint GC 和容量驱逐仍不在本次范围；这些不是当前实现 TODO。

### 11.2 Contract-tag 测试基础设施

当前 checkout 的多个 `//go:build contract` 测试仍导入已不在仓库中的 `tests/contract/fixture`，因此 `go mod tidy` 会尝试把它当作外部 module package 并失败。普通与 race Go suites、vet、build 不启用该 build tag，均已通过。该问题独立于 Projection Cache，修复时应统一处理 contract suite，而不是在本模块添加替代 fixture。

## 12. 当前验证记录

本次实际执行并通过：

- `go test ./session/projectioncache/... ./apiproxy/session ./subagent/internal/childdirectory ./subagent/plugin ./internal/assembly ./tests/architecture`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `sqlc generate -f session/projectioncache/sqlite/sqlc.yaml`
- `cd web && pnpm run build`
- `git diff --check`

`go mod tidy` 未通过，原因见 11.2；没有把它记录为成功 Gate，也没有为通过检查而修改 contract-tag 测试。

## 13. 阻塞与待决策

当前没有阻塞 Projection Cache 运行或默认装配的架构决策。以下项已在最终设计和代码中固定：

- 每 Session 一个 CheckpointRecord；
- 每 Unit 一个 `{ver, seq, val}` row；
- 不增加重复的 record-level `AsOfSeq`；
- 独立 SQLite cache DB；
- 完整 record 替换；
- Cache 核心/Plugin/SQLite adapter 分离；
- `turn/end` 和 detach 强制写；
- `session.list` 使用 API-owned Projection Unit；
- `session.history` 使用 cut-first ColdSnapshot；
- ChildDirectory 使用 cached identity + SeedLength gate；
- 默认 composition 必须安装 Cache，扩展组合保留权威 fallback。
