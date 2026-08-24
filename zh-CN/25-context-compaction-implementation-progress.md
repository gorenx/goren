# 25 Context Compaction 实现进度

状态：Session 写协调迁移 Completed；Real-provider Environment Verification 未执行
更新时间：2026-08-24

本文是 Context Compaction 的专用实现进度、证据等级和验收 Gate 记录。[24 Context Compaction 设计](./24-context-compaction.md)拥有架构、上下游契约、事件与交互流程；[08 实施进度](./08-implementation-progress.md)只保留一条 Compaction 总体状态并链接本文。

Compaction 已按[Session Core](../session/docs/design.zh-CN.md)迁移到唯一 `Context.Commit`：固定 facts 使用 `Batch`，必须基于 FIFO 头部最新 `Snapshot` 的 start、completion 与 pruning use case 实现 `WritePlan.Build`。旧 `SerializeProducer`、`WriteOperation`、`WriteContext` 与部分提交路径均已删除；本表以下证据来自迁移后的当前代码和重新执行的全仓测试。

专项源基线为 DeepSeek Harness `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`（`dsh-v0.1.1-rc.2`），已由[01 复制范围与兼容基线](./01-porting-scope-and-baseline.md)作为 capability-scoped exception 接受；全仓其他能力仍固定在 `47f9438`。

## 1. 状态与证据规则

- **执行状态**：`Completed`、`In Progress`、`Deferred` 或 `Excluded`。
- **证据等级**：`Implemented`、`Go Verified`、`Contract Verified` 或 `Environment Verified`。
- `Completed` 表示当前准入范围内的实现已闭环；当前范围包含 `/compact` 所需的 Commands 全局 Registry、两个 Remote endpoints 与独立 Consumer，不包含完整 Commands/Attachment/Typert 能力。
- 真实 Provider smoke 已有自跳过验收用例，但当前环境没有执行带 credential 的分支；因此只能记为 `Implemented`，不能记为 `Environment Verified`。

## 2. 总体状态

| 能力 | 执行状态 | 当前证据 | 剩余验收 |
| --- | --- | --- | --- |
| Compaction Definition + Token Meter + Pruner + Basic Provider + 默认自动装配 | In Progress | Contract Verified | Session 迁移及 race/E2E 已完成；只剩 real-provider smoke |
| Engine 人工 `CompactNow` | Completed | Go Verified | start/completion plan、atomic batch 与 ordered flush barrier 已验证 |
| Commands 最小 slice + `/compact` Consumer | Completed | Go Verified | command facts 已迁移为固定 `Batch`，E2E 随全仓测试通过 |

## 3. 公共前置与 Token Meter

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-D00 | `47f9438..b150a55` 专项差异分类与 scoped baseline | Completed | `01` 记录源 owner、行为差异、TS 专属机制和排除项 |
| CMP-D01 | merge-extensible plugin MessageSource lossless decode | Completed | Go Verified：已知 core form strict，Compaction source round-trip 保真 |
| CMP-D02 | Session-owned 固定 `Batch` 与状态相关 `WritePlan` | Completed | Go Verified：两者通过唯一 `Context.Commit` 进入同一 FIFO；全仓 race 与 Session 原子性测试通过 |
| CMP-D03 | `b150a55` config/event/result/checkpoint shape | Completed | Contract Verified：基于固定源 symbol 与 descriptor 的 Go golden/表驱动用例 |
| CMP-T01 | 固定 estimator 与 `EstimateMessage` | Completed | Go Verified：UTF-16 code-unit、role/block/tool schema 开销与 safe integer |
| CMP-T02 | request header、Step、Surface、assistant provenance 的 replay fold | Completed | Go Verified：incremental replay、malformed transaction 拒绝和 dispose |
| CMP-T03 | usage anchor、canonical envelope match 与 signed Surface delta | Completed | Go Verified：usage undercut fallback、replace delta 和 detached snapshot |
| CMP-T04 | `tokenUsage`、`contextPressure`、`contextBreakdown` Projection Unit | Completed | Go Verified：optional registration、bounded checkpoint/replay 和 shadow-price replacement |
| CMP-T05 | typed Factory、Service lifecycle 与默认装配 | Completed | Go Verified：strict 空配置、effect rollback/dispose、Catalog/DefaultSpecs |

## 4. Compaction Definition 与 Basic Provider

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-C01 | `Engine`、trigger、result 与 manual failure contract | Completed | Go Verified：provider-neutral interface 与 compile-time Provider assertion |
| CMP-C02 | `start`、`summary`、`end`、`prune` 严格 codec | Completed | Contract Verified：merge extension、malformed payload 和 Go golden cases |
| CMP-C03 | `CheckpointSource` 与 durable round-trip | Completed | Contract Verified：typed construction、opaque restore 识别和 Go round-trip cases |
| CMP-C04 | positional tool-call/result pairing | Completed | Go Verified：非单调 seq Surface、orphan result 与 absent sequence |
| CMP-C05 | cold replay invariant 与 durable lock | Completed | Go Verified：owner/identity/adjacency、failed/open attempt 和 end-seed 清理 |
| CMP-B01 | strict typed config、默认策略和 exact route override | Completed | Contract Verified：source defaults、model policy 与 Go config cases |
| CMP-B02 | head range、priced tail 与 tool-pair safe boundary | Completed | Go Verified：positional selection、retention 与并发变更拒绝 |
| CMP-B03 | reconstructable summarization prefix 与 direct `PurposeCompaction` stream | Completed | Go Verified：header/tools/messages/prompt、route override、raw output 和 usage |
| CMP-B04 | start/prepare/summarize/revalidate/summary/replace/end 区间事务 | Completed | Go Verified：`startPlan` 在 FIFO 头部校验；`completionPlan` 原子提交 summary/replacement/end 或单个 failure end；region tests 覆盖成功、Surface 变化、失败与关闭 |
| CMP-B05 | checkpoint 安全与严格缩小 | Completed | Go Verified：拒绝 error/aborted/max-tokens/empty/image/non-shrinking output |
| CMP-B06 | `agent/pre-step` pressure Consumer | Completed | Go Verified：no-op、prune/remeasure、有界重试和失败隔离 |
| CMP-B07 | `agent/request-error` overflow recovery | Completed | Go Verified：canonical code、generation-backed retry、budget 与原 failure 保留 |
| CMP-B08 | overflow retry sequence reset 与 cancellation | Completed | Go Verified：assistant success/idle reset，cancellation 优先于 pruner progress |
| CMP-B09 | Plugin/业务对象分离、Factory 与 effect lifecycle | Completed | Go Verified：`Plugin` 不实现 `Engine`，依赖绑定与释放受测 |

## 5. Tool Result Pruner、人工入口与默认装配

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-P01 | Unicode code-point measure 与 head/marker/tail prune | Completed | Go Verified：默认 `8192/4096/1024`、rune-safe slicing 和缩小不变式 |
| CMP-P02 | rich block 顺序和 strict config | Completed | Contract Verified：非文本 block 原位保留，source marker/config Go cases 一致 |
| CMP-P03 | stable Surface scan、shadow price 与相邻 replacement | Completed | Go Verified：`pruningPlan.Build` 使用 FIFO 头部 `Snapshot` 构造完整 batch；相邻性和 merge-extended payload 均受测 |
| CMP-P04 | Tool result 字段保留 | Completed | Go Verified：只改 content，merge-extended data 保真 |
| CMP-P05 | 原子失败、幂等、Plugin/业务分离 | Completed | Go Verified：任一候选构造失败时整个 pruning batch 不提交，二次 pass no-op，Plugin 不实现 `Pruner` |
| CMP-M01 | `CompactNow` 通过 Agent maintenance 执行 | Completed | Go Verified：idle admission、latched successor wake 优先、busy/cancelled/changed/summary/commit/persistence 分类 |
| CMP-M02 | selected-span stability、standalone bracket 与 explicit flush | Completed | Go Verified：completion plan 只在 FIFO 头部校验选区；manual close 后 Flush 等待 ordered committed-prefix barrier |
| CMP-M03 | `/compact` 无参数 Command Consumer | Completed | Go Verified：独立 `Compact` 业务对象、manual result mapping 与 `sourceCommandId` |
| CMP-M04 | Commands Registry 与 `command/run`/`command/done` | Completed | Go Verified：handler await 位于两次固定 `Batch` commit 之间，不占用 Session coordinator |
| CMP-M05 | `commands/list`/`commands/execute` Remote adapter | Completed | Go Verified：descriptor wrapper、ordinary Agent fence、absent value、image schema 与 RPC error |
| CMP-A01 | Catalog 注册 Token Meter、Pruner 和 Basic Factory | Completed | Go Verified：Catalog 和 strict Factory tests |
| CMP-A02 | `DefaultSpecs` 启用自动 Compaction | Completed | Go Verified：keyless composition 发布 Meter/Pruner/Engine |
| CMP-A03 | 默认 Commands 与 `/compact` Consumer | Completed | Go Verified：Factory Catalog、DefaultSpecs、Service publication 与注册可见性 |

## 6. 验收 Gate

| ID | Gate | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-G00 | 专项源基线和 owner mapping | Completed | Source Verified：`01`、`24` |
| CMP-G01 | Token Meter append/replace/header/usage/corrupt-log 覆盖 | Completed | Go Verified：包级与 race 测试 |
| CMP-G02 | Event/config/result/checkpoint 固定源契约 | Completed | Contract Verified：Go golden、strict codec、round-trip 与 malformed cases |
| CMP-G03 | 默认 AgentLoop pressure 低阈值 no-op 与高压缩 | Completed | Go Verified：`TestDefaultCompositionCompactsPressureThroughAgentLoop` |
| CMP-G04 | overflow 的 generation retry 与 cancellation 收敛 | Completed | Go Verified：默认 composition 的 retry/cancel E2E |
| CMP-G05 | tool pairing、非单调 seq 和 shrink invariant | Completed | Go Verified：Definition/region table tests |
| CMP-G06 | start 先落地与 Event group 相邻 | Completed | Go Verified：Session FIFO/atomic batch tests、region 与 pruner tests 随全仓 race suite 通过 |
| CMP-G07 | auto whole-surface 与 manual selected-span stability | Completed | Go Verified：迁移后的并发 Surface 变化和 selected-span deterministic tests 通过 |
| CMP-G08 | summary 失败、取消与 close failure 不伪成功 | Completed | Go Verified：summarizer/region/overflow cancellation tests |
| CMP-G09 | SQLite cold resume 可重建 checkpoint、lock、Meter 和 Projection | Completed | Go Verified：Runtime close/restart + `AgentRegistry.Resume` E2E |
| CMP-G10a | 默认装配无 credential 可运行 | Completed | Go Verified：`TestDefaultCompositionStartsCompactionWithoutCredential` |
| CMP-G10b | 真实 Provider 压缩 smoke | In Progress | Implemented：隔离用例会在无 credential 时 self-skip；当前未获得 Environment Verified 证据 |
| CMP-G11 | 依赖方向、canonical names、Factory disposer 和无平行 runtime | Completed | Go Verified：`tests/architecture`、Catalog/assembly tests |
| CMP-G12 | package README、Session Core、`24`、`25`、`08` 与索引一致 | Completed | Contract Verified：已按最终 `Commit` / `WritePlan` / FIFO baton 契约校准，并通过本地 Markdown 链接检查 |
| CMP-G13 | `/compact` 真实 HTTP success/failure/cancel 闭环 | Completed | Go Verified：正常路径的嵌套顺序、取消路径的独立收敛、HTTP 200 Remote error、Provider cancel、lock close 与 persistence |

## 7. 已执行验证与环境边界

Session 写协调迁移后已执行的验证包括：

- `go test ./... -count=1`；
- `go test -race ./... -count=1`；
- Compaction pressure、overflow、Commands cancellation 和 SQLite cold resume 的 focused repeated tests；
- `go test ./tests/architecture -count=1`；
- `go vet ./...` 与 `go build ./...`；
- keyless default composition acceptance；
- Web `pnpm install --frozen-lockfile`、现有 Vitest 和生产构建；这些只验证 Web 自身，不作为 `/compact` Remote 的必要证据。

真实 Provider 用例 `TestRealProviderCompactsOneRegion` 已实现，但本次只观察到无 credential 自跳过，不宣称真实 DeepSeek 环境已通过。

## 8. 剩余工作

1. 在隔离、有 DeepSeek credential 的环境执行 real-provider smoke，将 `CMP-G10b` 的证据从 `Implemented` 升为 `Environment Verified`。
