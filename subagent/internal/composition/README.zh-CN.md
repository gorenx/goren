# Child Composition

本包决定 continuable child 的未发布 Scope 包含什么：fresh creation 才写 delegation approval policy，fresh 与 cold resume 都恢复 descriptor 中的 persona 和 Tool restriction，并在最后组合 `internal/setup` 的部署贡献。它不拥有 child identity、Activation residency、Agent publication 或 setup registration。

```mermaid
flowchart LR
    Continuation -->|immutable Composition| Composer
    Composer --> Approval[delegation policy on fresh]
    Composer --> Persona[persona on create/resume]
    Composer --> Restriction[tool restriction on create/resume]
    Composer --> Setup[registered Setup contributions]
    Composer -->|agent.Setup| AgentScope[unpublished Agent Scope]
```

任一部分 Prepare/Commit 失败时，组合对象按逆序释放已进入的部分，最终由 Agent 创建事务回滚整棵 Scope。跨包 contract 见[领域设计](../../docs/design.zh-CN.md)，实现证据见[进度](../../docs/implementation-progress.zh-CN.md)。
