# Approval 模块业务迁移

## 架构与职责

Approval 拥有 ask/never 策略选择、策略文本以及 asked/decided/policy 审计语义。Session 拥有日志提交与持久化；Interaction 拥有客户端回答等待与取消；Prompt 消费策略文本。Approval 业务不依赖 tools、GoAkt、Agent 或可变 Session 对象。

审批通过名为 `approval` 的通用 ToolActor 接入，不启动专属 Approval Actor。Schema 与 Provider 分别由组合入口装配；工具的创建、工作替换和关闭遵循 [Tools 生命周期方案](./08-tools-implementation.md)。命名工具、Session 适配与 Interaction 回答已经在默认服务中接通。

## 主要对象、接口与数据

| 对象 | 职责 |
| --- | --- |
| Config / ValidatedConfig | 校验默认策略，子配置按创建时的父配置值继承并覆盖 |
| Policy | 从事件快照解析最后一次策略覆盖，生成策略文本 |
| Service | 根据固定事实准备审批 Decision 或策略变更草稿 |
| Decision | 固定本次策略、Question 和审批 ID，生成前置与终态审计草稿 |
| Resolution | 返回 Outcome 与必须先提交的 decided 草稿 |
| Permission | 命名审批工具参数：toolName、callId、reason，不携带 SessionID |
| Execution / Journal | 驱动审计与回答流程，通过固定幂等计划等待提交确认 |
| tool / actor | 通用工具适配和固定 Session 身份客户端；不启动 Approval Actor |
| Answerer | 接收已审计的 Question，等待一个 Outcome，观察取消并释放等待资源 |

`New(ValidatedConfig)` 创建独立 Service；没有 Plugin、Factory、父 Service 或 Prompt 注册回调。`ValidatedConfig.WithOverrides` 只继承配置值，不动态关联父 Agent。

`Service.Prepare(Request)` 接收 SessionID、完整事件快照和工具信息，检查开放 Turn，解析策略并生成 Decision。`Decision.Asked` 生成 `approval/asked` 草稿；`Decision.Question` 返回独立回答参数；`Decision.Interactive` 决定是否请求回答；`Decision.Resolve` 接收回答、取消事实和失败，返回 Resolution。业务对象不提交草稿，也不能据草稿存在宣称已经审计。

`Service.ChangePolicy` 返回可选 PolicyChange（Draft、Notice），相同策略返回 nil。`DelegationPolicyDraft` 返回带 delegation 来源的 never 草稿。策略变更必须先由 Session 提交，再发送 Notice。`Policy.Resolve` 拒绝损坏或非法的最新覆盖，不静默回退；`Policy.Text` 拒绝非法值。

## 工具调用与审计契约

```mermaid
sequenceDiagram
    participant R as Runtime Actor
    participant E as 工具执行业务
    participant T as 通用 ToolActor
    participant A as Approval 业务与工具适配
    participant S as Session
    participant I as Interaction
    R->>E: 调度 PrepareCall
    E->>T: 按 approval 名称 Invoke(Permission)
    T->>A: 调度具体工具工作
    A->>S: 读取固定 Session 生命周期的事件
    A->>A: Service.Prepare
    A->>S: 提交 asked 草稿
    S-->>A: 提交确认
    opt 策略为 ask
        A->>I: Answer(Question)
        I-->>A: Outcome / 取消 / 失败
    end
    A->>A: Decision.Resolve
    A->>S: 提交 decided 草稿
    S-->>A: 提交确认
    A-->>T: Outcome
    T-->>E: 原工具调用的审批结果
    E->>E: 检查结论与 Guards
    E-->>R: 可派发或可结算的 Call
```

图中路径已由真实 HTTP/WebSocket 集成测试验证；只有外部模型返回使用本地可控服务。

SessionID 只能来自 Invocation，不允许 Permission 改写目标 Session；参数保留原工具 CallID，审批子调用另有独立 CallID 和原 RootCallID。结果是 JSON 编码的 Outcome 字符串；默认 Schema 应对模型隐藏。内部工具接入不改变原 Asked、Decided、PolicyChanged 的 wire 名称或字段。

asked 提交确认前不请求回答，decided 提交确认前不允许原工具继续。取消优先于晚到允许；never 不请求回答；不可用或非法回答不能放行。取消不得跳过终态审计，原工具仍等待审批工作实际退出。

缺少已生效的审批工具时，执行业务按 unavailable 拒绝原工具；已确认的工具失败进入失败结果。投递回复不确定使用 DeliveryError 保留调用责任，不自动重复审批、不伪造原工具完成事实。

## 当前实现与验收

纯业务测试覆盖默认和覆盖配置、损坏事件、delegation 来源、取消优先、拒绝、不可用、审计草稿 ID 配对和数据隔离。API Interaction 实现 Answerer，由默认审批工具调用。

`tools/execution/scheduler_approval.go` 已把 AskDecision 续接到具名工具调用；Runtime Actor 不解释审批流程。公开 `Call.ApplyApproval` 与冗余 `tools.ApprovalRequest` 已删除，调用方不能自行传入允许结论。

`TestRuntimePreparationUsesNamedToolAndWaitsForApprovalExit` 使用真实 Registry、Runtime 与通用 ToolActor，覆盖允许、拒绝、取消、隐藏工具、策略仅执行一次及审批等待期间禁止配置切换。测试工具提供可控结果，不运行 Service 审计或真实客户端回答；相关 race 测试通过。

`TestHTTPBuiltInToolInteractions` 验证默认 Provider 装配、asked 提交后才请求客户端、允许/拒绝/取消/关闭、重连请求保持同一 rpcId、重复回答拒绝、decided 与原工具结果的持久化顺序。业务重试测试覆盖提交前失败及提交回复丢失；这不是进程崩溃恢复测试。

## 审计执行业务

Service.Begin 从已确认的 Session 事件创建 Execution。Decision 保持策略与草稿规则；Execution 拥有 asked 待提交、等待回答、decided 待提交和已完成四个业务阶段，通过 Journal.Commit 与 Answerer.Answer 执行效果，不依赖 Actor 框架。调用者串行驱动同一 Execution；失败保留当前阶段和固定草稿，再次 Run 只继续未确认步骤，不重复已取得的回答。

Journal 使用 Session 的 WritePlan / WriteResult 契约。Approval 的私有 audit 实现 IdempotentWrite，以 SessionID、事件类型和 RequestID 定位原提交，并验证完整草稿；不同内容复用同一 ID 明确报错。新提交必须仍在 Prepare 时的同一 Turn，decided 必须已有对应 asked；重试已确认历史提交不受当前 Turn 已结束影响。序号、持久化与实际原子提交仍归 Session。

审计提交使用不受用户取消中断的上下文，Session 适配等待真实提交回复；客户端回答继续观察原取消上下文。回答完成后固定决策，提交失败不重新提问，取消不能跳过 asked / decided 配对。回答端错误、非法返回或 panic 由 Decision 按 unavailable 处理，取消仍优先。只有校验过的提交回复才推进业务阶段；Journal 的 error 不能伪装成审批结果。

验收覆盖：两次审计先后顺序、策略 never、取消与晚到允许、回答失败或 panic、asked/decided 提交前失败及回复丢失重试、错误回复、跨 Session / Turn 拒绝、重复 Run 不重复事件或提问。生产工具一次 Run 等待真实回复；Session 技术故障后的自动恢复仍需独立验收，不能据业务重试测试宣称已经实现。

### 实际工具边界

approval/tool 提供普通 Provider、定义和执行适配，根 approval 包不导入 tools。工具执行从 invocation 取得 SessionID，读取该 Session 的固定创建身份与快照；approval.Execution 管理 asked → 回答 → decided。Session 适配固定目标与 CreationID，不在提交时重新选择 Session。审计提交脱离用户取消，并等待 Session 的实际提交回复；正常的用户取消只中断回答。初始化、身份校验或业务提交失败明确返回错误，不能伪造允许结果。通用 ToolActor 无审批状态机。
