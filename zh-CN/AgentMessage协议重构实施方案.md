# Agent Message 协议重构实施方案

状态：Planned

制定日期：2026-08-27

设计依据：[Agent Message 协议重构设计方案](./AgentMessage协议重构设计方案.md)

进度记录：[Agent Message 协议重构进度矩阵](./AgentMessage协议重构进度矩阵.md)

## 1. 文档职责

本文只规定 Agent Message owner 迁移的实施顺序、修改范围、原子切换边界、Gate、验证顺序和提交边界，不记录完成比例。

实际状态、代码证据、测试证据和剩余缺口只更新专项进度矩阵；协议语义和最终职责只由设计方案拥有。

## 2. 实施原则

### 2.1 一次破坏性 owner 切换

Message contract 必须从 `llm` 一次迁移到 `agentmessage`：

- 不保留 `llm.Message = agentmessage.Message` alias；
- 不保留转发 constructor、codec facade 或 deprecated wrapper；
- 不让 Session 同时接受两种 Message；
- 不执行双写、双 codec 或运行期 shape conversion；
- 不增加 feature flag 选择旧/新 owner。

因为 Go type identity 和 import path 会同时改变，核心 package 移动、`llm` 消费边界和全部生产调用方迁移属于一个不可拆开的编译原子切片。实施过程中可以分批编辑和运行局部静态检查，但不能提交或宣称一个无法通过全仓编译的中间状态。

### 2.2 保留 canonical 名称和协议

类型名、事件名、JSON 字段、discriminant、配置键和持久化 shape 不改。只改变 Go package owner。受 package 名影响而必须修改的局部变量、参数或 import identifier，应按最小作用域修正，并纳入 AST 命名审计；不顺便做其他命名清理。

### 2.3 先冻结 observable contract

迁移前先用现有测试和新增 fixture 固定 Message/source/content JSON、Session event payload 和 DeepSeek serialization。代码移动不能依赖“看起来相同”，必须由重放同一 fixture 得到相同 bytes/typed semantics 证明。

### 2.4 文档与代码分开提交

实施代码、测试和必要依赖形成一个代码提交；最终状态、README、权威设计和矩阵更新形成独立文档提交。只有用户明确要求时才创建 commit。

## 3. 阶段 M0：契约冻结与迁移清单

### 目标

在移动 declaration 前固定所有 public/durable 形状，并形成完整调用方清单。

### 修改

1. 枚举 `llm` 中属于 Agent Message family 的 declaration：Message ID/role/value/source/content/codec/constructors/clone/context section/call correlation。
2. 枚举所有直接调用方，至少覆盖 `agent`、`agentloop`、`session`、`tools`、`approval`、`compaction`、`subagent`、`systemprompt`、`toolaskuser`、`userquestions`、`apiproxy` 和 `internal/assembly`。
3. 冻结以下 golden：
   - Message/source/content strict JSON round-trip；
   - opaque source/content lossless bytes；
   - Session `user/message`、`assistant/message`、`tool/result` payload；
   - DeepSeek user/assistant/tool request serialization；
   - request reconstruction 的 Message ordering 和 stable ID。
4. 记录哪些文件仍因真正的 Runtime/config/stream 语义需要 `llm`，避免机械删除合法依赖。

### Gate M0

- declaration 和调用方清单无遗漏；
- baseline fixture 在迁移前通过；
- 没有生产代码变更或 placeholder package。

## 4. 阶段 M1：建立 `agentmessage` contract

### 目标

把完整 Agent Message family 移入唯一新 owner。

### 目标文件

```text
agentmessage/
  doc.go
  message.go
  message_codec.go
  message_source.go
  content.go
  content_codec.go
  message_test.go
  harness_contract_test.go
```

文件可以按职责调整，但不得将 codec、Message value、source/content union 和 LLM runtime state 重新混入一个大文件。

### 实施内容

1. 移动 `MessageID`、`CallID`、Message role/value/specializations 和 constructors。
2. 移动 Message source、ContextForm、ContextSnapshotSection 和 opaque source。
3. 移动 ContentBlock、核心 block variants、opaque content 和 clone/query helpers。
4. 移动 strict codecs 和 panic containment tests。
5. 保留可能进入 API、Session failure 或测试 fixture 的既有错误 code/text；只有经调用链证明为 package-local、不可观察的诊断才可以随 owner 修正前缀，不能把命名清理混入协议迁移。
6. 添加 package doc，明确 Agent interaction fact 和非 EventBus 边界。

### 原子性要求

M1 declaration 移动与 M2–M4 调用方迁移必须处于同一编译原子切片；不能通过临时 alias 让 M1 单独落地。

## 5. 阶段 M2：迁移 LLM consumer 与 Provider adapter

### 目标

让 `llm` 从 Message owner 变成 `agentmessage` consumer，同时保持 generation stream owner 不变。

### 修改

1. `GenerateOptions.Messages` 改为 `[]agentmessage.Message`，clone 通过新 owner 执行。
2. `BlockEndChunk.Block` 改为 `agentmessage.ContentBlock`。
3. `ToolCallDeltaChunk.ID` 和所有 stream/assembler correlation 改为 `agentmessage.CallID`。
4. `BlockAssembler` 使用 `agentmessage` blocks，只输出 assembled blocks、finish、usage 和 replay state；删除未被生产调用的 `AssembleMessage` convenience method，由 Agent Loop 创建 `agentmessage.AssistantMessage`。
5. Runtime replay filter 读取 `agentmessage.ModelMessageSource`，但 replay 解释和 Adapter identity 判定留在 `llm`。
6. DeepSeek serializer 从 `agentmessage.Message` 映射到私有 `wireMessage`；不移动 wire DTO。
7. `llm/tool_schema.go` 只保留 `ToolSchema`，将 ContextSnapshotSection 移出。
8. 删除 `llm` 中旧 Message/source/content declarations 和 tests，不留转发入口。

### Gate M2

- `llm` 只在 request/stream/runtime 位置依赖 `agentmessage`；
- DeepSeek request golden 与迁移前一致；
- Stream/assembler/replay tests 通过。

## 6. 阶段 M3：迁移 Agent、Session 与 Tools 主链

### 目标

先迁移真实交互 owner 和主请求链，固定“谁主动调用谁”。

### Agent

- Inbox queue、mutation、stable-ID uniqueness 和 notifications 使用 `agentmessage.UserMessage`/`MessageID`；
- `agent/pre-step` input/output 使用 `[]agentmessage.UserMessage`；
- Agent options、consumed work 和 durable Inbox codec 不再为 Message 导入 `llm`；
- `agent/request`、retry policy 等真实 LLM config/failure 依赖继续使用 `llm`。

### Session

- `user/message`、`assistant/message`、`tool/result` payload 使用 `agentmessage`；
- `DeriveMessages()` 返回 `[]agentmessage.Message`；
- message projection、surface、query、persistence recovery 和 event codecs 迁移；
- `request/header`、AssistantChunk、usage、turn error 等真实 LLM facts 继续使用 `llm`；
- persisted JSON 和 seed/recovery semantics 不变。

### Tools 与 AgentLoop

- Tool renderer/result/additional context 使用 `agentmessage.ContentBlock` 和 UserMessage；
- AgentLoop 从 Inbox 到 Session 使用 `agentmessage`，仅在 build/stream/assembler/retry 时调用 `llm`；
- Tool call/result 使用同一 `agentmessage.CallID`；
- request reconstruction 继续由 Session facts 证明，不增加 mapper cache。

### Gate M3

- Inbox、Session、Tool continuation 和 AgentLoop focused tests 通过；
- Session event golden bytes 不变；
- 主链不存在 Agent/Session 自定义 Message DTO。

## 7. 阶段 M4：迁移其他生产者和投影消费者

### 目标

完成全仓调用方收口，避免只迁移 happy path。

### 修改范围

- Compaction 的 region selection、replacement、checkpoint 和 tool-result pruning；
- Subagent 的 relay、recall、notice、structured output 和 parent/child messaging；
- System Prompt dynamic context；
- Approval、UserQuestions、`ask_user_question` 的 model-facing content；
- Session Title、Query、API projection 和 default composition tests；
- test doubles、recordings、contract fixtures 和 benchmark。

每个 package 都要区分：

- 只因 Message 导入 `llm`：迁移后删除 `llm` import；
- 同时使用 LLM config/stream/usage：保留 `llm`，新增 `agentmessage`；
- 仅消费 API wire projection：不得直接依赖 `agentmessage`，继续通过 owner-defined mapper。

### Gate M4

- `rg` 和 AST audit 证明生产代码没有旧 `llm.Message` family 引用；
- 所有 package 编译；
- 没有因 import-name shadowing 引入 `message Message` 类命名冲突；
- 不存在 duplicate protocol type 或 conversion wrapper。

## 8. 阶段 M5：Architecture Gate 与代码近邻文档

### 目标

把 owner 和禁止方向变成可持续执行的 repository policy。

### 修改

1. 在 `tests/architecture/` 增加 Agent Message boundary audit：
   - `agentmessage` 不导入 `agent`、`session`、`tools`、`llm`、`plugin`、API、transport 或 storage adapter；
   - `llm` 不再声明 Message family；
   - 生产代码不引用旧 `llm.Message` family；
   - 不存在 alias、wrapper 或第二套 content/source union。
2. 对移动后的 declarations 运行 capitalization-insensitive binding audit。
3. 新增 `agentmessage/README.zh-CN.md`，说明职责、非职责、生产/持久化/消费链、failure 和 clone/codec 行为，并链接设计与进度矩阵。
4. 更新 `llm/README.zh-CN.md`、`agent/README.zh-CN.md`、`session/README.zh-CN.md`、`tools`/`agentloop` 等真正受影响的代码近邻说明。

### Gate M5

- architecture tests 对人工引入的反向 import、alias 和旧 declaration 能失败；
- README 不复制字段表或进度证据；
- package links 全部可解析。

## 9. 阶段 M6：兼容性与全量验收

### 验证顺序

```text
gofmt
  -> agentmessage focused tests
  -> llm/deepseek focused tests
  -> Agent/Session/Tools/AgentLoop focused tests
  -> Compaction/Subagent focused race tests
  -> architecture tests
  -> go test ./...
  -> go test -race ./...
  -> go vet ./...
  -> go build ./...
  -> TypeScript-to-Go differential fixtures
  -> Markdown link validation
  -> git diff --check
```

真实 DeepSeek smoke 不是纯 package owner 迁移的默认必需条件；只有 offline differential 或 default composition 暴露 provider 行为差异时才重新执行，并与 keyless checks 分开记录。

### Gate M6

1. 新旧 golden bytes 和 observable ordering 一致。
2. Agent 主流程、Tool continuation、compaction replacement、Subagent relay 和 cold Session recovery 通过。
3. full test/race/vet/build 通过。
4. 进度矩阵逐项附真实证据，不以文档完成代替代码完成。

## 10. 最终文档同步

代码 Gate 全部通过后，单独更新：

- [03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)的 current implementation 说明；
- [13 Harness LLM Runtime 与 DeepSeek Provider](./13-harness-llm-runtime-and-deepseek-provider.md)的最终文件映射；
- [14](./14-agent-registry-inbox-and-events.md)、[15](./15-agent-loop-and-request-driver.md)和 Session 设计中的 Go 类型路径；
- [08 实施进度](./08-implementation-progress.md)的全仓汇总；
- 本专项[进度矩阵](./AgentMessage协议重构进度矩阵.md)的每个交付项和验证记录。

文档提交不得混入 Go source，也不得把未执行的 Environment smoke 标记为通过。

## 11. 完成定义

以下条件缺一不可：

- `agentmessage` 是唯一 Agent Message contract owner；
- 全部生产调用方迁移，无旧入口和双轨类型；
- LLM 是下游 consumer，Provider wire 保持私有；
- persisted/API/provider observable semantics 未改变；
- architecture、focused、full 和 differential Gate 通过；
- package README、权威设计、全仓进度与专项矩阵同步；
- 代码和文档形成分离的 coherent changeset。
