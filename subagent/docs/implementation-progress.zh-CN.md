# Subagent 实现进度

状态：Working Evidence

本文只记录 `subagent` 领域迁移期间的代码状态与可复查证据，不替代仓库级 [08 实施进度](../../zh-CN/08-implementation-progress.md)。需求和架构确认后，再把已验收结果统一并入权威索引。

状态定义：

- `Implemented`：存在非占位实现，但尚未完成该切片全部验证；
- `Go Verified`：实现已由当前 Go 测试直接覆盖；
- `Contract Verified`：已与 feature-local DSH source/fixture 做可重复差分；
- `In Progress`：接口或部分行为存在，完整用例仍不可用；
- `Planned`：只有已接受设计，不得从接口存在推断功能可用。

## 1. 当前基线

| 项目 | 值 |
| --- | --- |
| DSH feature-local source | `b150a551b8d465e31e418e1b2eaf5e79bbb7d28e` |
| Goren 开始实现时 HEAD | `6eeac353b6e55d555530189b1f79f9cd7e70ad9c` |
| 当前验收代码 HEAD | `19aa87d`（主体实现 `eddf35f`） |
| 最后核对日期 | 2026-08-23 |
| 全局基线是否改变 | 否 |

## 2. 能力进度

| ID | 能力 | 状态 | 实现位置 | 当前证据或缺口 |
| --- | --- | --- | --- | --- |
| SA-D01 | 分离的 Service/Provider contract 与 Plugin Manifest | Go Verified | 根包 contract、`runtime/plugin.go`、`plugin/service.go` | `contracts_test.go` 验证窄接口与附加 Provider 能力；Plugin Runtime 测试验证独立 `ProvidedService` 实现可发布且 Plugin 无需实现业务接口 |
| SA-D02 | Agent `SubagentDepth` 传播、复制与边界验证 | Go Verified | `agent/agent.go`、`agentloop/agent.go`、`agentloop/plugin.go` | `agentloop/config_test.go` |
| SA-D03 | descriptor v2 snapshot、strict JSON codec 与 first-event fold | Go Verified | `descriptor.go`、`descriptor_codec.go` | `descriptor_codec_test.go` 覆盖 variant、未知字段、未知 version、detachment |
| SA-D04 | coordinator/report/settlement MessageSource | Go Verified | `message_source.go` | `message_source_test.go` 覆盖 canonical kind/form、authority data 和 summary bound |
| SA-D05 | object output schema detached validation | Go Verified | `tools/schema.go`、`internal/oneshot/service.go` | `tools` schema tests及 one-shot focused test 尚需补齐组合路径 |
| SA-D06 | Provider 注册、顺序、exact unregister 与 veto rollback | Go Verified | `internal/provider/registry.go`、`runtime/events.go` | `runtime_test.go` 覆盖 duplicate、顺序、observer rollback、幂等撤销和同名重注册；并发 race 随全量 race gate 验证 |
| SA-D07 | one-shot Start validation、snapshot、Run publication 与 lifecycle | Go Verified | `internal/oneshot/service.go`、`runtime/events.go` | `runtime_test.go` 覆盖 capability gate、request detachment、startup failure 无 lifecycle、start/end identity；`one_shot_integration_test.go` 验证实际 Run 终态与配对 Event；schema failure、result failure和无效 Run 仍需扩充 |
| SA-D08 | continuable fresh create 与 initial prompt transaction | Go Verified | `internal/continuation/manager_start.go`、`materialization.go` | `manager_test.go` 验证 descriptor seed、depth/options、initial Inbox acceptance 和 lifecycle start；`materialization_drain_test.go` 验证 Inbox 拒绝时回滚、publication 后 scoped drain 截止且不接受 initial prompt |
| SA-D09 | followup、cold resume、authority 与 FIFO | Go Verified | `internal/continuation/manager_delivery.go`、`materialization.go`、`disposal.go` | 原有 resident/cold resume 与 exact parent 测试继续通过；`settlement_delivery_test.go` 重复验证 Followup 不跨越自然 settlement cutoff，并在旧 terminal 完成后恢复同一 durable child 的新 epoch |
| SA-D10 | interrupt、report、settlement | Go Verified | `internal/continuation/manager_delivery.go`、`manager_settlement.go`、`outcome.go`、`output.go` | 原有 interrupt/report 集成测试继续通过；Agent-owned `ConsumedWork` tests 覆盖 claim/cancel/turn/step；真实 AgentLoop + SQLite 集成测试验证 completed、max-tokens、model error 和 parent settlement；teardown failure 不发布不可确认输出 |
| SA-D11 | selected children / descendant drain | Go Verified | `internal/continuation/manager_drain.go`、`activation.go`、`disposal.go` | `manager_test.go` 和 `materialization_drain_test.go` 覆盖 cutoff/barrier/rollback；`drain_order_test.go` 验证嵌套 child-first release；`external_disposal_test.go` 验证 terminal publication 先于 ownership release 和下一 epoch admission |
| SA-D12 | Activation Extension ordered provisioning 与即时精确撤销 | Go Verified | `extension.go`、`internal/extension`、`internal/childscope`、`runtime/plugin.go` | extension tests 覆盖顺序、partial provision rollback、commit invalidation、自撤销、resident 精确撤销和幂等收敛；childscope tests 覆盖后续 part 失败逆序回滚与 cold-resume persona/tool policy；`b1a4dee` 删除共享模块对 continuation DTO 的反向依赖 |
| SA-D13 | Catalog live/persistent listing 与 diagnostics | Go Verified | `catalog.go`、`internal/catalog`、`internal/projection`、`runtime/projections.go` | `service_test.go` 覆盖 live-preferred、creation window、cold fold、diagnostic、ordinary traversal、stable preorder、缺失依赖与 cancellation；projection tests 覆盖 last-wins、timing reset 和 damaged checkpoint rejection |
| SA-D14 | spawn/fork Provider | Go Verified | `spawn`、`fork`、`internal/inprocess`、`internal/lineage` | fork test 覆盖 balanced completed-turn prefix；inprocess tests 覆盖 activation boundary、partial output、cancel mapping、descriptor append 和 authoritative structured capture；lineage tests 覆盖继承、metadata 与 depth 边界 |
| SA-D15 | Tool/control/report Consumer | Go Verified | `tool`、`control`、`report` | Tool tests 覆盖 foreground、continuable background 与无 Jobs 的 one-shot background rejection；control 集成测试分别覆盖 send/list/interrupt；report Extension 通过 child-scoped Plugin 安装 Tool/Prompt，并由真实 Agent Scope 集成测试验证 delivery、durable source 与撤销/关闭收敛 |
| SA-D16 | Factory、默认 assembly 与端到端验证 | Go Verified | 各 `factory`、`internal/assembly/catalog.go`、feature-split `*_integration_test.go` | Factory strict config；默认 assembly 把 contained continuation failure 接入进程 Diagnostics；keyless 集成测试按 one-shot、continuable durability/outcome、cold resume、list、interrupt、report 分文件验证；真实 DeepSeek one-shot 验收继续通过；fork Factory 静态注册但不进入默认 deployment |

## 3. 已执行验证

2026-08-23 当前工作树执行：

```text
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
go test ./subagent/internal/continuation -run '^TestFollowupWaitsForNaturalSettlementAndResumesDurableChild$' -count=100
go test ./subagent -run '^TestContinuableSettlementReports(MaxTokens|ModelFailure)FromAgentLog$' -count=50
go test -race ./subagent/internal/continuation -count=20
go test -race ./subagent ./subagent/internal/continuation -count=10
GOREN_REAL_PROVIDER_TEST=1 go test ./subagent -run '^TestRealProviderForegroundOneShot$' -count=1
```

结果：通过。

最后一项从本地 `.env` 注入进程环境并由 credential owner 解析 `DEEPSEEK_API_KEY`，执行了一次真实 DeepSeek foreground one-shot；测试缺少显式开关或凭据时自跳过，不属于 keyless 自动化门禁，密钥未写入日志、fixture 或 Git。其余结果证明当前 Go 实现通过全仓测试/race、vet、build、命名架构检查；高重复测试直接覆盖自然 settlement 与 Followup、terminal outcome，完整套件继续覆盖 materialization/drain、interrupt 和 resident report shutdown。这仍不是 TypeScript/Go compatibility acceptance。问题、根因和修复证据另见[服务端测试问题记录](./server-test-findings.zh-CN.md)。Jobs、background one-shot、Code Mode structured capture 与 Workflow 没有因此进入实现范围。

## 4. 下一验证门

1. Provider registry：顺序、duplicate、added veto rollback、removed containment、exact stale handle、race。
2. one-shot：所有 capability gate、depth/schema、request detachment、Provider start failure 无 lifecycle、start/end 配对与 observer containment。
3. continuable：为 settlement/FIFO/outcome/final flush 增加固定 DSH fixture 的跨语言差分；当前 source-aligned Go owner 与真实 AgentLoop/SQLite 测试仍不等同于 DSH 差分验收。
4. Catalog：增加与固定 DSH list/projection fixtures 的差分验证和可选 projection-cache acceleration；cache 不是权威读取前提。
5. Provider/Consumer：增加固定 DSH spawn/fork、Tool/control/report fixture 的跨语言差分；当前 Go tests 只证明本地契约。
6. 全量：继续执行 `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、`git diff --check`，并补齐固定 DSH source differential fixtures。

## 5. 进度维护规则

- 每个状态提升必须同时填写实现位置和实际运行的证据；
- 接口存在只能标为 `Planned` 或 `In Progress`，不能标为完成；
- `Contract Verified` 必须命名 DSH source symbol/fixture 和 Go test；
- 当前文档只在领域迁移期间提供细粒度事实，最终以全局 `zh-CN/08-implementation-progress.md` 的统一验收条目为权威；
- 不记录未来日期、预计完成度或无法复查的口头结论。
