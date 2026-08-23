# Subagent 迁移草案：范围与阅读顺序

状态：Draft，未进入权威设计

本文组记录 Subagent 的源实现分析和 Go 迁移候选方案。它不是当前权威范围、路线图或实施证据，不修改也不替代：

- [01 复制范围与兼容基线](../01-porting-scope-and-baseline.md)；
- [05 复制路线图与验收](../05-porting-roadmap-and-acceptance.md)；
- [08 实施进度](../08-implementation-progress.md)；
- [中文设计索引](../README.md)。

待需求和架构确认后，再把被接受的结论统一并入上述文档。本文组固定范围、来源和候选架构，不随每个代码切片更新；细粒度实现事实只记录在 [`subagent` 领域进度](../../subagent/docs/implementation-progress.zh-CN.md)，不代表整体兼容已经验收。

## 1. 分析基线

全局固定基线仍是 `47f943859bef60e4160492346772ded9b24f765a`。本次为分析 Subagent，额外读取本地 `../deepseek-harness` 的最新提交：

```text
b150a551b8d465e31e418e1b2eaf5e79bbb7d28e
```

这只是 Subagent 功能的 feature-local 分析基线，不会静默升级 Goren 的全局兼容基线。正式纳入前必须把 `47f9438..b150a55` 中与 Subagent 有关的变化整理为兼容差异，并由权威范围文档接受。

## 2. 当前研究边界

| 分类 | 内容 | 本轮处理 |
| --- | --- | --- |
| 核心 | `subagent` Service、Provider Registry、continuable Activation、持久化身份、控制、报告、清理、目录查询 | 完整分析并设计首期 Go 架构 |
| 内进程 Provider | `subagent-spawn-in-process`、`subagent-fork-in-process` | 完整分析；首期建议只启用 spawn continuable |
| 模型工具 Consumer | `tool-subagent`、`tool-subagent-control`、`tool-subagent-report` | 完整分析并设计首期契约 |
| one-shot | core one-shot contract、`subagent-in-process-driver`、spawn/fork one-shot 路径 | core contract 与 Runtime 属于核心切片；in-process driver 和具体 Provider 路径属于后续切片 |
| Host Consumer | API Proxy 的 `subagents.list/history/prompt/interrupt` | 完整识别边界；是否纳入首期仍待确认 |
| 明确排除 | `subagent-acp`、`subagent-codex`、`subagent-claude-code`、`subagent-dsh-sdk` | 不迁移、不为其预建实现包 |
| 其他实验 Consumer | Agent Team、Workflow、SDK Server 等 | 仅记录依赖，不进入当前设计 |

`llm/docs/zh-CN/` 是旧 pi 实现的迁移证据，不是本设计的目标 API，也不作为 Subagent 所有权依据。

## 3. 阅读顺序

1. [01 源功能与模块关系](./01-source-capability-analysis.md)：先回答 Subagent 到底拥有哪部分行为、谁主动调用谁、one-shot 与 continuable 如何分工。
2. [02 Continuable 生命周期与持久化语义](./02-continuable-runtime-and-durability.md)：描述创建、续投、恢复、中断、报告、settlement、drain 和查询契约。
3. [03 Go 架构、接口与契约](./03-go-architecture-and-contracts.md)：提出包边界、命名对象、Provider/Consumer 接口、事件、错误和配置。
4. [04 实施、验证与待决项](./04-implementation-and-verification-plan.md)：给出可独立验收的切片、测试矩阵和必须先确认的问题。
5. [05 Agent 创建事务与 Provisioning 边界](./05-agent-creation-transaction.md)：固定 Subagent 组合进入 Agent publication 前的实际 Go seam。

## 4. 当前核心判断

Subagent 是一个独立的核心运行能力，不是若干 Tool 的 DTO 集合，也不应塞进 Agent Loop：

- Agent 只拥有单个 Agent 的 Inbox、运行状态、取消和轮次驱动；
- Session 只拥有 append-only durable log、Header 与 live membership；
- Subagent 拥有父子委派关系、Provider 选择、Activation 驻留、续投授权、跨冷恢复、子树清理和运行期生命周期事件；
- Tool、Host API 和将来的其他入口都是 Consumer，只能调用 Subagent Service，不能各自重做父子校验或恢复算法；
- Provider 只贡献“怎样准备一个子任务”，不能接管核心身份、父子关系、恢复、目录和 settlement。

one-shot 与 continuable 是 Subagent 的两种执行策略，不是两个独立核心。公共 contract 同时包含 `Start`/`Run`/`Result` 和 continuable 操作；in-process driver、structured result capture 和 Tool route 可以分切片实施，但不能通过删除 one-shot API 缩小核心模型。

## 5. 草案约束

- 保留 DSH canonical 名称、事件名、错误码、descriptor 字段和可观察顺序；
- Go 用有状态命名对象表达 Runtime、ContinuationManager、Activation、Provisioning 和 Extension Registry，不翻译 TypeScript 闭包链；
- Service/Provider 等接口声明与 Runtime 用例实现在文件上分离，但仍属于同一个 `subagent` 包和同一领域职责，不建立 `internal/subagent` 形式的第二个领域包；
- Provider 与 Consumer 通过最小接口连接，Agent、Session、Tools、Approval、LLM 继续拥有各自契约；
- 任何已接受消息必须能从 Session log 重建，process-local Activation 不是事实来源；
- 每个注册、挂载和运行期安装都必须有精确、幂等的 disposer；
- 不创建占位包，不为被排除 Provider 预留空实现，不引入第二套 Agent 或 LLM runtime。
