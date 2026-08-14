# Memory Agent 历史设计

状态：Historical

本目录保存 Goren 主线切换到 DeepSeek Harness 复刻前的 Memory Agent 设计，仅用于追溯历史讨论。它不拥有当前 Agent、Session、Tools、Workflow、LLM 或插件边界，也不是 Harness 实现的需求来源。

普通 Harness 设计和实现任务不要默认加载本目录。只有任务明确处理 Memory Agent 历史方案、迁移记录或旧链接时才按需读取：

- [01 架构与边界](./01-architecture-and-boundaries.md)
- [02 工作流与决策](./02-workflow-and-decisions.md)
- [03 Tools 与契约](./03-tools-and-contracts.md)
- [04 Knowledge Context 接口](./04-knowledge-context-interface.md)
- [05 可靠性与运维](./05-reliability-and-operations.md)
- [06 验收与路线图](./06-acceptance-and-roadmap.md)

当前权威设计从[中文详设索引](../README.md)进入。
