# Bound Subagent

本包实现独立`ModeBound`，拥有exact parent Session中的binding/config用例、child create/restore、resident Agent epoch、message admission、parent interaction delivery和failure containment。

```mermaid
flowchart LR
    Registry[BoundRegistry] --> Bound[bound.Service]
    Started[agent.SessionStarted] --> Bound
    Send[ChildControl.Send] --> Bound
    Event[session.EventAppended] -->|turn/end wakeup| Bound
    Bound --> Parent[parent Session events/projection]
    Bound --> Constructor[agent.Constructor]
    Bound --> Execution[Execution Registry]
    Bound --> Worker[per-binding worker]
    Worker --> Child[child Inbox receipt]
    Worker --> Parent
```

`Bind`原子提交binding和config revision 1并Flush，但不创建child。`StartBindings`在parent publication之后逐binding调用`Start`；单child失败写materialization event并进入diagnostics，不否决parent。`UpdateConfig`先持久化完整revision，再用exact Agent epoch replacement更新resident child；cold child保持cold。`Send`、config replacement和interaction admission使用同一parent-child operation lock。

每个materialized binding拥有一个跨child epoch保留的worker。现有`session.EventAppended`只在parent `turn/end`时唤醒worker；worker从parent log纯fold direct user与canonical `assistant/message`，对report-only turn写`skipped` cursor。投递先形成child `agent/inbox/spliced` receipt并Flush，再向parent提交`subagent/bound-cursor`。receipt已存在但cursor缺失时只修复cursor，不重复Followup。该fold不注册`session/projection.Unit`，也没有interaction projection event。

关闭时先取消并等待workers，再停止resident Executions；`Enabled=false`暂停cursor，重新启用后catch-up。交互调用链和恢复顺序见[Parent Interaction投递设计](../../docs/session-event-delivery.zh-CN.md)。

本包不拥有配置源、Plugin dependency lookup、System Prompt、Tools实现、通用Session EventBus或公开interaction projection，也不注销Extension。完整需求见[Bound需求](../../docs/parent-bound-requirements.zh-CN.md)，方法和调用链见[技术设计](../../docs/parent-bound-design.zh-CN.md)，config组合见[Bound Config管理](../../docs/bound-config-management.zh-CN.md)，实现证据见[08实施进度](../../../zh-CN/08-implementation-progress.md)。
