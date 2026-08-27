# Bound Subagent

本包拥有独立的 `ModeBound` 实现。Bound child 由 parent Session 中已经提交的 binding 识别；上游在 parent Agent 发布后构造 `BoundStartCommand`，统一 `subagents.Service` 再把命令分派给本包。

```mermaid
flowchart LR
    Parent[parent Agent 已发布] --> Binding[binding projection]
    Binding --> Command[BoundStartCommand]
    Command --> Facade[subagents.Service.Start]
    Facade --> Bound[bound.Service.Start]
    Bound -. TODO .-> Creation[Bound immutable creation record]
    Bound -. TODO .-> Config[Parent Session config projection]
    Bound -. TODO .-> Child[Bound child Agent]
```

当前只实现 Mode、命令分派和明确的未实现失败。`Start` 尚未读取 immutable creation record和parent binding/config projections、创建或恢复 child，也不会发布 `Execution`。因此 `Interrupt` 和 `Close` 当前没有可管理的 Bound 运行期资源。调用 Bound `Start` 的 parent publication observer 必须包含该错误，不能使 parent publication 失败。

Binding event、初始 prompt和恢复由[Bound Subagent 技术设计](../../docs/parent-bound-design.zh-CN.md)负责；config event/projection、System Prompt/Tools owner边界和运行期安全应用见[Bound Config 管理详细设计](../../docs/bound-config-management.zh-CN.md)；实现状态见[08 实施进度](../../../zh-CN/08-implementation-progress.md)。
