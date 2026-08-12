# Memory Agent 中文详设

状态：Draft

本目录是 Goren 中文详细设计的入口。文档按责任归属拆分：每个关键概念、行为或契约只由一个文档维护，其他文档通过链接引用，避免形成多个事实来源。

## 文档职责

- [01 架构与边界](./01-architecture-and-boundaries.md)：目标与非目标、可配置 Knowledge Model、三种 stance、系统边界，以及外部依赖和 Agent 内部职责。
- [02 工作流与立场判断](./02-workflow-and-decisions.md)：一次 Run 的处理步骤、LLM 直接输出、重复与修正、抽取对象生命周期和 Agent 状态机。
- [03 接口与数据契约](./03-tools-and-contracts.md)：Knowledge Model Port、只读 Knowledge Context Port，以及最终结果信封。
- [04 Knowledge 信息接口](./04-knowledge-context-interface.md)：Agent 如何通过只读接口请求相关信息，以及明确不依赖的 Knowledge 管理细节。
- [05 可靠性、安全与可观测性](./05-reliability-and-operations.md)：Agent 不变量、只读依赖失败、Scope 隔离、扩展安全、日志与指标要求。
- [06 验收、评估与实施路线图](./06-acceptance-and-roadmap.md)：三种 stance 的验收、质量指标、实施计划和待确认决策。

## 阅读顺序

首次阅读按文件名前的 `01`–`06` 顺序进行。只需理解核心模型时可以读完 `01`–`03`；实现 Knowledge Information Provider 时继续阅读 `04`–`05`；制定交付范围时使用 `06`。

## 文档规则

- 根目录 [README.md](../README.md) 和 [README.zh-CN.md](../README.zh-CN.md) 只维护项目背景，不承载详细设计。
- 本目录中的专项文档是当前设计依据；[notes](./notes/original-concept.md) 仅保存历史输入。
- 详设文件名和一级标题使用两位数字前缀表达阅读顺序；历史 notes 和本索引不参与编号。
- 新增或调整设计时，应修改拥有该职责的文档，并同步更新本索引；不要在多个文件中复制同一契约或决策。
- 未决事项保留在“验收、评估与实施路线图”的待确认决策中，在确认前不得写成既定行为。
