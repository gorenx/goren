# Compaction 业务规则

本包负责压缩事件、结果、错误和日志不变量。迁移设计见 [Compaction 模块](../zh-CN/actor-design/31-compaction-module.md)。

| 对象 / 文件 | 职责 |
| --- | --- |
| events.go / codec.go | 定义和校验持久化压缩事件与 checkpoint 来源 |
| InspectLog / invariant.go | 校验压缩事务顺序、关联、范围与日志锁 |
| ToolPairingBoundaries / tool_pairing.go | 从 Snapshot 推导合法的工具调用切分边界 |
| Result / ManualError / types.go | 返回压缩事实、分类错误及原始原因 |

根包没有 Plugin、Agent 引用、Session 对象或调度回调。持久化来源字段保持原格式。Basic 的策略、模型摘要及 command 交互尚在 Actor 迁移中，不能沿用旧生产验证结论。

```mermaid
flowchart LR
    S[Session Snapshot / Events] --> B[Compaction 业务规则]
    B --> C[Compaction Actor 编排]
    C -->|提交草稿| Owner[Session Actor]
```

Actor 提供一致快照并消费校验结果；Session 负责实际提交与持久化。纯业务无启动/关闭资源和并发任务，非法日志与边界返回 error；调用方负责取消、模型请求及失败后的收尾。
