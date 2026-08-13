# 01. LLM Runtime 能力需求与实现状态

- 状态：当前有效
- 最后核对：2026-08-13
- Goren 基线：`e2f2ba8`
- pi 参考快照：`20b78eafb` 的 `packages/ai`

## 1. 目的与边界

本文回答三个问题：

1. Goren `llm` 当前已经实现并验证了什么；
2. 为保证现有 OpenAI adapters 正确运行，还缺少什么；
3. 如果继续扩展到 pi 已支持的 Provider 和运行场景，需要按什么顺序补充能力。

本文只覆盖 Model Runtime。责任边界保持为：

- Model Runtime 提供模型调用、Context 硬上限、输出限制、token 计数、结构化响应、流事件和 usage；
- Context Manager 选择消息并负责裁剪、压缩、分段和输出预留；
- Agent、Tools 和 Workflow 不由 `llm` 包实现；
- Provider wire 字段、兼容差异、认证细节和 transport 生命周期由 adapter 或 composition root 所有。

pi 用于发现能力和验证边界，不自动构成 Goren 的需求。矩阵中的“基线”表示当前已经存在的能力；“顺序 1”是现有契约正确性要求；“顺序 2”是生产运行能力；“按需”只在选择相应 Provider 或产品能力后进入实现范围。

## 2. 状态定义

| 状态 | 含义 |
| --- | --- |
| 已实现并验证 | 已有生产代码和针对性自动化测试 |
| 部分实现 | 已有类型或部分路径，但关键行为缺失或未验证 |
| 未实现 | 当前没有可用实现 |

## 3. 实现状态矩阵

| ID | 需求 | 功能 | 顺序 | 状态 | 当前证据 |
| --- | --- | --- | --- | --- | --- |
| LLM-01 | Model-bound Client 与 API 路由 | 创建 Client 时固定模型并按 API 构造 adapter | 基线 | 已实现并验证 | [`client.go`](../../client.go)、[`registry.go`](../../registry.go)、[`client_test.go`](../../client_test.go) |
| LLM-02 | 统一流事件与终止协议 | 归一化 text、thinking、tool call、done、error 和 abort | 基线 | 已实现并验证 | [`stream.go`](../../stream.go)、[`client_test.go`](../../client_test.go) |
| LLM-03 | 通用消息与 Function Tool | 表达文本、图片、reasoning、函数调用和工具结果 | 基线 | 已实现并验证 | [`types.go`](../../types.go)、[`chatcompletions_test.go`](../../adapter/openai/chatcompletions_test.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-04 | OpenAI Chat Completions | 调用兼容 Chat Completions 的基础 SSE 接口 | 基线 | 已实现并验证 | [`chatcompletions.go`](../../adapter/openai/chatcompletions.go)、[`chatcompletions_test.go`](../../adapter/openai/chatcompletions_test.go) |
| LLM-05 | OpenAI Responses | 流式处理 reasoning、text、function call、usage 和失败事件 | 基线 | 已实现并验证 | [`responses.go`](../../adapter/openai/responses.go)、[`responses_stream.go`](../../adapter/openai/responses_stream.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-06 | 结构化输出与成本 | 请求 JSON Schema 输出并按 token 类别计算成本 | 基线 | 已实现并验证 | [`types.go`](../../types.go)、[`chatcompletions_test.go`](../../adapter/openai/chatcompletions_test.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-07 | 跨模型消息转换 | 切换 Model 或 Provider 时生成合法、可回放的历史输入 | 1 | 已实现并验证 | [`context_prepare.go`](../../context_prepare.go)、[`context_runtime_test.go`](../../context_runtime_test.go) |
| LLM-08 | 运行期错误保留 partial | 失败或取消时返回已生成内容、identity 和 usage | 1 | 已实现并验证 | [`stream.go`](../../stream.go)、[`context_runtime_test.go`](../../context_runtime_test.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-09 | Assistant 文本阶段与回放元数据 | 区分 `commentary`、`final_answer` 并精确回放 Responses item | 1 | 已实现并验证 | [`types.go`](../../types.go)、[`responses_map.go`](../../adapter/openai/responses_map.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-10 | Context 稳定序列化 | 支持 Session 持久化、跨进程传输和确定性回放 | 1 | 已实现并验证 | [`context_codec.go`](../../context_codec.go)、[`context_runtime_test.go`](../../context_runtime_test.go) |
| LLM-11 | Token 计数与 Context 硬上限 | 保证输入加输出预留不超过模型限制 | 1 | 已实现并验证 | [`runtime.go`](../../runtime.go)、[`client.go`](../../client.go)、[`client_test.go`](../../client_test.go) |
| LLM-12 | Tool 参数校验 | 执行前按参数 Schema 验证模型返回值 | 1 | 已实现并验证 | [`runtime.go`](../../runtime.go)、[`validation.go`](../../validation.go)、[`context_runtime_test.go`](../../context_runtime_test.go) |
| LLM-13 | 输入模态能力执行 | 只向模型发送其支持的文本或图片输入 | 1 | 已实现并验证 | [`context_prepare.go`](../../context_prepare.go)、[`chatcompletions_test.go`](../../adapter/openai/chatcompletions_test.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-14 | Stream partial snapshot | 每个增量事件可取得一致的当前 Assistant 状态 | 2 | 已实现并验证 | [`stream.go`](../../stream.go)、[`chatcompletions.go`](../../adapter/openai/chatcompletions.go)、[`responses_stream.go`](../../adapter/openai/responses_stream.go) |
| LLM-15 | OpenAI-compatible 兼容配置 | 适配 role、token 字段、reasoning、strict、usage 和 tool result 差异 | 2 | 已实现并验证 | [`config.go`](../../adapter/openai/config.go)、[`compatibility_test.go`](../../adapter/openai/compatibility_test.go)、[`deepseek_e2e_test.go`](../../adapter/openai/deepseek_e2e_test.go) |
| LLM-16 | 调用控制 | 支持 timeout、retry、cache、session、hooks、metadata 和 service tier | 2 | 已实现并验证 | [`types.go`](../../types.go)、[`chatcompletions.go`](../../adapter/openai/chatcompletions.go)、[`responses.go`](../../adapter/openai/responses.go)、[`compatibility_test.go`](../../adapter/openai/compatibility_test.go)、[`deepseek_e2e_test.go`](../../adapter/openai/deepseek_e2e_test.go) |
| LLM-17 | Reasoning capability negotiation | 支持 off、档位映射、clamp、budget 和 summary | 2 | 已实现并验证 | [`runtime.go`](../../runtime.go)、[`config.go`](../../adapter/openai/config.go)、[`context_runtime_test.go`](../../context_runtime_test.go)、[`responses_test.go`](../../adapter/openai/responses_test.go) |
| LLM-18 | Tool 选择与错误结果语义 | 表达 auto、none、required、指定函数及工具错误 | 2 | 已实现并验证 | [`types.go`](../../types.go)、[`map_model.go`](../../adapter/openai/map_model.go)、[`responses_map.go`](../../adapter/openai/responses_map.go)、[`compatibility_test.go`](../../adapter/openai/compatibility_test.go) |
| LLM-19 | 其他 Provider 协议 | 接入非 OpenAI wire protocol | 按需 | 部分实现 | 当前只有 [`openai-completions`、`openai-responses`](../../types_api.go) |
| LLM-20 | Model 目录 | 查询模型、Provider、价格、上下文和能力 | 按需 | 未实现 | 仅支持调用方手工构造 `Model` |
| LLM-21 | Credential 集成 | 解析环境变量、OAuth、ADC 或云凭据 | 按需 | 部分实现 | [`APIKeyResolver`](../../client.go) 只提供注入边界，没有默认实现 |
| LLM-22 | Image generation | 使用独立模型和 API 生成或编辑图片 | 按需 | 未实现 | — |
| LLM-23 | Diagnostics 与 overflow 识别 | 记录 transport 失败并统一判断 Context overflow | 2 | 部分实现 | 已有 [`ErrContextWindowExceeded`](../../context_prepare.go)，尚无统一 diagnostics 和 rate-limit 分类 |
| LLM-24 | Session transport 生命周期 | 管理缓存连接、fallback、续传和资源清理 | 按需 | 未实现 | — |
| LLM-25 | 可编程测试 adapter | 无外部 API 地测试 Agent 流、失败、usage 和缓存 | 2 | 未实现 | 测试内有私有 scripted adapter，没有可复用实现 |

## 4. 顺序 1：现有契约正确性

### 4.1 LLM-07 跨模型消息转换

功能：在不修改原始 Context 的前提下，把历史消息转换成目标模型可以接受的输入。

验收条件：

- 同一 API、Provider 和 Model 才能原样回放 model-bound reasoning metadata；
- 跨模型时不得把 encrypted 或 redacted reasoning 当作目标模型的合法签名；
- tool call ID 转换后，对应 tool result 必须使用同一个新 ID；
- `error` 或 `aborted` assistant message 不进入下一次模型输入；
- 不完整 tool call history 不得形成 Provider 无法接受的消息序列；
- 输入转换不得修改调用方持有的原始 Context。

pi 参考：`providers/transform-messages.ts`、`providers/openai-responses-shared.ts` 和 `providers/openai-completions.ts`。

实现：`Client.Stream` 和两个 OpenAI adapters 都先调用 `PrepareContext`。转换会深复制输入，移除失败 turn，跨模型剥离 opaque metadata，把可见 thinking 降级为普通 Assistant 文本，规范化 tool call ID，并为孤立调用补充 `IsError=true` 的结果。

### 4.2 LLM-08 运行期错误保留 partial

功能：一旦 `Stream` 已返回，transport、解析或 Provider 错误通过终止事件报告，同时保留错误前已经收到的信息。

验收条件：

- `ErrorEvent.Message` 保留已经结束和仍在构建的 content blocks；
- 已收到的 `ResponseID`、`ResponseModel` 和 usage 不丢失；
- context cancellation 返回 `StopReasonAborted`，其他运行期失败返回 `StopReasonError`；
- `Result` 与终止事件引用同一份最终 Assistant 语义；
- startup validation error 仍然直接由 `Stream` 返回，不伪装成运行期事件。

实现：adapter assembler 持续生成 snapshot；`FailWith` 和 `AbortWith` 使用当时的 Assistant，而不是重新创建空消息。Responses 的失败事件会先捕获 response identity 和 usage。

### 4.3 LLM-09 Assistant 文本阶段与回放元数据

功能：把用户可观察的输出阶段与 Provider 私有回放身份分开保存。

建议的通用语义是 Assistant content phase：

```text
unspecified
commentary
final_answer
```

它不是 Agent 状态，也不是 reasoning。一个 Response 可以依次产生 commentary、tool call、更多 commentary 和 final answer；调用是否结束仍由 `DoneEvent` 或 `ErrorEvent` 决定。

验收条件：

- phase 只属于 Assistant 文本内容，不泄漏到 User 或 Tool Result 文本；
- OpenAI Responses 能保存并重放 message item ID 和 phase；
- Provider 私有 item ID 使用 opaque metadata 保存；
- 只有来源 API、Provider 和 Model 兼容时才原样回放 opaque metadata；
- 不支持 phase 的 Provider 返回 `unspecified`，adapter 不伪造 `final_answer`。

实现：Assistant 文本使用独立的 `AssistantTextContent`；`ReplayMetadata` 把 opaque JSON 与来源 API、Provider、Model 绑定。Responses 保存并重放 message item ID、tool item ID、reasoning item 和 phase；Chat Completions 输出 `unspecified`。

### 4.4 LLM-10 Context 稳定序列化

功能：把通用消息和 content blocks 持久化后无损恢复。

验收条件：

- wire representation 具有明确的 message role、content type 和 schema version；
- 文本、图片、thinking、tool call、tool result、timestamps 和 replay metadata 可以 round-trip；
- 未知版本或未知 content type 明确失败，不能静默丢字段；
- Provider 私有 metadata 保持不透明，不被通用 codec 重新解释。

实现：`Context` 使用 schema version `1` 的自定义 JSON codec。未知字段、未知版本、未知 role 或 content type 都会失败；接口类型不依赖 Go 的隐式 concrete-type 编码。

### 4.5 LLM-11 Token 计数与 Context 硬上限

功能：让 Context Manager 在组装输入时获得可靠预算，并在 adapter 调用前执行最后一道硬限制。

验收条件：

- 可以按目标模型计算或保守估算输入 token；
- 调用前验证 `input + reserved output <= ContextWindow`；
- Model Runtime 发现超限时明确失败，不自动裁剪消息；
- token 计数结果可供 Run 记录和成本观测使用；
- tokenizer 不可用时的 fallback 误差策略必须显式配置或记录。

实现：`Client` 默认使用 `ConservativeTokenCounter`，按序列化 UTF-8 字节数给出带 strategy 标记的上界；调用方可通过 `WithTokenCounter` 注入模型 tokenizer。`CountTokens` 可单独记录结果，`Stream` 在 adapter 前执行硬限制并返回 `ErrContextWindowExceeded`，不会自动裁剪。

设计依据：[Model Runtime 职责](../../../zh-CN/01-architecture-and-boundaries.md#56-model-runtime)和[Context 超限验收](../../../zh-CN/06-acceptance-and-roadmap.md#1914-context-超限)。

### 4.6 LLM-12 Tool 参数校验

功能：阻止无效或不符合 Schema 的模型输出直接进入 Tool 执行。

验收条件：

- 根据 tool name 找到本次 Context 中的定义；
- 对完整参数执行 JSON Schema 校验；
- 错误包含 tool name 和可定位字段路径；
- 校验不执行 Tool，也不修改原始 `ToolCall`；
- 不允许 string-to-number 等隐式 coercion。

实现：Tool Schema 在启动调用前编译；完整 `ToolCall` 在成功终止前使用 JSON Schema Draft 校验，错误携带 tool name 和 instance path。校验只读取参数，不执行 Tool，也不修改参数。

### 4.7 LLM-13 输入模态能力执行

功能：让 `Model.Input` 成为真实约束，而不只是描述字段。

验收条件：

- 支持图片的模型无损接收 user image 和 tool-result image；
- 不支持图片时不得继续发送图片 wire item；
- reject、文本占位或上层重路由三种策略必须由明确决策选择，adapter 不得静默猜测；
- 同一策略在 Chat Completions 和 Responses 中保持一致。

实现决策：不支持图片时直接返回 `ErrUnsupportedModality`，不插入占位文本。支持图片时，user image 原样映射；Responses 使用 function output image，Chat Completions 按 pi 的兼容方式在 tool result 后追加 user image message。

## 5. 顺序 2：生产运行能力

### 5.1 LLM-14 Stream partial snapshot

`EventStream.Snapshot` 返回隔离副本。两个 OpenAI assembler 在发布 start、delta 和 end 事件前更新 snapshot；tool arguments 的临时字符串仍只通过 delta 观察，只有完整合法 JSON 才进入持久化 `ToolCall.Arguments`。LLM-23 的统一 diagnostics 与 LLM-25 的公开测试 adapter 不在 LLM-01～LLM-18 范围内，继续保留在矩阵中。

### 5.2 Adapter 兼容与调用控制

LLM-15、LLM-16 和 LLM-17 归 adapter 配置与调用策略所有。通用层只暴露跨 Provider 确实稳定的语义。

需要覆盖的能力包括：

- system/developer role；
- `max_tokens` 与 `max_completion_tokens`；
- streaming usage、strict tool 和 tool-result name；
- Provider-specific reasoning enable/disable、effort mapping 和 summary；
- timeout、retry、最大 retry delay；
- prompt cache retention、session affinity 和 request ID；
- request payload hook、HTTP response hook 和安全 metadata；
- service tier 及其成本修正。

实现边界：

- `Compatibility` 由 OpenAI adapter constructor 注入，覆盖 role、token 字段、stream usage、strict tool、tool-result name、reasoning wire shape、session header 和 tool error 前缀；不进入通用 `Model`；
- adapter 构造时创建并长期持有 model-bound SDK client；每次调用只创建独立 SDK stream，API key 通过 request option 注入；
- `StreamOptions` 在异步 transport 启动前深复制 headers、metadata、schema 和控制指针，调用方后续修改不会污染在途请求；
- timeout 和 cache 默认不启用；`MaxRetries=nil` 使用 SDK 默认值，显式 `0` 禁用重试，`MaxRetryDelay` 对 SDK 重试等待设置上限；
- `RequestID` 作为 `X-Client-Request-Id` 发送，并在调用前限制为最长 512 字节的 ASCII 字符串；
- request hook 获得不含 credential 的 request info；payload transform 获得只读 request JSON 副本，并通过 `Set` 显式写 Provider 字段，替换 `Body` 会明确失败；HTTP hook 只获得 status、headers 和 request ID，不读取 response body；
- service tier 随请求发送，响应 usage 保存实际 tier；只有 `Model.ServiceTierCost` 配置了 multiplier 时才修正成本。

Reasoning 由 `Model.ReasoningLevels`、`ReasoningMap` 和 `ReasoningBudget` 声明能力。运行时把请求 clamp 到已声明档位，再由 adapter 映射 wire value；`off`、Responses summary 和兼容 Provider budget field 都有明确行为，不支持的组合直接失败。

### 5.3 DeepSeek 真实环境验收

2026-08-13 使用官方 OpenAI-compatible Chat Completions 接口和 `deepseek-v4-flash` 完成真实流式 E2E。测试覆盖：

- adapter 复用 model-bound SDK client，并为调用创建独立 SDK stream；
- DeepSeek `thinking.type=disabled`，关闭 thinking 时不发送无效的 `reasoning_effort=none`；
- 收到 `StartEvent` 和 `TextDeltaEvent`，最终以 `StopReasonStop` 结束；
- 返回预期文本、Provider response ID 和非零 usage。

测试默认跳过，避免普通 `go test ./...` 消耗外部额度。配置 `DEEPSEEK_API_KEY`、`DEEPSEEK_API_BASE_URL` 后显式执行：

```bash
set -a
source ./.env
set +a
GOREN_E2E_DEEPSEEK=1 go test -run '^TestDeepSeekStreamingE2E$' -count=1 -v ./llm/adapter/openai
```

模型名可通过 `DEEPSEEK_MODEL` 覆盖。默认值和 wire 字段以 [DeepSeek Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing/) 与 [Create Chat Completion](https://api-docs.deepseek.com/api/create-chat-completion) 为准，credential 不进入测试日志或仓库。

### 5.4 Tool 选择与工具错误

LLM-18 的验收条件：

- 通用语义至少能表达 auto、none、required 和指定函数；
- adapter 只映射目标 Provider 支持的选择方式；
- `ToolResultMessage.IsError` 在支持原生错误状态的 Provider 中映射为原生字段；
- 不支持原生错误状态时使用明确且可测试的降级格式。

实现：`ToolChoice` 表达四种通用选择；指定函数必须存在于本次 Context。OpenAI 两种 API 都映射 choice。它们没有通用原生 Tool 错误字段，因此使用 adapter 可配置、默认值为 `[tool_error] ` 的文本前缀。

## 6. 按需扩展能力

### 6.1 Provider protocols

pi 当前参考实现还包括：

- Anthropic Messages；
- Google Generative AI；
- Google Vertex；
- Amazon Bedrock Converse Stream；
- Mistral Conversations；
- Azure OpenAI Responses；
- OpenAI Codex Responses。

这些 adapter 只在产品确定需要对应 Provider 后进入实现范围。多个厂商共享 OpenAI-compatible wire protocol 时，应优先扩展兼容配置，而不是复制 adapter。

### 6.2 Model 目录与 Credential

Model 目录负责模型发现、能力、价格和限制，不负责保存密钥。Credential resolver 或 composition root 负责环境变量、OAuth、ADC 和云凭据；adapter 只消费已经解析的凭据。

### 6.3 Image generation 与 Session transport

Image generation 是独立于对话 LLM 的能力，需要独立的 input/output model 和 adapter registry。WebSocket cache、continuation 和 session resource cleanup 只属于明确支持这些 transport 的 adapter，不能进入所有 Client 的默认生命周期。

## 7. 明确不复制的 pi 设计

- target Model 继续在创建 Client 时注入，不恢复成每次 `Stream` 传入；
- 不增加仅用于批量卸载注册项的 `sourceId`；
- 不在 adapter constructor 之外再包装独立 Factory；
- `commentary` 建模为 Assistant content phase，不建模为 Agent 生命周期状态；
- Provider compatibility 不堆成一个包含大量可选字段的通用 `Model`；
- pi 的通用 Tool 仍然只是 function tool，不能据此声称已经有 Web Search、Computer 或 MCP 的通用模型；
- 保留 Goren 已有的 JSON Schema structured response，不因 pi 通用接口缺少它而删除。

## 8. 已落实决策与后续范围

已落实：

1. 不支持的图片输入直接 reject；上层可以在调用前选择其他 Model，不由 adapter 猜测；
2. Tool 参数严格按 JSON Schema 校验，不做类型 coercion；
3. 支持注入精确 tokenizer，默认 fallback 是明确标记的保守上界；
4. timeout、retry、cache、session、request ID、hooks、metadata、service tier 等跨 Provider 语义进入 `StreamOptions`，wire 差异进入 adapter `Compatibility`；
5. Context wire schema 首版固定为 version `1`。

后续范围仍包括：第一个非 OpenAI Provider、Model 目录来源、Credential 默认实现归属、LLM-23 diagnostics、LLM-24 session transport 和 LLM-25 公开测试 adapter。这些事项不影响 LLM-07～LLM-18 的当前实现。

## 9. Public contract 迁移影响

- Assistant 可见文本从共享 `TextContent` 拆为 `AssistantTextContent`；User 和 Tool Result 继续使用 `TextContent`；
- `Model` 新增 reasoning 档位、映射、budget 和 service-tier cost 配置；`StreamOptions` 新增调用控制；
- `StreamOptions.MaxRetries` 是 `*int`：`nil` 表示沿用 SDK 默认值，指向 `0` 表示明确禁用；
- `Context` 的 JSON 表示改为稳定 versioned schema；持久化方应以 schema version 为兼容边界；
- `New` 和 `NewResponses` 通过可变参数接收 `AdapterOption`，原有两参数调用保持兼容。

## 10. 状态更新规则

- 每次实现需求时同时更新矩阵、生产代码和针对性测试；
- 只有真实测试覆盖主要成功路径和失败边界后，状态才能改为“已实现并验证”；
- Provider 真实环境 smoke test 与本地协议测试分别记录，不能互相替代；
- 修改通用 public contract 时，必须同步记录迁移影响；
- 每次核对更新本文顶部的 Goren commit 和日期。
