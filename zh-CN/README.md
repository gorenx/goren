# Memory Agent 中文详设

状态：Draft

本目录是 Goren 中文详细设计的入口。文档按责任归属拆分：每个关键概念、行为或契约只由一个文档维护，其他文档通过链接引用，避免形成多个事实来源。

## 文档职责

- [01 架构与边界](./01-architecture-and-boundaries.md)：Agent、Tools 与 Workflow 的组合、两阶段语义处理、对象版本边界和组件职责。
- [02 工作流与知识决策](./02-workflow-and-decisions.md)：三者如何协作完成第一阶段立场判断与第二阶段冲突解决，以及对象生命周期和 Agent 状态机。
- [03 接口与数据契约](./03-tools-and-contracts.md)：Conversation、Knowledge Model、Knowledge Context Tools、两阶段结果信封及禁止携带的对象版本字段。
- [04 Knowledge 信息接口](./04-knowledge-context-interface.md)：第一阶段之后如何通过只读 Tool 获取现有 Knowledge 和 stance，以及 Agent 不依赖的管理细节。
- [05 可靠性、安全与可观测性](./05-reliability-and-operations.md)：两阶段不变量、解析校验、只读依赖失败、Scope 隔离、日志与指标要求。
- [06 验收、评估与实施路线图](./06-acceptance-and-roadmap.md)：stance、basis 和 `add / update / keep / delete` 的验收、评估指标与实施计划。

### 组件文档

- [LLM Runtime 中文文档](../llm/docs/zh-CN/README.md)：Model Runtime、Provider adapter 的能力需求、实现状态和验收证据。

## 阅读顺序

首次阅读按文件名前的 `01`–`06` 顺序进行。只需理解核心模型时可以读完 `01`–`03`；实现 Knowledge Information Provider 时继续阅读 `04`–`05`；制定交付范围时使用 `06`。实现或评审 LLM Runtime 时，再进入对应的组件文档。

## 文档规则

- 根目录 [README.md](../README.md) 和 [README.zh-CN.md](../README.zh-CN.md) 只维护项目背景，不承载详细设计。
- 本目录中的 `01`–`06` 专项文档是当前设计依据。
- 详设文件名和一级标题使用两位数字前缀表达阅读顺序；本索引不参与编号。
- 新增或调整设计时，应修改拥有该职责的文档，并同步更新本索引；不要在多个文件中复制同一契约或决策。
- 未决事项保留在“验收、评估与实施路线图”的待确认决策中，在确认前不得写成既定行为。
