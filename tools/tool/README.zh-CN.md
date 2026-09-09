# 单工具业务

本包拥有一个稳定工具名称下的实现替换与独占资源生命周期，不依赖 GoAkt。接口、上下游契约和验收由[工具设计](../../zh-CN/actor-design/08-tools-implementation.md)拥有；当前迁移缺口见[集成记录](../../zh-CN/actor-design/40-integration-findings.md)，[历史进度](../../zh-CN/08-implementation-progress.md)不代表生产链路完成。

## 模块职责

tool.go 中 `Tool` 保存当前实现、待应用候选和待释放旧实现。`Revision` 是本次更新的具名字符串身份，标识实际工具实现，不能比较大小；`Implementation` 包含定义与可选独占资源，`Resource` 只提供可重试的 Close。定义校验复用 tools.SnapshotDefinition，不复制 schema 规则。无独占资源的工具不需要提供 Resource。

tool_provision.go 中 Provision 通过具名 Provider.Open 构造新实现。Provider 持有已绑定配置和借用能力，每次构造返回独立资源；它不持有 Tool 或 Runtime。Provision 负责构造后的接管及失败清理。即使 Open 返回错误，也必须交回仍需清理的资源。清理失败的对象保留在不可提交的候选中，后续 Abort/Close 继续清理。准备被取消后，失败清理使用不继承该取消的 context；Resource.Close 必须有界返回。

Schema 只返回指定候选的模型定义副本，供 ToolActor 自注册；不返回执行器或资源。Registry 发布完成后仍需单独 Commit 才能切换工具实现，发布事件本身不会让候选可执行。

调用方必须串行访问，并先排空工具调用再执行更新或关闭。业务对象不维护任务队列、Actor 身份或等待回复列表；它不创建连接，不注册目录，也不推断目录是否已经发布。

```mermaid
sequenceDiagram
    participant Runtime as Tool Runtime
    participant Actor as ToolActor 调度
    participant Business as Tool 业务
    participant Resource as 独占资源
    Runtime->>Actor: 准备更新
    Actor->>Business: Provision(context, Revision, Provider)
    Business-->>Actor: 验证成功并接管资源
    Runtime->>Actor: 目录发布确认后切换
    Actor->>Business: Commit(Revision)
    Actor->>Business: Retire(Revision)
    Business->>Resource: Close
    Resource-->>Business: 成功或错误
    Business-->>Actor: 成功或保留待释放资源
```

## 失败与关闭

Provision 负责清理 Provider 返回的失败实现资源，清理失败保留责任。Provision 成功到旧资源释放完成期间不返回可执行定义。Abort 一旦开始，候选不再允许 Commit；即使资源释放失败，也只能继续清理。Close 永久停止接纳，尝试释放全部资源，失败对象保留供重试，成功对象不重复释放。取消通过清理 context 传播；取消不等于清理完成，不丢弃资源引用。

业务测试覆盖切换、旧实现拒绝、返回值隔离、部分释放失败、撤销失败禁止提交及关闭重试。[ToolActor](actor/README.zh-CN.md) 已调度构造、自注册与资源清理；tool_execute.go 通过 Invocation.Run 执行已结算实现的工具体，Actor 已调度执行和取消。Tool Runtime policy／中间件的 Actor 接线、生产初始化和跨工具更新屏障尚未接通。


准备只有 Provision 一个生产入口；不再提供测试专用 Prepare。业务测试复用 Provider 夹具，验证失败返回前清理资源及 Close 不重复释放。
