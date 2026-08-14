# LLM Runtime 中文文档

状态：迁移前实现记录（非 Harness 目标设计）

本目录记录主线切换到 DeepSeek Harness 复刻前，`llm` 模块的实现状态和历史决策，只作为迁移审计证据。当前 Harness LLM 目标 contract 由[协议与 API 兼容设计](../../../zh-CN/03-protocol-and-api-compatibility.md#4-llm-协议)拥有；冲突时以当前 Harness 详设和代码为准。

## 文档职责

- [01 LLM Runtime 能力需求与实现状态](./01-capability-requirements-and-status.md)：保存切换前 LLM-01～LLM-18 的实现证据、历史决策和验收条件，不构成 Harness API 需求。

## 维护规则

- 文件名和一级标题使用两位数字前缀表达阅读顺序，本索引不参与编号。
- 状态必须由当前源码和测试支持；只有实现与针对性测试同时存在时，才能标记为“已实现并验证”。
- 不要在普通 Harness 设计或实现任务中默认加载本目录；只在迁移现有 transport、测试和 adapter 时按需查阅。
- pi `packages/ai` 是旧设计的参考实现，不是当前 Harness 复刻的需求来源。
- Provider 专属参数和 wire 差异归 adapter 所有，不能无条件堆入通用 `Model`、`Context` 或 Agent 状态。
