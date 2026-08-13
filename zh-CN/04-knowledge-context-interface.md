# 04 Knowledge 信息接口

状态：Draft

本文只定义 Memory Agent 在第二阶段获取现有 Knowledge 的只读 Tool。请求和响应信封见[接口与数据契约](./03-tools-and-contracts.md)，查询失败和安全要求见[可靠性、安全与可观测性](./05-reliability-and-operations.md)。

## 13. 边界原则

Memory Agent 只通过 `Knowledge Context Tool` 获取相关信息，不关心 Knowledge 如何管理。

Agent 不依赖也不定义：

- Knowledge 的数据库、文件或物理布局；
- 对象如何创建、更新、删除或合并；
- 记忆对象版本、Snapshot、事务和并发控制；
- 索引、缓存、Embedding、摘要或召回视图；
- 第二阶段决策在下游如何执行、拒绝或持久化。

`add`、`update`、`keep` 和 `delete` 是 Agent 返回的语义决策，不是该 Tool 上的写操作。

Knowledge Model 和 Schema 的版本用于解释知识类型，不等于记忆对象版本。Knowledge Context Tool 不向 Agent 返回对象版本或 `expected_version`。

## 14. Knowledge Context Tool

```text
request_knowledge_context(request) -> KnowledgeContext
```

这是 Memory Agent 内部提供给 Agent 的只读 Tool，由外部 Knowledge Information Provider 实现其读取能力。Agent 用第一阶段结果描述需要查找的对象和立场，Provider 将任意 Knowledge 产品的内容适配为第二阶段 LLM 可消费的 Context。

### 14.1 调用时机

Agent 必须先完成第一阶段的对象抽取、stance 判断和确定性校验，Workflow 才开放该 Tool。第一阶段不得读取 Knowledge，也不得让现有立场影响用户当前立场的判断。

第一阶段没有结果时，Workflow 不开放该 Tool，直接返回空决策。

### 14.2 请求

请求只包含：

- 当前 Scope；
- `knowledge_model_version`；
- 已校验的第一阶段结果，包括 `result_ref`、`kind`、Schema 版本、payload、stance 和可选 basis；
- 需要的 Evidence 深度；
- 最大返回条目数和 token 预算。

请求不得包含表名、Repository 方法、查询语言、索引名称、存储主键、记忆对象版本或生命周期操作。

### 14.3 响应

响应只提供第二阶段判断所需的最小信息：

- 按 `result_ref` 分组的现有 Knowledge 匹配项，空数组表示没有匹配对象；
- 每项的不透明 `knowledge_ref`；
- `kind`、Schema 版本、payload 和现有 stance；
- 可选的 basis 和 Evidence 引用；
- 不透明 `context_ref`；
- 截断状态和实际 token 用量。

Agent 只把这些内容放入第二阶段 Context，不从引用或字段推断 Knowledge 的内部管理方式或对象版本。

### 14.4 Provider 保证

Provider 保证：

- Scope 已执行隔离和授权；
- 返回项属于请求允许的知识类型；
- payload 与声明的 Schema 版本一致；
- 每项都包含第二阶段比较所需的现有 stance；
- 每个请求中的 `result_ref` 都有且只有一个响应分组；
- 被截断时显式返回 `truncated = true`；
- `context_ref` 在审计保留期内可以解析到当时返回的信息；
- 不暴露无关的内部状态或敏感字段。

Provider 如何检索、组织和管理 Knowledge 属于其自身实现。

### 14.5 Agent 使用规则

Memory Agent：

- 只在第一阶段产生结果后查询；
- 根据剩余 token 预算限制返回规模；
- 只允许第二阶段引用本次响应中的 `knowledge_ref`；
- 把现有对象和第一阶段结果交给 LLM 合并或解决冲突；
- 只输出 `add`、`update`、`keep` 或 `delete`；
- 不通过接口执行写入、确认、发布或生命周期操作。

Knowledge Context 缺失或截断时，Agent 不能假装已经知道现有状态。能够可靠决策的结果可以继续；依赖缺失信息的结果必须让 Run 失败或执行一次策略允许的受限补充查询。

### 14.6 类型兼容性

Agent 只消费本次 Knowledge Model 中启用的 `kind` 和兼容 Schema。遇到未注册类型或不兼容 Schema 时，不降级为无类型文本，也不猜测字段语义。

非必需项可以忽略并记录诊断；若缺失信息会影响对象身份、合并或冲突解决，则不能为对应对象输出决策。Agent 不要求 Provider 迁移或修改内部 Knowledge。

## 15. 输出边界

Knowledge Context 只作为第二阶段 LLM 输入。Memory Agent 最终返回第一阶段立场结果和第二阶段语义决策。

下游如何执行 `add`、`update`、`keep` 或 `delete`，不属于 Memory Agent 的接口或 Workflow。

若下游写入组件需要乐观并发控制，它只在真正构造记忆数据库写命令时取得并注入 `expected_version`。对象版本不加入 Knowledge Context，也不回填到 Agent decision。
