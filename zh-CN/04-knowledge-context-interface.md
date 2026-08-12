# 04 Knowledge 信息接口

状态：Draft

本文只定义 Memory Agent 为完成 LLM 立场判断所需的 Knowledge 读取边界。请求和响应信封见[接口与数据契约](./03-tools-and-contracts.md)，查询失败和安全要求见[可靠性、安全与可观测性](./05-reliability-and-operations.md)。

## 13. 边界原则

Memory Agent 只通过 `Knowledge Context Port` 获取相关信息，不关心 Knowledge 如何管理。

Agent 不依赖也不定义：

- Knowledge 的数据库、文件或物理布局；
- 对象如何创建、更新、删除或合并；
- 身份、Revision、Snapshot、事务和并发控制；
- 索引、缓存、Embedding、摘要或召回视图；
- Agent 返回结果被下游接受、拒绝或持久化的规则。

## 14. Knowledge Context Port

```text
RequestKnowledgeContext(request) -> KnowledgeContext
```

这是 Memory Agent 拥有的只读 Port，由外部 Knowledge Information Provider 实现。Agent 描述判断所需的信息，Provider 将任意 Knowledge 产品的内容适配为 LLM 可消费的 Context。

### 14.1 请求

请求只包含：

- 当前 Scope；
- `knowledge_model_version`；
- 启用的 `kind`；
- 从当前输入得到的检索 Focus；
- 需要的 Evidence 深度；
- 最大返回条目数和 token 预算。

请求不得包含表名、Repository 方法、查询语言、索引名称、存储主键或生命周期操作。

### 14.2 响应

响应只提供判断所需的最小信息：

- 相关 Knowledge 条目；
- 每项的不透明 `knowledge_ref`；
- `kind`、Schema 版本和 payload；
- 可选的 Evidence 引用；
- 不透明 `context_ref`；
- 截断状态和实际 token 用量。

Agent 只把这些内容放入 Context，不从引用或字段推断 Knowledge 的内部管理方式。

### 14.3 Provider 保证

Provider 保证：

- Scope 已执行隔离和授权；
- 返回项属于请求允许的知识类型；
- payload 与声明的 Schema 版本一致；
- 被截断时显式返回 `truncated = true`；
- `context_ref` 在审计保留期内可以解析到当时返回的信息；
- 不暴露无关的内部状态或敏感字段。

Provider 如何满足这些保证属于其自身实现。

### 14.4 Agent 使用规则

Memory Agent：

- 只在已有 Knowledge 可能影响最终立场或对象规范化时查询；
- 根据剩余 token 预算限制返回规模；
- 只把实际使用过的 `knowledge_ref` 放入结果；
- 对缺失、截断或冲突且无法判断的信息返回 `uncertain`，不得标记为 `inferred`；
- 不通过接口执行写入、确认、发布或生命周期操作。

### 14.5 类型兼容性

Agent 只消费本次 Knowledge Model 中启用的 `kind` 和兼容 Schema。遇到未注册类型或不兼容 Schema 时，不降级为无类型文本，也不猜测字段语义。

非必需项可以忽略并记录诊断；若缺失信息会影响立场判断，则返回 `uncertain` 或让 Run 明确失败。Agent 不要求 Provider 迁移或修改内部 Knowledge。

## 15. 输出边界

Knowledge Context 只作为 LLM 输入。Memory Agent 最终只返回知识对象、stance、确定性立场的 basis、Evidence 和实际使用的 Knowledge 引用。

下游如何根据这些结果管理 Knowledge，不属于 Memory Agent 的接口或 Workflow。
