# 25 Context Compaction 实现进度

状态：Implementation Complete；Real-provider Environment Verification 未执行
更新时间：2026-08-24

本文是 Context Compaction 的专用实现进度、证据等级和验收 Gate 记录。[24 Context Compaction 设计](./24-context-compaction.md)拥有架构、上下游契约、事件与交互流程；[08 实施进度](./08-implementation-progress.md)只保留一条 Compaction 总体状态并链接本文。

专项源基线为 DeepSeek Harness `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`（`dsh-v0.1.1-rc.2`），已由[01 复制范围与兼容基线](./01-porting-scope-and-baseline.md)作为 capability-scoped exception 接受；全仓其他能力仍固定在 `47f9438`。

## 1. 状态与证据规则

- **执行状态**：`Completed`、`In Progress`、`Deferred` 或 `Excluded`。
- **证据等级**：`Implemented`、`Go Verified`、`Contract Verified` 或 `Environment Verified`。
- `Completed` 表示当前准入范围内的实现已闭环；`/compact` 属于尚未准入的 Commands Consumer，单独 `Deferred`，不把已实现的 Engine 退回骨架状态。
- 真实 Provider smoke 已有自跳过验收用例，但当前环境没有执行带 credential 的分支；因此只能记为 `Implemented`，不能记为 `Environment Verified`。

## 2. 总体状态

| 能力 | 执行状态 | 当前证据 | 剩余验收 |
| --- | --- | --- | --- |
| Compaction Definition + Token Meter + Pruner + Basic Provider + 默认自动装配 | Completed | Contract Verified | 在有 DeepSeek credential 的隔离环境执行 real-provider smoke |
| Engine 人工 `CompactNow` | Completed | Go Verified | Commands 准入后由独立 Consumer 调用 |
| `/compact` Command Consumer | Deferred | None | Commands capability、command lifecycle 与 adapter 进入范围 |

## 3. 公共前置与 Token Meter

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-D00 | `47f9438..b150a55` 专项差异分类与 scoped baseline | Completed | `01` 记录源 owner、行为差异、TS 专属机制和排除项 |
| CMP-D01 | merge-extensible plugin MessageSource lossless decode | Completed | Go Verified：已知 core form strict，Compaction source round-trip 保真 |
| CMP-D02 | Session-owned 多 Event producer serialization | Completed | Go Verified：`SerializeProducer` 相邻性和并发 producer 测试 |
| CMP-D03 | `b150a55` TypeScript config/event/result/checkpoint vectors | Completed | Contract Verified：`generate-compaction-vectors.ts` 可重复生成，Go 消费 fixture |
| CMP-T01 | 固定 estimator 与 `EstimateMessage` | Completed | Go Verified：UTF-16 code-unit、role/block/tool schema 开销与 safe integer |
| CMP-T02 | request header、Step、Surface、assistant provenance 的 replay fold | Completed | Go Verified：incremental replay、malformed transaction 拒绝和 dispose |
| CMP-T03 | usage anchor、canonical envelope match 与 signed Surface delta | Completed | Go Verified：usage undercut fallback、replace delta 和 detached snapshot |
| CMP-T04 | `tokenUsage`、`contextPressure`、`contextBreakdown` Projection Unit | Completed | Go Verified：optional registration、bounded checkpoint/replay 和 shadow-price replacement |
| CMP-T05 | typed Factory、Service lifecycle 与默认装配 | Completed | Go Verified：strict 空配置、effect rollback/dispose、Catalog/DefaultSpecs |

## 4. Compaction Definition 与 Basic Provider

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-C01 | `Engine`、trigger、result 与 manual failure contract | Completed | Go Verified：provider-neutral interface 与 compile-time Provider assertion |
| CMP-C02 | `start`、`summary`、`end`、`prune` 严格 codec | Completed | Contract Verified：merge extension、malformed payload 和 source vectors |
| CMP-C03 | `CheckpointSource` 与 durable round-trip | Completed | Contract Verified：typed construction、opaque restore 识别和 TypeScript vector |
| CMP-C04 | positional tool-call/result pairing | Completed | Go Verified：非单调 seq Surface、orphan result 与 absent sequence |
| CMP-C05 | cold replay invariant 与 durable lock | Completed | Go Verified：owner/identity/adjacency、failed/open attempt 和 end-seed 清理 |
| CMP-B01 | strict typed config、默认策略和 exact route override | Completed | Contract Verified：source defaults、model policy 与 compact spec vectors |
| CMP-B02 | head range、priced tail 与 tool-pair safe boundary | Completed | Go Verified：positional selection、retention 与并发变更拒绝 |
| CMP-B03 | reconstructable summarization prefix 与 direct `PurposeCompaction` stream | Completed | Go Verified：header/tools/messages/prompt、route override、raw output 和 usage |
| CMP-B04 | start/prepare/summarize/revalidate/summary/replace/end 区间事务 | Completed | Go Verified：同步提交、失败 close、orphan lock 保留和 replayable checkpoint |
| CMP-B05 | checkpoint 安全与严格缩小 | Completed | Go Verified：拒绝 error/aborted/max-tokens/empty/image/non-shrinking output |
| CMP-B06 | `agent/pre-step` pressure Consumer | Completed | Go Verified：no-op、prune/remeasure、有界重试和失败隔离 |
| CMP-B07 | `agent/request-error` overflow recovery | Completed | Go Verified：canonical code、generation-backed retry、budget 与原 failure 保留 |
| CMP-B08 | overflow retry sequence reset 与 cancellation | Completed | Go Verified：assistant success/idle reset，cancellation 优先于 pruner progress |
| CMP-B09 | Plugin/业务对象分离、Factory 与 effect lifecycle | Completed | Go Verified：`Plugin` 不实现 `Engine`，依赖绑定与释放受测 |

## 5. Tool Result Pruner、人工入口与默认装配

| ID | 子目标 | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-P01 | Unicode code-point measure 与 head/marker/tail prune | Completed | Go Verified：默认 `8192/4096/1024`、rune-safe slicing 和缩小不变式 |
| CMP-P02 | rich block 顺序和 strict config | Completed | Contract Verified：非文本 block 原位保留，source marker/config vector 一致 |
| CMP-P03 | stable Surface scan、shadow price 与相邻 replacement | Completed | Go Verified：`SerializeProducer` 内提交 prune fact + replacement |
| CMP-P04 | Tool result 字段保留 | Completed | Go Verified：只改 content，merge-extended data 保真 |
| CMP-P05 | 部分成功、幂等、Plugin/业务分离 | Completed | Go Verified：较早 replacement 不回滚，二次 pass no-op，Plugin 不实现 `Pruner` |
| CMP-M01 | `CompactNow` 通过 Agent maintenance 执行 | Completed | Go Verified：idle admission、latched successor wake 优先、busy/cancelled/changed/summary/commit/persistence 分类 |
| CMP-M02 | selected-span stability、standalone bracket 与 explicit flush | Completed | Go Verified：区间外 mutation 允许，区间内 rewrite 拒绝，闭合后 Flush |
| CMP-M03 | `/compact` 无参数 Command Consumer | Deferred | Commands capability 未准入；不创建占位 package |
| CMP-A01 | Catalog 注册 Token Meter、Pruner 和 Basic Factory | Completed | Go Verified：Catalog 和 strict Factory tests |
| CMP-A02 | `DefaultSpecs` 启用自动 Compaction | Completed | Go Verified：keyless composition 发布 Meter/Pruner/Engine |
| CMP-A03 | 默认 `/compact` Consumer | Deferred | 依赖 `CMP-M03` |

## 6. 验收 Gate

| ID | Gate | 状态 | 证据 |
| --- | --- | --- | --- |
| CMP-G00 | 专项源基线和 owner mapping | Completed | Source Verified：`01`、`24` |
| CMP-G01 | Token Meter append/replace/header/usage/corrupt-log 覆盖 | Completed | Go Verified：包级与 race 测试 |
| CMP-G02 | Event/config/result/checkpoint TypeScript differential | Completed | Contract Verified：已提交 vector + contract-tag regeneration |
| CMP-G03 | 默认 AgentLoop pressure 低阈值 no-op 与高压缩 | Completed | Go Verified：`TestDefaultCompositionCompactsPressureThroughAgentLoop` |
| CMP-G04 | overflow 的 generation retry 与 cancellation 收敛 | Completed | Go Verified：默认 composition 的 retry/cancel E2E |
| CMP-G05 | tool pairing、非单调 seq 和 shrink invariant | Completed | Go Verified：Definition/region table tests |
| CMP-G06 | start 先落地与 Event group 相邻 | Completed | Go Verified：Session producer concurrency + region/pruner tests + race |
| CMP-G07 | auto whole-surface 与 manual selected-span stability | Completed | Go Verified：并发 append/replacement deterministic tests |
| CMP-G08 | summary 失败、取消与 close failure 不伪成功 | Completed | Go Verified：summarizer/region/overflow cancellation tests |
| CMP-G09 | SQLite cold resume 可重建 checkpoint、lock、Meter 和 Projection | Completed | Go Verified：Runtime close/restart + `AgentRegistry.Resume` E2E |
| CMP-G10a | 默认装配无 credential 可运行 | Completed | Go Verified：`TestDefaultCompositionStartsCompactionWithoutCredential` |
| CMP-G10b | 真实 Provider 压缩 smoke | In Progress | Implemented：隔离用例会在无 credential 时 self-skip；当前未获得 Environment Verified 证据 |
| CMP-G11 | 依赖方向、canonical names、Factory disposer 和无平行 runtime | Completed | Go Verified：`tests/architecture`、Catalog/assembly tests |
| CMP-G12 | package README、`24`、`25`、`08` 与索引一致 | Completed | 相对 Markdown 链接校验与 `git diff --check` 通过 |

## 7. 已执行验证与环境边界

已执行的验证包括：

- `go test ./... -count=1`；
- Compaction pressure、overflow、cancellation 和 SQLite cold resume 的 focused race tests；
- `go test -tags=contract ./tests/contract -run TestPinnedCompactionSourceGeneratesCommittedVectors`；
- `go test ./tests/architecture -count=1`；
- keyless default composition acceptance；
- `go vet` 和 `go build` 的相关检查。

真实 Provider 用例 `TestRealProviderCompactsOneRegion` 已实现，但本次只观察到无 credential 自跳过，不宣称真实 DeepSeek 环境已通过。

## 8. 剩余工作

1. 在隔离、有 DeepSeek credential 的环境执行 real-provider smoke，将 `CMP-G10b` 的证据从 `Implemented` 升为 `Environment Verified`。
2. Commands capability 未来准入时，以独立 Consumer 实现 `/compact`；复用现有 `Engine.CompactNow`，不把 command parsing 放进 Basic Provider。
