# Subagent Plugin

`plugin.Plugin`是Subagent领域的composition owner。它解析Agent、Session、Approval和Projection能力，构造普通Go业务对象，并发布`SeedBuilderRegistry`、`Starter`、`ChildControl`、`ExtensionRegistry`、`ChildDirectory`和`BoundRegistry`。本包不拥有Subagent用例或Plugin Runtime。

```mermaid
flowchart TD
    Plugin[plugin.Plugin] --> Builders[seedbuilder.Registry]
    Plugin --> Facade[subagents.Service]
    Plugin --> OneShot[oneshot.Service]
    Plugin --> Continuable[continuable.Service]
    Plugin --> Bound[bound.Service]
    Plugin --> Directory[childdirectory.Service]
    Plugin --> Extensions[extension.Registry]
    Facade --> OneShot
    Facade --> Continuable
    Facade --> Bound
    Plugin -->|publishes narrow views| Consumers[Tool / Control / Report / Host]
```

Apply依次解析依赖、注册Projection、启用ChildDirectory、构造OneShot/Continuable/Bound三个Mode Service，并打开统一Service准入。各Mode自己组合child-local Provisioner；Plugin只注入approval、common Extension Provisioner和`boundExtensions`窄adapter，不解释descriptor或Bound config。

Plugin同时观察`agent.SessionStarted`与`agent.Disposed`：前者异步调度parent已有Bound bindings，不能否决parent publication；后者同步完成exact Execution settlement。完整业务调用链只在[Bound技术设计](../docs/parent-bound-design.zh-CN.md)维护。

Dispose先`service.Close`停止三个Mode的Execution，再停用ChildDirectory、`extensions.Clear`、清理SeedBuilder并释放Projection handle。先关child再清Registry，避免把Extension cleanup误当成Bound运行期配置事件。跨包契约见[Subagent包说明](../README.zh-CN.md)，实现证据见[08实施进度](../../zh-CN/08-implementation-progress.md)。
