# Agent Message 协议重构进度矩阵

状态：实施完成，集成与真实 Provider 验收通过

更新时间：2026-08-27

设计依据：[Agent Message 协议重构设计方案](./AgentMessage协议重构设计方案.md)

实施顺序：[Agent Message 协议重构实施方案](./AgentMessage协议重构实施方案.md)

全仓汇总：[08 实施进度](./08-implementation-progress.md)

## 1. 记录规则

- **执行状态**：`Planned`、`In Progress`、`Completed`、`Deferred` 或 `Excluded`。
- **证据等级**：`None`、`Documented`、`Implemented`、`Go Verified`、`Contract Verified` 或 `Environment Verified`。
- 只有本轮实际执行的检查记录为 Verified；真实 Provider smoke 不从历史结果或 keyless tests 推断。
- 本矩阵拥有 Agent Message 专项状态；`08` 只保留阶段汇总和公共验证结果。

## 2. 最终边界

- `agentmessage` 是 Message、MessageSource、ContentBlock、MessageID、CallID、ContextForm 与 ContextSnapshotSection 的唯一物理 owner；
- `llm` 只拥有 GenerateOptions、ToolSchema、StreamChunk、finish、usage、failure、Runtime 与 Adapter，并单向消费 `agentmessage`；
- Agent、Session、Tools、Compaction、Subagent、API projection、System Prompt、Approval、UserQuestions 与 Session Title 全部使用 `agentmessage`；
- 旧 `llm.Message` family、alias、forwarding constructor、兼容 facade、双 codec、feature flag 和未使用的 `BlockAssembler.AssembleMessage` 均不存在。

## 3. 总览

| 阶段 | Planned | In Progress | Completed | Deferred/Excluded |
| --- | ---: | ---: | ---: | ---: |
| 决策与契约冻结 | 0 | 0 | 5 | 0 |
| 核心 owner 迁移 | 0 | 0 | 6 | 0 |
| LLM consumer 与 Provider | 0 | 0 | 6 | 0 |
| 主链与扩展调用方 | 0 | 0 | 6 | 0 |
| Architecture 与文档 | 0 | 0 | 5 | 0 |
| 验收 Gate | 0 | 0 | 8 | 0 |

## 4. 决策与契约冻结

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-D01 | Message 是 Agent interaction fact，不是 LLM request DTO | Completed | Documented | [设计方案第 2–4 节](./AgentMessage协议重构设计方案.md#2-最终决策) |
| AM-D02 | 使用公开根 package `agentmessage` 并保持 canonical 类型名 | Completed | Implemented | `agentmessage/` 与[包说明](../agentmessage/README.zh-CN.md) |
| AM-D03 | 冻结 Message/source/content durable JSON | Completed | Contract Verified | `agentmessage/testdata/pinned-message-fixtures.jsonl` 与 `TestPinnedHarnessMessageFixtures` 覆盖 core、opaque、空 snapshot 与 field ordering |
| AM-D04 | 冻结 Session surface event payload | Completed | Contract Verified | `session/agentmessage_contract_test.go` 固定 `user/message` 与 `assistant/message` payload bytes |
| AM-D05 | 冻结 DeepSeek serialization 与 request reconstruction | Completed | Contract Verified | `llm/deepseek/serialize_test.go`、`llm/runtime_test.go`、AgentLoop/Session focused tests |

## 5. 核心 owner 迁移

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-C01 | 建立真实 `agentmessage` package、package doc 与 public contract | Completed | Implemented | `agentmessage/*.go`、`agentmessage/README.zh-CN.md` |
| AM-C02 | 迁移 MessageID、CallID、role、specializations 与 constructors | Completed | Contract Verified | `agentmessage/message.go`、package tests |
| AM-C03 | 迁移 MessageSource、ContextForm、ContextSnapshotSection 与 opaque source | Completed | Contract Verified | `agentmessage/message_source.go`；clone 保留 nil 与非 nil 空 sections 的字段存在性 |
| AM-C04 | 迁移 ContentBlock、核心 variants、opaque content 与 clone helpers | Completed | Contract Verified | `agentmessage/content.go`、`harness_contract_test.go` |
| AM-C05 | 迁移 strict codec、panic containment 与 package tests | Completed | Contract Verified | `agentmessage/*_codec.go`、strict/opaque/panic/golden tests |
| AM-C06 | 删除 `llm` 旧 declarations，零 alias、wrapper 和双轨入口 | Completed | Go Verified | 旧文件删除；`TestLLMDoesNotRedeclareAgentMessageFamily` 与全仓 `rg` audit 通过 |

## 6. LLM consumer 与 Provider adapter

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-L01 | `GenerateOptions` 消费 `[]agentmessage.Message` | Completed | Go Verified | `llm/types.go` |
| AM-L02 | Stream/BlockAssembler 使用 agentmessage block/CallID，AgentLoop 创建 AssistantMessage | Completed | Go Verified | `llm/stream.go`、`llm/assembler.go`、`agentloop/request.go`；旧 `AssembleMessage` 已删除 |
| AM-L03 | Runtime replay filter 只解释 ModelMessageSource replay state | Completed | Contract Verified | `llm/generation.go` 使用 `agentmessage.ReplaceSource`，runtime tests 通过 |
| AM-L04 | DeepSeek serializer 从 agentmessage 映射私有 wire DTO | Completed | Contract Verified | `llm/deepseek/serialize.go` 与 tests |
| AM-L05 | ToolSchema、StreamChunk、finish、usage、failure 与 Runtime 保持 llm owner | Completed | Go Verified | `llm` declarations 与 Architecture Gate |
| AM-L06 | DeepSeek request/stream 行为与迁移前一致 | Completed | Environment Verified | package contract tests 通过；加载 `.env` 后，真实 Subagent OneShot 与 Compaction region 均经 DeepSeek Adapter 完成 |

## 7. 主链与扩展调用方

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-X01 | Agent Inbox、pre-step、consumed work 与 durable mutation | Completed | Go Verified | `agent/`、`agentloop/` migrated tests |
| AM-X02 | Session event、surface、DeriveMessages、query 与 recovery | Completed | Contract Verified | `session/` 与 `session/agentmessage_contract_test.go` |
| AM-X03 | Tools result/additional context 与 AgentLoop tool continuation | Completed | Go Verified | `tools/`、`agentloop/tool_calls.go` tests |
| AM-X04 | Compaction selection/replacement/pruning | Completed | Go Verified | `compaction/...` focused tests |
| AM-X05 | Subagent relay/recall/notice/structured output | Completed | Go Verified | `subagent/...` focused tests |
| AM-X06 | System Prompt、Approval、UserQuestions、Session Title、API projection 与 test doubles | Completed | Go Verified | 全仓调用方 audit 与 full tests |

## 8. Architecture 与文档

| ID | 交付项 | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-A01 | 禁止 agentmessage 反向依赖高层 owner | Completed | Go Verified | `TestAgentMessageDoesNotImportConsumersOrRuntimeAdapters` |
| AM-A02 | 禁止 llm 重声明、alias 或转发 Message family | Completed | Go Verified | `TestLLMDoesNotRedeclareAgentMessageFamily`、`TestProductionCodeDoesNotUseRemovedLLMMessageFamily` |
| AM-A03 | AST 命名审计覆盖新 import 与 binding | Completed | Go Verified | `TestIdentifierNamesDoNotShadowDeclarations` 通过 |
| AM-A04 | 新增 agentmessage README 并更新代码近邻 README | Completed | Documented | `agentmessage`、`llm`、`agent`、`agentloop`、`session`、`tools` README |
| AM-A05 | 同步 03、04、08、13、14、15、24 与 Session 设计 | Completed | Documented | 权威文档与索引已指向唯一 owner 和专项证据 |

## 9. 验收 Gate

| ID | Gate | 状态 | 证据等级 | 证据 |
| --- | --- | --- | --- | --- |
| AM-G01 | Agent Message strict/opaque/clone/panic/golden | Completed | Contract Verified | `go test ./agentmessage` |
| AM-G02 | Agent Inbox、Session replay/recovery 与 request reconstruction | Completed | Go Verified | focused suite 与 `go test ./...` |
| AM-G03 | Tool continuation、Compaction replacement 与 Subagent messaging | Completed | Go Verified | focused suite 与 `go test -race ./...` |
| AM-G04 | DeepSeek serialization 与固定源 protocol fixture | Completed | Contract Verified | `go test ./llm/deepseek ./agentmessage` |
| AM-G05 | full test 与 race | Completed | Go Verified | `go test ./...`、`go test -race ./...` |
| AM-G06 | vet、build 与 AST Architecture Gate | Completed | Go Verified | `go vet ./...`、`go build ./...`、`go test ./tests/architecture` |
| AM-G07 | dependency、Markdown links 与 whitespace | Completed | Go Verified | `go mod tidy`、Markdown relative-link audit、`git diff --check` |
| AM-G08 | Assembly/Subagent integration 与真实 Provider acceptance | Completed | Environment Verified | `go test -count=1 ./internal/assembly ./subagent`；显式启用 `GOREN_REAL_PROVIDER_TEST=1` 后两项真实测试通过 |

## 10. 验收说明

本次是 package owner 与调用方的一次性破坏性 Go API 迁移，没有保留过渡代码。`tests/contract/fixture` 及其 12 个残留 `//go:build contract` TypeScript differential 文件已按仓库既有 suite 删除决策一并移除；新的 Agent Message 固定 JSONL fixture 是仓库内 keyless contract evidence，不恢复 Node/TypeScript runtime dependency。

本轮显式加载根 `.env` 且设置 `GOREN_REAL_PROVIDER_TEST=1`，实际通过 `TestRealProviderForegroundOneShot` 与 `TestRealProviderCompactsOneRegion`。前者验证 Agent Message 经真实模型、子 Agent lifecycle、Tool result 和 OneShot release 闭环；后者验证持久化 Message surface、真实模型摘要、replacement generation 与 durable attempt 收敛。测试和输出均未打印 credential 或请求 Authorization header。
