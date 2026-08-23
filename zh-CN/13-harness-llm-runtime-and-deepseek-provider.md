# 13 Harness LLM Runtime 与 DeepSeek Provider 模块设计

状态：Accepted

本文拥有 Harness-compatible `llm` Service、Adapter Registry、模型路由、流归一化、增量组装、retry policy contract，以及 DeepSeek direct provider 的配置、wire 转换、SSE 与错误映射。Message、content block、StreamChunk 和 finish 的公共协议词汇由[03 协议与 API 兼容设计](./03-protocol-and-api-compatibility.md)拥有；Plugin Service/Event、Scope 与 effect 生命周期由[09 Plugin Runtime 与 Server Assembly 模块设计与实现](./09-plugin-runtime-and-server-assembly.md)拥有；默认 policy 的执行、durable retry facts 和等待生命周期见[`llmretry` 模块说明](../llm/retry/README.zh-CN.md)；当前实施状态和验证证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/core/llm/src/types.ts` | `llm/message.go`、`message_codec.go`、`message_source.go`、`content.go`、`content_codec.go`、`tool_schema.go`、`stream.go`、`stream_codec.go`、`finish_reason.go`、`types.go` | Message、source、content、GenerateOptions、StreamChunk、finish、usage 与 model metadata |
| `packages/core/llm/src/index.ts` 的 `Llm` | `llm.LlmRuntime`、`llm.Runtime` | Adapter route、configurable provider、model discovery、call resolution 与 stream dispatch |
| `packages/host/apiproxy/src/api/llm.ts` 与 `api-proxy.ts` 的 `llm.providers`/`llm.models` | `apiproxy.LLMGateway` | configurable directory 与 active route 合并、Host-scoped model catalog wire 投影 |
| `packages/core/llm/src/index.ts` 的 adapter registration | `llm/adapter_registration.go` 的 `AdapterRegistrationHandle`、`adapterRegistrationState` | 多 route 原子注册、replace、release 与 effect ownership |
| `llm/stream` | `llm.GenerateOptions` -> `llm.StreamOutput` Waterfall | 每次模型调用外层的 typed waterfall |
| 源 retry policy schema/defaults | `llm.RetryPolicy`、`ResolveRetryPolicy` | normal/always union、默认 retryable code 与 backoff snapshot |
| `packages/llm/llm-retry` | `llm/retry`（package `llmretry`） | 独立 Consumer 执行 provider policy、记录 retry facts 并把 retry action 交回 Agent Loop |
| 源 assistant stream assembly | `llm.BlockAssembler` | 按 block index 增量组装 text、reasoning、tool-call、usage、finish 与 replay state |
| `packages/llm/llm-deepseek` | `llm/deepseek` | DeepSeek typed config、chat-completions serialization、HTTP/SSE、translation 和 vendor error mapping |
| 源 anonymous user id helper | `llm/deepseek/anonymoususerid` | Harness installation identity 的读取、校验和持久化 |
| `bundle/base` 的 LLM declarations | `llm/factory.Factory`、`llm/deepseek.Factory` | `llm` Runtime 与 DeepSeek Provider Plugin 的 typed config、构造和生命周期 |

Go 不复制 TypeScript class inheritance、declaration merging、fetch implementation 或 schema code generation。公共扩展点以 capability interface 表达，异构 route 只保存 `llm.Adapter`；Message、StreamChunk、FinishReason 和 ContentBlock 使用封闭 core interface，并为未知 extension 保留显式 opaque variant，不使用 `any` 或反射执行 callback。

### 1.1 Go 包路径与兼容影响

内置 LLM Plugin 按 source-aligned capability 归位到 `llm` owner 下：DeepSeek Provider 使用公开 import path `github.com/gorenx/goren/llm/deepseek`，默认 Retry Consumer 使用 `github.com/gorenx/goren/llm/retry`。公开 DeepSeek 包允许 composition root 或其他编译期装配直接注册其 typed Factory，但 vendor wire DTO、transport 细节和错误 vocabulary 仍由该包独占，不成为 provider-neutral `llm` contract。

这次调整只改变 Go import path：旧 `github.com/gorenx/goren/llmretry` 调用方迁移到 `github.com/gorenx/goren/llm/retry`，package identifier 仍为 `llmretry`；仓库内旧 `internal/llm/deepseek` 调用方迁移到 `llm/deepseek`。不保留平行 wrapper 或第二条注册路径。TypeScript Client 协议、canonical Plugin 名、配置 JSON、Provider route、事件名、持久化字段和运行时语义均不改变。

## 2. 职责与非职责

`llm` 拥有：

- canonical `llm` Service、`llm/adapters-updated` Event 与 `llm/stream` Waterfall；
- provider route 到 Adapter instance、Provider metadata 和 RetryPolicy snapshot 的映射；
- configurable provider directory 与 model discovery 的能力边界；
- exact model capability resolution、默认 reasoning/maxTokens 合并和 prepared call 一致性；
- Message、ContentBlock、StreamChunk、FinishReason、TokenUsage、LlmFailure 和 LlmError；
- 跨 Adapter replay state 清理、adapter failure 到 terminal finish 的归一化；
- provider-neutral `BlockAssembler`。

`llm` 不拥有：

- Agent turn/step、何时重试、上下文压缩调度、Session append 或 Tool execution；
- DeepSeek/OpenAI 等供应商 JSON、header、endpoint、SSE 或 credential lookup；
- System Prompt section、Tool registry、Connection HTTP 入站或客户端状态；
- JSONL、SQLite/sqlc 或 model catalog 的长期持久化。

`llm/deepseek` 只拥有一个 direct DeepSeek route 的 outbound protocol adapter，代码近邻的工作原理和上下游流程见[`llm/deepseek/README.zh-CN.md`](../llm/deepseek/README.zh-CN.md)。它不向 Agent 暴露 wire DTO，不建立“通用 OpenAI-compatible SDK”公共层，也不把 Provider 的 transport/error vocabulary 泄漏进 `llm` contract。

## 3. 包内职责划分

| 文件 / 组件 | 单一职责 |
| --- | --- |
| `llm/message.go` | Message value、构造和 detached clone |
| `llm/message_codec.go` | Message 的严格 JSON 恢复与 durable encoding |
| `llm/message_source.go` | Message provenance/source 判别类型、校验和 opaque extension |
| `llm/content.go` | ContentBlock 判别值、detached clone 与内容能力查询 |
| `llm/content_codec.go` | ContentBlock 的严格 JSON 恢复、opaque extension 与嵌套 tool result codec |
| `llm/tool_schema.go` | ToolSchema 与 ContextSnapshotSection 边界值 |
| `llm/finish_reason.go` | FinishReason 判别类型、严格恢复和 opaque extension |
| `llm/stream.go` | StreamChunk 判别值和 `ChunkStream` capability |
| `llm/stream_codec.go` | StreamChunk durable codec 与 interface-valued field 恢复 |
| `llm/slice_stream.go` | 测试及进程内调用使用的 deterministic pull stream |
| `llm/adapter.go` | Service、Event、Adapter 与可选 capability interface |
| `llm/factory/factory.go` | `llm.Runtime` 的空 typed config、严格 decode 与 Plugin 构造 |
| `llm/runtime_service.go` | Runtime state、构造、provider list 与 topology notification |
| `llm/adapter_registration.go` | Adapter route entry、校验、原子 replace/release 与精确 ownership |
| `llm/provider_directory.go` | configurable provider directory entry 与独立生命周期 |
| `llm/model_discovery.go` | provider model discovery registration 与调用边界 |
| `llm/runtime.go` | model metadata、exact model 和 effective call config resolution |
| `llm/prepared_call.go` | immutable call snapshot 与 single-dispatch lifecycle |
| `llm/generation.go` | waterfall dispatch、Adapter 调用、replay-state filter 和 terminal normalization |
| `llm/retry_policy.go` | retry tagged union、默认值、范围校验与 detached snapshot |
| `llm/retry_policy_codec.go` | retry typed config 的 omission 与严格 tagged-union JSON codec |
| `llm/assembler.go` | provider-neutral 增量 block assembly |
| `llm/deepseek/config.go` | owner config、connection snapshot 与枚举值 |
| `llm/deepseek/config_codec.go` | omission/null 与 strict config JSON codec |
| `llm/deepseek/config_resolve.go` | 默认值、环境派生、模型目录校验与 atomic resolution |
| `llm/deepseek/serialize.go` | Harness Message 到 DeepSeek wire message 的单向映射 |
| `llm/deepseek/request_serialization.go` | 完整 chat-completions request、Tool schema 与 thinking resolution |
| `llm/deepseek/wire.go` | DeepSeek 私有 request/response wire DTO |
| `llm/deepseek/sse.go` | SSE framing、idle timeout、`[DONE]` 与 body lifecycle |
| `llm/deepseek/translate.go` | DeepSeek payload 到 Harness StreamChunk 的流状态机 |
| `llm/deepseek/response_mapping.go` | finish reason 与 token usage 的纯映射 |
| `llm/deepseek/adapter.go` | Adapter 构造、依赖和 lazy Stream 入口 |
| `llm/deepseek/plugin.go` | DeepSeek typed Factory、Service 依赖、Provider entry 与 reverse-order Dispose |
| `llm/deepseek/model_catalog.go` | Provider metadata、retry snapshot 与 model capability |
| `llm/deepseek/stream.go` | 单次 lazy stream 的初始化、取消与 terminal lifecycle |
| `llm/deepseek/request.go` | credential/identity resolution、HTTP request/header/send 与 SSE bridge |
| `llm/deepseek/http_error.go` | HTTP/provider failure、retry-after 与稳定 Harness error code |
| `llm/deepseek/anonymoususerid/store.go` | installation identity 文件边界，不承担 LLM 或配置职责 |
| `apiproxy/llm_api.go` | `llm.providers`、`llm.models` wire DTO、API interface 与 typed registration |
| `apiproxy/llm_gateway.go` | 持有 consumer-owned `LLMDirectory`，投影 provider topology 与共享 model catalog |

Registry 锁只保护 entry membership 和 route 快照。Adapter、model discovery、observer、waterfall 和网络 I/O 都不在 Registry lock 内运行。DeepSeek parser、translator 与 BlockAssembler 分离：parser 只理解 SSE，translator 只理解 provider payload，assembler 只理解 Harness StreamChunk。

上述文件划分按责任对象组织。判别 union 中多个 struct 仍可同处一个文件，因为它们共同组成一个 contract family；codec、registration lifecycle、network request 和 error mapping 等独立变化原因不得再混入该文件。公共 contract 与 Runtime 保持在根 `llm` 包；依赖根 contract 的构造边界放入 `llm/factory`，capability-owned 的内置 Provider 和 Retry Plugin 分别放入 `llm/deepseek` 与 `llm/retry`。它们都只能单向依赖 `llm`，不得建立反向 facade，也不得把具体 Provider 类型变成其他 Provider 的依赖。

## 4. 公共 Service 与扩展边界

Agent 和辅助调用只消费 `llm.LlmRuntime`。必需的 vendor 扩展点保持最小：

```go
type Adapter interface {
    Stream(context.Context, GenerateOptions) (ChunkStream, error)
}
```

Provider metadata、retry policy、model catalog 和 exact model resolution 是独立可选 capability interface；实现一个 wire adapter 不需要伪造不支持的目录能力。注册操作接收 route 集合并返回可 replace/release 的 typed handle；Provider Plugin 自己持有 handle，并在 `Dispose` 中逆序释放。同一 route 不能被两个 active registration 隐式覆盖，业务注册 API 不暴露 `plugin.Scope`。

`PrepareCall` 把 exact model resolution、Adapter registration identity、effective config 和 RetryPolicy snapshot 绑定在一起。Prepared call 只能 dispatch 一次，且 dispatch 前不得改写 resolved config，从而避免记录模型信息后 Adapter 正好被 replacement 的竞态。普通 `Stream` 则在调用时解析 live route。

`llm/stream` 是 provider selection 外层 waterfall。middleware 可以观察、包装或把调用路由到另一 provider；只有调用 `next` 才进入下游。最终 Adapter 选择和 provider-specific replay state 清理由 `llm` owner 执行，Plugin 不能绕过 Runtime 直接调用已注册 Adapter。

## 5. Message、Stream 与组装不变量

Message 以稳定 ID、role、source 和 content block 组成。Source 保留 `user`、`plugin`、`model`、`tool` 的 provenance；model source 可携带 provider-owned replay state。Opaque source/content/finish 只承载未知 extension 的合法 JSON，不改变 core variant 的严格字段校验。

Adapter 输出 pull-based `ChunkStream`。Runtime 保证：

- adapter 在调用前收到 detached `GenerateOptions`；
- 来自不同 Adapter instance 的历史 model replay state 在出站前删除；
- route/model/config 失败和 Adapter 同步失败转成一个 `finish:error` terminal stream；
- `context.Context` 已取消或 `ABORTED` 失败转成 `finish:aborted`；
- 中途 `Next` 失败被归一化为唯一 terminal chunk，之后不再泄漏上游 chunk；
- `Close` 幂等并向上游传播。

`BlockAssembler` 按首次出现的 block index 保持顺序，累加 text/reasoning/tool argument delta。第一个 `block-end` 是该 index 的权威完整值；finish 保存 usage、reason 和 replay state。`max-tokens` 完成时不把未完成 tool call 当作可执行调用。

## 6. DeepSeek 配置与模型目录

DeepSeek Factory 的 canonical name 是 `@deepseek-ai/dsh-llm-deepseek`，注册 route `deepseek-official`。配置是 owner-defined `deepseek.Config`，只允许：

- `apiKeyEnv`：credential reference，默认 `DEEPSEEK_API_KEY`；
- `baseURL`：显式值优先，其次启动环境 `DEEPSEEK_BASE_URL`，最后官方 endpoint；
- `thinking`、`reasoningEffort`、`maxTokens` 与 `defaultContextWindow`；
- advisory `models` catalog；
- `streamIdleTimeoutMs` 与 `retryPolicy`。

配置不接受明文 API key。strict decode 拒绝未知字段、显式 `null`、错误类型、非安全整数、重复模型、无效 thinking/effort 组合和超出 Go timer/JavaScript safe integer 边界的值。Factory 创建 immutable `ConnectionOptions` snapshot；每次真实请求开始时再解析 API key 和 anonymous user ID，不把 secret 固化到 Plugin config、日志、Session 或 fixture。

Model catalog 是 selector metadata，不是在线事实源。未列出的 model ID 仍可按文本模型解析，并使用 provider 默认 context/maxTokens；目录不会被误当作请求 allowlist。

### 6.1 Host-scoped LLM API

`apiproxy.LLMGateway` 是 API anti-corruption boundary 的状态对象，不是第二个 LLM Runtime。它只持有 consumer-owned `LLMDirectory` interface，通过方法表达两个查询和一个共享投影：

```text
llm.providers
  -> LLMGateway.Providers
  -> configurable declarations in declaration order
  -> mark active routes
  -> append active routes without a declaration

llm.models / session.models
  -> LLMGateway.Catalog
  -> each active provider ListModels + ResolveModelInfo
  -> sound ModelProviderGroup[] + isolated ModelCatalogFailure[]
```

`settingsNs` 与 `settingsPath` 只来自 configurable provider declaration；未声明但已注册的 route 仍可调用，因此以空 settings address 追加。一个 provider 的 list/resolve 失败只产生该 provider 的 failure 并丢弃其不完整 group，不影响其他 provider。`session.models` 在同一 catalog 上增加 Session 当前选择与 `routable`，不得复制目录遍历规则。

Go 实现不照搬源端 `buildModelCatalog(ctx)` 的独立函数与对象字面量 API。Gateway 以 named struct 持有依赖，以 `Providers`、`Models`、`Catalog` 方法保持职责和可复用状态边界；泛型函数只保留在无状态 typed route registration seam。

## 7. DeepSeek 出站流程

```text
Agent / auxiliary consumer
  -> llm.PrepareCall 或 llm.Stream
  -> llm/stream waterfall
  -> route + exact model resolution
  -> llm/deepseek.Adapter.Stream
  -> first ChunkStream.Next snapshots config and resolves credential/user id
  -> SerializeRequest
  -> POST {baseURL}/chat/completions
  -> SSE parser
  -> DeepSeek delta translator
  -> Harness StreamChunk
  -> Agent-owned BlockAssembler / retry loop
```

DeepSeek Adapter 使用 `net/http` 作为 outbound transport primitive；Echo 仍只负责 inbound Connection Host。请求固定 `stream=true`、`stream_options.include_usage=true`，并发送 `Authorization`、`Content-Type`、`Accept`、Harness User-Agent、anonymous user ID；存在 Session/Purpose 时增加对应 Harness header。

Serialization 保留以下源行为：

- system prompt 置于消息序列首部；
- text 直接拼接；assistant tool call 以 function call 发送；user 中 tool result 拆成 `tool` message；
- 空 tool 输出写成稳定占位文本；
- reasoning 只在 assistant 同时回放 tool calls 时作为 `reasoning_content` 传回；
- direct DeepSeek route 不支持 image，出站前返回 `UNSUPPORTED_CONTENT`；
- `Stop=nil` 表示省略字段，非 nil 空 slice 表示显式空数组；
- session-title purpose 强制关闭 thinking，compaction purpose 使用专用 header。

## 8. SSE、完成与用量映射

SSE parser 处理 UTF-8 BOM、CRLF、comment、多个 `data:` line 和空事件边界；只有 `[DONE]` 才是成功流结束。EOF 没有 `[DONE]`、malformed JSON、null choice、空成功响应和 idle timeout 都是可判别失败。取消或 timeout 会关闭 response body，以解除阻塞读取。

Translator 为 reasoning、text 和每个 provider tool-call index 分配稳定 Harness block index，并在 `[DONE]` 时按首次出现顺序发出 `block-end`、usage 和 finish。Provider finish 映射为：

- `stop` -> `stop`；
- `tool_calls` -> `tool-calls`；
- `length` -> `max-tokens`；
- 未知值 -> `error`，code 使用稳定大写值。

DeepSeek prompt token 是累计值；`prompt_cache_hit_tokens` 或 `prompt_tokens_details.cached_tokens` 从 `inputTokens` 扣除后单独写入 `cacheReadTokens`。Reasoning token 单独写入 `reasoningTokens`，避免重复计数。

## 9. 错误、RetryPolicy 与取消所有权

DeepSeek Adapter 把 HTTP/provider/transport 事实映射成 `LlmError`：`AUTH`、`QUOTA`、`RATE_LIMIT`、`CONTEXT_WINDOW_EXCEEDED`、`INVALID_REQUEST`、`SERVER`、`TIMEOUT`、`TRANSPORT`、`EMPTY_RESPONSE`、`INVALID_CREDENTIAL` 等。HTTP status、provider retry-after 和 request ID 是结构化字段，不需要 Agent 解析错误字符串。

`RetryPolicy` 由 Adapter registration 捕获并向 Agent request owner 暴露。默认 normal policy 是两次 retry、500ms 初始 delay、10s 上限、0.1 jitter，retryable code 为 `EMPTY_RESPONSE`、`RATE_LIMIT`、`SERVER`、`TIMEOUT`、`TRANSPORT`。`always` 表示持续到成功或取消。`llm` 定义并校验 policy，不自行启动跨请求 retry loop；独立 [`llmretry` Consumer](../llm/retry/README.zh-CN.md) 负责 delay/jitter/attempt、durable schedule/start facts 和取消，Agent Loop 只执行 `agent/request-error` 明确返回的 retry action，具体 attempt boundary 见[15 Agent Loop 与请求驱动模块设计](./15-agent-loop-and-request-driver.md)。

调用方 context、单次 `Next` context 和显式 `Close` 都可取消 owned request。取消不得撤销共享 Adapter registration、LLM Service 或其他 Agent 的请求。Adapter replacement 和 Plugin unload 只撤回 route entry；已取得的 prepared call 仍绑定原 registration identity。

## 10. Plugin 装配与生命周期

默认组合声明 `@deepseek-ai/dsh-llm` 与 `@deepseek-ai/dsh-credentials` Provider，再装配同时需要两项 Service 的 DeepSeek Plugin：

```text
llm/factory.Factory
  -> strict decode llm factory.Config
  -> llm.NewRuntime
  -> Runtime Manifest Provides ServiceOf[LlmRuntime]

credentials/factory.Factory
  -> selects local LiveStore
  -> NewManager
  -> Manager Manifest Provides ServiceOf[credentials.Provider]

llm/deepseek.Factory
  -> strict decode deepseek.Config
  -> immutable ConnectionOptions
  -> DeepSeek Plugin Apply
  -> Require[LlmRuntime] + Require[credentials.Provider]
  -> create typed Adapter
  -> RegisterConfigurableProviders
  -> RegisterAdapter(deepseek-official)
  -> retain both registration handles

DeepSeek Plugin Dispose
  -> Release Adapter registration
  -> Release configurable-provider registration
```

DeepSeek Plugin 直接拥有两项 registration handle；apply 失败由 Runtime 调用幂等 `Dispose` 回滚已取得的 handle，unload 同样逆序撤销 route 和目录并发布 committed topology change。DeepSeek 在每次新请求开始时通过 `credentials.Provider.Resolve(apiKeyEnv)` 解析 secret；环境优先级、只读状态与文件 LiveStore 由[22](./22-credentials-and-api-key-management.md)拥有。Anonymous identity 只在第一次真实请求时初始化，因此普通 server 启动、`host.describe` 和非 LLM 测试不触发 Provider 网络请求。

未来新增 Provider 必须实现 `llm.Adapter` 并在自己的 Plugin 中注册 route；不得复制 `LlmRuntime`、Message/Stream 类型、retry schema 或 Agent loop。Credentials Provider 由 DeepSeek Plugin 通过 typed Service dependency 取得，不改变 Agent—LLM 或 LLM—Adapter contract；未来 Settings Service 只替换 live options 来源。

## 11. 后续能力进入规则

- Agent Loop 消费 `PrepareCall`、RetryPolicy、ChunkStream 与 BlockAssembler，不把 retry attempt orchestration 回填进 Adapter；
- Session 派生模型历史时复用唯一 Message/Content contract，不创建 Session 专用 LLM DTO；
- Provider model discovery 只有真实配置 UI/API Consumer 出现后才接入外部 endpoint；
- 在线 DeepSeek smoke 必须使用显式环境 credential，并与离线 contract suite 分开记录；
- 新 SSE/provider field 先由固定源或真实响应证明，再扩展 opaque/wire 边界，不以宽松 map 静默吞掉 core drift。
