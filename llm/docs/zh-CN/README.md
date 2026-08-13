# LLM Runtime 中文文档

状态：当前有效

本目录维护 `llm` 模块的中文设计和实现状态。Memory Agent 的领域设计仍由仓库根目录的[中文详设索引](../../../zh-CN/README.md)统一组织；本目录只负责 Model Runtime、Provider adapter 和相关运行能力，不定义 Agent、Tools、Workflow 或 Knowledge 的语义。

## 文档职责

- [01 LLM Runtime 能力需求与实现状态](./01-capability-requirements-and-status.md)：记录 LLM-01～LLM-18 的实现证据、明确决策和验收条件，并区分未纳入当前范围的参考能力。

## 维护规则

- 文件名和一级标题使用两位数字前缀表达阅读顺序，本索引不参与编号。
- 状态必须由当前源码和测试支持；只有实现与针对性测试同时存在时，才能标记为“已实现并验证”。
- pi `packages/ai` 只是兼容能力的参考实现，不是 Goren 的需求来源；不能为了形式一致而复制其 TypeScript 类型、调用形态或注册机制。
- Provider 专属参数和 wire 差异归 adapter 所有，不能无条件堆入通用 `Model`、`Context` 或 Agent 状态。
