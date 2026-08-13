# Goren

Goren 探索一个面向 Agent Memory 静态记忆产品的可复用 Memory Agent。此类产品通常重点解决长期事实、偏好、指令或摘要如何存储、组织和召回，但往往缺少一个独立的语义组件，用来持续理解对话中的知识变化。

Memory Agent 由 Agent、Tools 和两阶段 Workflow 结合而成。第一阶段仅根据对话上下文抽取可配置的知识对象，判断用户支持、反对或不确定，并区分用户直接表态与 Agent 根据上下文推断；第二阶段再通过只读 Tool 获取相关已有 Knowledge，让 LLM 合并对象或解决冲突，输出 `add`、`update`、`keep` 或 `delete`。知识对象类型由接口、Schema、Prompt 和响应解析器定义；Agent 核心不固定具体类型，也不关心 Knowledge 如何存储或执行这些决策。

中文详细设计从[中文详设索引](./zh-CN/README.md)进入。英文背景见 [README.md](./README.md)。
