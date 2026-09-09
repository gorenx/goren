# Approval

本包拥有审批策略和审计草稿规则。Session Context 已退出业务契约；同步提交、Agent 注入和等待回答不再由 Service 执行。设计见 [Approval 模块](../zh-CN/actor-design/20-approval-module.md)。Actor 编排尚未完成。

## 对象与模块职责

| 对象 / 文件 | 职责 |
| --- | --- |
| Policy / policy.go | 从事件快照解析有效策略，生成说明文本 |
| Config、ValidatedConfig / types.go | 严格配置解码、验证与独立继承 |
| Service / service.go | 校验请求身份和开放 turn，准备 Decision |
| Decision、Resolution / decision.go | 固定策略和审计 ID，构造 asked/decided 草稿，取消优先且失败拒绝放行 |
| PolicyChange / service_policy.go | 策略变更草稿与提交后才注入的用户通知 |
| Question / Answerer | Interaction 回答请求契约，不携带可变 Agent 或 Session |

Service 不持有 Session、Agent、Answerer 或框架引用。Decision 不记录正在等待的请求，也不保证草稿已经提交。Asked / Decided 是持久化载荷，Question 是非持久化请求，两者通过 RequestID 关联。

## 目标交互流程

```mermaid
sequenceDiagram
    participant A as 审批编排 Actor（待接入）
    participant B as Approval 业务对象
    participant S as Session Grain
    participant I as Interaction Actor（待接入）
    A->>B: Prepare(固定事件快照)
    B-->>A: Decision
    A->>S: Commit(Asked 草稿)
    S-->>A: 提交确认
    A->>I: 策略允许时请求回答
    I-->>A: Outcome / error
    A->>B: Resolve(回答、取消、错误)
    B-->>A: Resolution
    A->>S: Commit(Decided 草稿)
    S-->>A: 提交确认
    Note over A: 此后才能向工具返回 Outcome
```

ChangePolicy 返回 nil 表示有效策略未变，否则返回草稿与用户通知；编排方必须先提交再注入消息。DelegationPolicyDraft 返回带 delegation 来源的 never 草稿，创建编排负责提交。业务方法本身不修改日志。

## 失败与生命周期

无开放 turn、非法策略或未准备的 Decision 明确拒绝。取消优先于晚到允许，never 策略拒绝交互，非法回答或回答失败归为 unavailable。输入 Question 与输出 Question 的指针字段复制隔离。

当前仅实现 Service / Decision 的审批业务规则；通用 ToolActor 的工具接入、Session 审计提交与回答端仍待接线。未接线的 Journal / Execute 草稿已删除，不保留专属 ApprovalActor。不能据业务测试宣称审批链路完成。

## 验证

业务测试覆盖 asked/decided 同 ID、草稿生成无写入、问题数据隔离、取消优先、never 拒绝、错误和非法回答、未准备 Decision 拒绝、开放 turn 约束、策略幂等和 delegation 来源。配置和策略文本测试保留。旧 Service 同步集成测试已删除，其编排语义仍需真实 Actor 测试替代。


Permission 定义命名审批工具的输入（toolName、callId、reason）；SessionID 由工具调用信封提供。执行业务通过现有 Invoker 按 approval 名称查询并调用通用 ToolActor，输出为 Outcome 的 JSON 字符串。审批 Service 仍不导入 tools 或 GoAkt；具体工具适配及审计接线尚未完成。详见 [审批续接设计](../zh-CN/actor-design/08-tools-implementation.md#审批续接修复)。
