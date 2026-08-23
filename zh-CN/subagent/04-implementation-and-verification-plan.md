# Subagent 实施、验证与待决项

状态：Draft

本文把[Go 候选架构](./03-go-architecture-and-contracts.md)拆成可验证切片，并保留尚未接受的产品与架构选择。本文不随每个代码切片更新；逐项代码事实只见[领域实现进度](../../subagent/docs/implementation-progress.zh-CN.md)。在一个可验收版本完成前，Subagent 不进入默认组合，也不更新[08 实施进度](../08-implementation-progress.md)。

## 1. 迁移前兼容差异

全局固定基线是 `47f943859bef60e4160492346772ded9b24f765a`，本草案分析的是最新 `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e`。实施前先生成 feature-local 差异表，至少逐项归类：

- core API、descriptor version、projection schema 与 stable error；
- continuation cleanup、Extension revocation、cold resume 与 drain 修复；
- report delivery 和 settlement notice；
- Host `subagents.*` contract；
- fork Provider 与 preset/bundle composition 漂移；
- 只影响明确排除的外部 Provider、Agent Team、Workflow 或 SDK 的变化。

每一项标记为“纳入语义”“Go 无关机制”“明确排除”或“待决”，之后才允许升级权威基线。不能因为本地 DSH checkout 更新就整体追随。

## 2. 实施切片

### D0：源 fixtures 与行为矩阵

产出：

- descriptor v2、projection、MessageSource、Tool schema、lifecycle event 的 TypeScript fixtures；
- core 操作的 caller/callee 与成功边界表；
- `47f9438..b150a55` Subagent 差异表；
- one-shot、Host API、fork、sandbox/preset 的范围决策记录。

验收：每个 compatibility claim 同时指向 DSH symbol/fixture 和实际 Go owner/test；接口或局部实现不被误报为整条行为完成。

### D1：前置 owner contract

改动：

- [Agent](../14-agent-registry-inbox-and-events.md)：`agent.Options.SubagentDepth` validation、copy 与 resume 传播（结构与 owner-local tests 已建立）；
- [Approval](../17-approval-user-questions-and-interaction-gateway.md)：owner-owned `SeedDelegationPolicy`；
- LLM：Subagent typed MessageSource 的注册/codec 方案；
- Agent provisioning：`agent.Provisioner`/可选 `agent.Provisioning`/`agent.Scope`/exact `agent.Effect` 和 publication 前 Provision/Commit/rollback（基础 seam 已建立；resident registration 撤销由 D5 验证）。

验收：每个前置能力有 owner-local test；尚未创建的 Subagent 不迫使 Agent Loop 加 feature branch。

### D2：durable facts 与 read model

实现：

- `subagent/descriptor.go` 严格 variant codec；
- descriptor/timing projection；
- depth 与 Header/descriptor invariant；
- live-preferred `listChildren`、`listDescendants`；
- typed diagnostic rows 和 core errors。

验收：目录查询不调用 Agent Create/Resume；能识别 one-shot descriptor 但不声称 resumable；live 创建窗口、cold corrupt 与 unavailable 分开处理。

### D3：Runtime 与 Provider Registry

实现：

- `Runtime` Service Plugin；
- Provider registration/order/get/list 与 exact disposer；
- provider-added veto rollback、provider-removed containment；
- `subagent/spawn` continuable Provider 与 strict Factory。

验收：重复名称失败，撤销旧 handle 不影响后来注册；Provider 移除不终止已接受 Activation。

### D4：ContinuationManager

实现：

- per-child lock、Activation 和 parent ownership；
- transactional `StartContinuable`；
- resident `Followup` 与 Persistence cold resume；
- exact direct-parent/live-ancestor authorization；
- `Interrupt` keep-inbox no-wait；
- waiting/settled 派生、start/end epoch；
- settlement notice、best-effort Flush、exact Handle dispose；
- scoped/global child-first drain 和聚合错误。

验收：成功边界严格为 Inbox acceptance；接受前失败不泄漏 ID/Agent/Session/Provisioning；接受后请求取消不撤销工作；同 child 的 deliver/resume/close 线性化。

### D5：child composition 与 report

实现：

- `extension.Registry`、per-creation Provisioner/Provisioning、构建批次与 resident installation 索引；
- delegation prompt、persona 和 Tool restriction overlays；
- approval delegation seed；
- `subagent/report` Activation Extension、Tool 与 `next-step`/`quiet` delivery。

验收：安装失败完整回滚；撤销立即卸载 resident installation；构建/撤销竞态不能发布失效安装；report 只从 exact live child 发给 direct live parent。

### D6：模型 Tool Consumers

实现：

- `subagent/tool` 的 one-shot/continuable 策略路由；
- `send_message`、`interrupt_agent`、`list_agents`；
- Provider add/remove 驱动 Tool registration；
- typed config 和稳定错误展示。

验收：`run_in_background=false` 路由 one-shot `Start`，continuable background 路由 `StartContinuable`，不 silent reroute；Tool Consumer 不直接读取 Persistence 或构造 Activation。

### D7：Host API（条件切片）

只有产品范围明确纳入浏览器 Subagent 后才实施：

- API Proxy `subagents.list/history/prompt/interrupt`；
- direct-child catalog、history pagination/projection、parent availability；
- RPC error mapping、request cancellation 和 time-zone provenance；
- TypeScript Client golden/differential tests；
- Web 入口另按现有 core-flow UI 边界评审，不自动复制 DSH Web 产品。

Core 不依赖本切片。未纳入时，Tool 驱动的 Subagent 仍是完整核心能力，只是浏览器没有控制面。

### D8：Fork（条件切片）

先解决最新源中 fork Provider 注释/base bundle 与 CLI preset 的 continuable 配置冲突，再实施：

- completed-turn balanced prefix；
- `seedLength`；
- descriptor/policy 必须位于 seed suffix；
- inherited prompt/report Tool 对 prefix reuse 的影响；
- 是否进入默认 composition。

在结论前允许实现实验 Provider test，不允许默认启用或宣称与最新源 preset 一致。

### D9：one-shot 行为

核心切片包含 `Capabilities`、`Run`、`Result`、stop reason、`Start`、Provider base `Start`、capability/depth/schema validation、detached request、Run publication 与 lifecycle observer。独立后续切片包含：

- diagnostic bound、result failure/invalid Run 的完整测试；
- in-process driver 与 structured output capture；
- spawn/fork one-shot 路径；
- background Jobs settlement；
- Tool foreground route。

`subagent-acp`、`subagent-codex`、`subagent-claude-code`、`subagent-dsh-sdk` 仍保持排除，不因 D9 自动进入范围。

## 3. 核心测试矩阵

| 领域 | 必测行为 |
| --- | --- |
| Descriptor | v2 两种 variant；缺失/null/unknown；seed 中旧 descriptor 被 own suffix 覆盖；lossless fixture round trip |
| Provider | 顺序、duplicate、added veto rollback、removed containment、exact/idempotent disposer、并发 register/unregister |
| Start | depth、ID collision、Provider failure、provisioning/Extension failure、Agent create failure、Inbox reject、每阶段逆序回滚 |
| Followup | running enqueue、waiting wake、cold inspect/resume、non-resumable、provider absent still resumes、acceptance/cancel race |
| Authority | exact direct parent、live ancestor、self/sibling/stale instance/非祖先拒绝；provenance 不授权 |
| Interrupt | keep inbox、fire-and-return、claimed work 不重排、absent/idle no-op、不 cold resume |
| Extension | 有序安装、部分失败、立即撤销、构建期撤销、later registration 不追装、release failure aggregation |
| Report | exact child、direct parent、parent absent、next-step vs quiet、多次报告、不结束 turn |
| Settlement | idle + no child gate、final assistant output、notice before ownership release、flush failure still dispose、run ID 配对 |
| Drain | 深层 child-first、多个 branch、单 branch failure 不短路、聚合错误、draining 拒绝新 admission |
| Listing | live/cold 优先、创建窗口、省略 vs diagnostic、稳定排序、deep iterative traversal、ordinary/one-shot intermediate、取消 |
| Tool | strict config、provider lifecycle、schema 文案、maxDepth、one-shot/continuable 精确路由、控制工具只做薄映射 |

并发测试必须包含 `go test -race`，尤其覆盖 per-child lock、settlement/followup、Extension revoke/build、provider replacement 与 nested drain。

## 4. Compatibility fixtures

需要从最新 DSH 固定而不是手写猜测的 fixture：

- `subagent/descriptor` Event 的 exact JSON 字段存在性和 discriminant；
- `subagent`、`subagentTiming` projection 输出；
- `coordinator`、`subagent-report`、`subagent-settled` MessageSource；
- `subagent/provider-added`、`provider-removed`、`start`、`end` 事件 payload；
- `subagent`、`send_message`、`interrupt_agent`、`list_agents`、`report` Tool schema/result；
- 若 D7 纳入，`SubagentListEntry`、address、receipt 和四个 `subagents.*` RPC success/error envelope。

Go 测试需要区分：

- wire parity：JSON 字段、缺失/null、错误码和顺序；
- semantic parity：acceptance、授权、取消、恢复、settlement；
- Go-only safety：race、idempotent disposer、typed config 和 dependency direction。

## 5. Architecture checks

在 `tests/architecture/` 增加仓库级规则：

- Agent/Session/Tools/Approval/LLM 不依赖 `subagent`；
- `subagent` core 不依赖 spawn/fork/tool/control/report/API Proxy/Echo；
- API Proxy 是唯一 wire DTO owner；
- 被排除 Provider 包不存在；
- one-shot contract 可以先存在，但 Runtime 明确返回 pending、未进入默认组合；不存在伪成功 `Run`、空 driver 或 Jobs compatibility layer；
- Runtime/Manager/Registry 是命名对象，不以 closure/object-literal service 替代；
- keyed struct literal 一行一个字段，标识符不与 type/function 发生大小写无关碰撞。

## 6. 每个切片的验证命令

实现切片先跑 owner-focused tests，再按风险跑：

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```

涉及 Host wire 时再运行 TypeScript-to-Go golden/differential tests；涉及 Web 时按仓库约定构建 Web。仅文档变更只要求 Markdown link validation 和 `git diff --check`，不能据此声明 Go 行为已验证。

代码与设计文档在同一任务中改变时分别提交：implementation/test/dependency 为代码提交，设计/index/progress 为文档提交。

## 7. 当前待决项

| 编号 | 问题 | 当前建议 | 不确认的风险 |
| --- | --- | --- | --- |
| Q1 | 首期是否纳入 Host `subagents.*` 与 Web 控制面 | Core/Tool 先独立；Host 作为 D7 显式选择 | 协议范围被静默扩大，或核心被 UI 阻塞 |
| Q2 | fork continuable 是否受支持/默认启用 | 源配置漂移解决前只默认 spawn | prefix reuse 和 report/prompt 顺序不一致 |
| Q3 | one-shot 行为完成前 Tool foreground 怎么处理 | 整个 Tool Plugin 不进入默认组合；行为完成后严格路由 `Start` | pending 路径被误当生产能力，或 silent reroute 改变语义 |
| Q4 | sandbox 委派 | 等 sandbox owner contract | 不受控 effect 或虚假继承声明 |
| Q5 | Agent Preset composition | 等真实 composition contract，不只复制名称 | child 的 tools/prompt/provider 组合不一致 |
| Q6 | projection cache | 首期 live snapshot + cold inspect；以后只做性能优化 | 提前引入重复 read model 和 invalidation |
| Q7 | 多进程 Activation lease | 首期明确 process-local ownership，不宣称跨进程互斥 | 两个 Host 同时 cold resume 同一 child |
| Q8 | Extension immediate revocation seam | D1 用 spike/contract test 证明 exact child unload | report/tool 已撤销但 resident child 仍可调用 |
| Q9 | one-shot descriptor 的只读支持 | decoder/catalog 支持，execution 不支持 | 历史合法 child 被错误标为 corrupt |

## 8. 完成定义

首期 Subagent 只有在下列条件同时满足时才可标记完成：

- durable continuable spawn 能从模型 Tool 启动、续投、报告、中断、settle 和 cold resume；
- nested child ownership 与 child-first drain 在 race test 下成立；
- accepted work、descriptor、policy和消息可由 Session log 重建；
- Provider/Extension/tool 所有副作用有精确幂等 disposer；
- listing 不激活 Agent，并对损坏/不支持/暂不可用给出稳定分类；
- one-shot 行为、fork、Host、sandbox/preset 等未完成项被明确标注，不存在 silent fallback；
- compatibility fixtures 指向确定的 DSH commit 和 Go test；
- 权威范围、架构索引、路线图和实施进度已在最终确认后统一更新。
