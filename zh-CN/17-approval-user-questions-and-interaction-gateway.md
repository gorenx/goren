# 17 Approval、UserQuestions 与 Interaction Gateway

状态：Accepted

本文拥有 Approval policy/audit、UserQuestions provider、`ask_user_question` Consumer，以及它们通过 API Proxy 与 TypeScript Client 形成的可应答交互闭环。通用 `/api/respond` correlation table 由[07 API Proxy 模块设计与实现](./07-api-proxy-module.md)拥有；Mux 的 Session baseline、subscriber queue 和 Host edge 由[16 Session API Gateway 与实时 Frame 投影](./16-session-api-gateway-and-live-frames.md)拥有；Tool policy 流水线与 Agent Turn 分别由[12](./12-tools-registry-and-execution-pipeline.md)和[15](./15-agent-loop-and-request-driver.md)拥有；当前实施证据只见[08 实施进度](./08-implementation-progress.md)。

## 1. 固定源与职责映射

固定源基线：`47f943859bef60e4160492346772ded9b24f765a`。

| 源 owner / symbol | Go owner | 保留职责 |
| --- | --- | --- |
| `packages/interaction/user-approval` | `approval` | `ask/never` policy、`approval/asked`/`approval/decided` audit、scoped `approval/request` waterfall |
| `packages/interaction/user-questions` | `userquestions` | 单一 Provider、calling Agent attestation、intent prerequisite 和稳定错误码 |
| `packages/interaction/tool-ask-user` | `toolaskuser` | Tool 输入到 question batch 的映射、结构化 answer/result 与 Tool error projection |
| `packages/host/apiproxy/src/api-proxy.ts` 的 `pendingApprovals`、`pendingQuestions`、`respond` 与 `events.mux` | `apiproxy.InteractionGateway`、`InteractionFrameBroker` | stable `rpcId`、requested/replay/resolved、response 第二层解析、取消与 teardown |
| `api/approvals.schema.ts`、`questions.schema.ts`、`events.schema.ts` | `interaction_approval.go`、`interaction_question_response.go`、Mux frame values | client response payload 与 frame contract |
| 源 `WebApiClient.respond/events.mux` | contract oracle；仓库 `web.HarnessAPI`/`ConversationStore` 是当前浏览器 adapter | 固定 Client 验证协议；仓库 Web 消费 Question requested/respond/resolved，不复制源浏览器 runtime |

Go 不复制源 React/UI composer、浏览器 state、Typert generator、SDK 或 `!!js`。Interaction Gateway 提供现有 TypeScript Client 所需的 Host half；仓库自有 Web 只把已纳入的 Question contract 投影为交互控件。CLI/ACP 等未来 adapter 必须复用 Approval/UserQuestions core，而不是模拟浏览器 frame。

## 2. 四个 owner 的边界

### 2.1 Approval

Approval 拥有：

- deployment default 与 Session-local `ask`/`never` policy；
- 每次请求的 `RequestID` 以及严格配对的 `approval/asked`、`approval/decided`；
- 根据 calling Agent Scope 选择 answerer 的 waterfall；
- `allowed-once`、`rejected`、`cancelled`、`unavailable` 四种 host-side outcome；
- policy override 对 System Prompt context 和 Agent notice 的投影。

它不拥有 Web frame、`rpcId`、HTTP、Tool body 或 sandbox effect。`never` 在 dispatch 前直接拒绝；answerer 缺失、失败或返回未知值时 fail closed 为 `unavailable`。

### 2.2 UserQuestions

UserQuestions 拥有：

- 一次只允许一个 scope-owned Provider；
- 非空 batch、`plan-review` intent 及 approve option/detail prerequisite；
- supplied Agent 必须是 Agent Registry 中同一 live instance 且为 root Agent；
- `ASK_ABORTED`、`ASK_CANCELLED`、`ASK_MISSING_AGENT` 等稳定错误分类；
- request/answer 的 detached snapshot。

它不决定问题如何展示或通过哪种 carrier 回答。Subagent 不能直接弹出人机问题，应把未决事项带回父 Agent。

### 2.3 `toolaskuser`

`toolaskuser` 是 Consumer：把模型 Tool 参数映射到 `userquestions.Request`，把 Answer 编码为规范 Tool value/content，并保留 `UserQuestionError` 名称与 code。它不注册第二个 Provider、不持有 pending，也不读取 API Proxy。

### 2.4 Interaction Gateway

Interaction Gateway 是 transport adapter：

- 把 Approval answerer 和 UserQuestions Provider 接到 Mux + `/api/respond`；
- 为每次请求生成稳定 `rpcId`；
- 持有生成 requested/replay/resolved 所需的最小 pending metadata；
- 校验 client response 与原请求的 Session、audit ID、问题顺序和 option；
- 将 carrier 结果映射回 core outcome/error。

它不改变 policy、追加 Session audit、验证 root Agent、执行 Tool 或保存业务数据。

## 3. Approval 完整流程

```text
Tools pre-execute returns AskDecision
  -> Approval.Request(Agent, toolName, callId, reason)
  -> append approval/asked(RequestID)
  -> scoped approval/request waterfall
  -> InteractionGateway finds this unclaimed audit record
  -> Catalog.RegisterPendingResponse(stable rpcId, approval decoder)
  -> InteractionFrameBroker publishes approval/requested
  -> TypeScript Client or repository Web POST /api/respond
  -> decoder requires matching sessionId + approvalId + client outcome
  -> first valid claimant resolves allowed-once or rejected
  -> broker removes replay entry and broadcasts approval/resolved
  -> Approval appends approval/decided(same RequestID)
  -> Tools continues or denies
```

`approval.Request` 不重复携带 `RequestID`：与固定源一致，Approval 先写 audit，再 dispatch。Gateway 从 Session 末尾反向查找尚未 decided、也未被另一个 pending claim 的 `approval/asked`；有 `callId` 时两侧必须精确相等，无 `callId` 时只匹配同样无 ID 的记录。这样并行 Tool call 不会共享 audit identity；未来若 Approval 公共 contract 显式携带 ID，必须作为整体 contract 迁移，不能在 adapter 增加替代 ID。

Client 只能回答 `allowed-once` 或 `rejected`。`cancelled` 与 `unavailable` 是 Host 自己的终态，不能由浏览器伪造。

## 4. Question 完整流程

```text
ask_user_question Tool
  -> UserQuestions validates batch / intent / exact root Agent
  -> InteractionGateway.Ask as active Provider
  -> Catalog.RegisterPendingResponse(stable rpcId, question decoder)
  -> publish question/requested(sessionId, questions)
  -> TypeScript Client POST /api/respond
  -> validate payload and match exact request
  -> answer or client cancellation
  -> remove replay entry + broadcast question/resolved
  -> UserQuestions returns Answer or stable UserQuestionError
  -> toolaskuser materializes Tool result
```

成功 answer 必须满足：

- `sessionId` 与 pending Session 相同；
- answer 数量、顺序和每个 `id` 与 question batch 完全相同；
- `selected` 不重复且每项都来自该问题的 option label；
- `custom` 若存在，trim 后不得为空；
- single-select 最多一个 selected，且 custom 与 selected 互斥；
- multi-select 可同时携带多个 selected 与 custom。

Client 的 failure branch 只有 `error.code == "cancelled"` 可结算 question，并映射为 `ASK_CANCELLED`；其他 failure branch 是 `bad-response`。

## 5. 三层 pending 为什么不是重复业务逻辑

```text
Catalog pending table
  owns: rpcId -> owner decoder -> atomic response settlement

Interaction Gateway pending records
  own: session/audit/questions/frame material + core waiter terminal mapping

InteractionFrameBroker replay table
  owns: requested frame visibility across mux subscriber generations
```

Catalog 不认识 Approval 或 Question。Gateway 不能只把类型藏进 Catalog，因为重连需要原 requested frame；Broker 也不能解析 response，因为它不拥有业务 schema。三者分别回答“如何路由回答”“回答的是哪项业务交互”“新 subscriber 应重放哪些帧”，没有第二套 policy 或决策。

requested frame 的 `rpcId` 是可应答 identity，首次发布与所有 replay 必须相同。resolved 是通知，不可应答；它使用新的 frame `rpcId`，但 payload 保留 `approvalId` 或 `questionRpcId` 关联。

Mux baseline 顺序为：

```text
session/subscribed for each attached Session
  -> every pending question/requested in registration order
  -> every pending approval/requested in registration order
  -> non-empty session/queue snapshots
  -> later live frames
```

subscriber 登记、pending snapshot 和 live publish 共用 hub lock，因此 open 与 publish 之间不存在丢帧窗口。

## 6. Response、取消与并发结算

- payload/schema 或 correlation 不匹配返回 `bad-response`，entry 继续 pending，客户端可以修正；
- unknown、late、duplicate 或已经 withdraw 的 `rpcId` 返回 `not-pending`；
- valid response、request context cancellation 与 Gateway teardown 竞争时，Catalog 的第一个原子 claimant 是权威结果；
- request context 取消撤回 entry：Approval 得到 `cancelled`，Question 得到 `ASK_ABORTED`，两者都广播 cancelled resolved frame；
- client 主动取消 Question 得到 `ASK_CANCELLED`；Approval 不接受 client failure branch；
- WebSocket disconnect 只销毁 subscriber，不撤回业务 pending；新 mux 会收到同一 `rpcId` 的 requested replay；
- Gateway teardown 先标记 closed，再撤回并结算全部 pending，等待 resolved 已进入 hub 后才允许 Session Gateway 关闭 subscriber queue。

Frame delivery/mint failure 由 observer failure sink containment，不能把已经接受的业务回答改写为另一 outcome。首次 requested 发布失败则请求尚未对客户端可见，Gateway 撤回 Catalog entry 并向 core 返回技术失败。

## 7. Plugin composition 与依赖方向

```text
UserQuestions Plugin -> provides userQuestions
Approval Plugin -> provides approval and emits typed approval/request
API Proxy Plugin
  -> requires userQuestions to install the one Provider
  -> always registers the typed Approval answerer
  -> owns apiproxy/session Gateway, LiveFrameSource, InteractionGateway, Catalog and EventStreams
Connection Plugin -> consumes only API Proxy surface
```

Approval answerer 的注册不要求 Approval Service 已存在：typed event key 可提前注册，Service 缺席时没有请求，稍后加载时仍能命中。这保留 optional Approval deployment，不依赖声明顺序。UserQuestions 则必须作为 Service 存在，因为 API Proxy 要占用它的单一 Provider slot。

依赖保持单向：`approval`、`userquestions`、`toolaskuser` 不导入 `apiproxy` 或 `connection`；`apiproxy` 作为 consumer adapter 导入 core contract；Echo 只在 `internal/connection`。

## 8. 存储边界

Approval audit 与 policy override 是 Session 业务事实，随 Session persistence adapter 持久化。Interaction pending、Mux subscriber、Provider callback 和 `rpcId` replay table 是当前进程的 live operation，不进入 SQLite/JSONL：它们包含 waiter、Context、behavior interface 和 connection generation，进程重启后不能在没有恢复 use case 的情况下伪造继续等待。

未来 cold resume 若要恢复未决交互，必须先定义 durable interaction fact 与 Recovery owner，再由 Recovery 明确重建或取消；存储 adapter 只读写 owner 已决定的记录。

## 9. 后续能力进入规则

- Headless adapter 可以实现 Approval answerer/UserQuestions Provider，但无人值守不得默认 allow；
- ACP/MCP 若需要人机交互，映射在各自 adapter，不复用 Web frame 作为 core DTO；
- 新 question intent 先进入 `userquestions` closed vocabulary 和固定源 schema，再扩展 UI/adapter；
- sandbox/guard 只产生 Approval 请求或执行 effect，不接管 audit/pending；
- persisted recovery、multi-host routing 或 external interaction queue 出现真实需求前，不把 live pending 写入通用数据库；
- Approval 浏览器控件、源浏览器 Connection/runtime、SDK、Typert generator 和 `!!js` 继续排除；仓库 Web 的 Question adapter 由[21](./21-web-agent-main-flow.md)拥有。
