# 05 可靠性、安全与可观测性

状态：Draft

本文负责 Memory Agent 两阶段处理的不变量、只读依赖失败、重试、安全隔离和运行观测要求。验收与阶段计划见[验收、评估与实施路线图](./06-acceptance-and-roadmap.md)。

## 16. 不变量和失败处理

### 16.1 不变量

1. Memory Agent 必须同时包含 Agent、Tools 和 Workflow；不能把任一部分当作完整的 Memory Agent。
2. Workflow 是阶段顺序、状态转换和 Tool 可用性的唯一控制者。
3. Agent 只能使用 Workflow 当前阶段开放的 Tools；Tool 执行器必须再次校验阶段以及该 Tool 适用的 Scope 和输入预算。
4. 外部 Provider 的内部接口和状态不能直接暴露给 Agent，只能由 Tool 适配为受控结果。
5. 一次 Run 的两阶段 Prompt、Schema、Parser、Validator、类型规则和结果必须使用同一个 `knowledge_model_version`。
6. Agent 核心不得根据具体 `kind` 编写抽取、立场判断、身份匹配或合并分支。
7. 第一阶段只使用 Conversation Context，Workflow 不得开放 Knowledge Context Tool。
8. 第一阶段 Agent 通过 LLM 直接输出知识对象、stance 和确定性立场的 basis。
9. `stance` 只能是 `support`、`oppose` 或 `uncertain`。
10. `support` 和 `oppose` 必须携带 `basis = explicit | inferred`；`uncertain` 不得携带 basis。
11. `inferred` 必须有足够的 Conversation Evidence，不得作为低置信度猜测的替代标签。
12. 每项第一阶段结果都必须绑定当前输入 Evidence，并拥有本次 Run 内唯一的 `result_ref`。
13. 第一阶段结果通过校验后，Workflow 才能开放 Knowledge Context Tool；Tool 响应不能反向改写第一阶段 stance 或 basis。
14. 第二阶段必须同时接收第一阶段结果、Knowledge Context 和对应类型规则。
15. 第二阶段决策只能是 `add`、`update`、`keep` 或 `delete`，不得增加其他中间语义状态。
16. 每项第二阶段决策必须引用促成它的 `result_refs`。
17. `update`、`keep` 和 `delete` 的 `target_ref` 只能引用本次 Knowledge Context 返回的对象。
18. `add` 和 `update` 必须返回已经合并并通过类型 Validator 的完整对象。
19. stance 与决策相互独立，不得通过固定规则把 support 映射为 add、oppose 映射为 delete。
20. `uncertain` 不得产生 `add`、`update` 或 `delete`；有匹配对象时可以 `keep`，无匹配对象时不产生决策。
21. Knowledge Context Tool 对 Agent 只读；Agent 返回决策后不得调用任何 Tool 修改 Knowledge。
22. 同一现有对象在一次 Run 的最终结果中只能有一项决策。
23. 每次模型调用的输入加预留输出不得超过模型 Context 硬上限。
24. 当前消息、Evidence 锚点、启用类型的最小输出 Schema 和必要类型规则不能因压缩而丢失。
25. `knowledge_model_version` 和 `schema_version` 只表示配置与类型 Schema 版本，不得解释为记忆对象版本。
26. Conversation Tool、Knowledge Context Tool、第一阶段结果和第二阶段 decision 都不得包含记忆对象版本或 `expected_version`。
27. `expected_version` 只能由下游写入组件在构造实际记忆数据库写命令时注入。
28. 对象版本冲突、条件写失败及其重试不属于 Agent Run 的状态或失败分类。

### 16.2 重试与回放

精确回放至少固定：

```text
run_id
agent_version
policy_version
context_policy_version
knowledge_model_version
source_refs
Workflow 状态和 Tool 权限
Tool 调用及其响应引用
第一阶段模型输入
第一阶段结果
knowledge_context_ref
第二阶段模型输入
```

这些版本都是 Agent、Policy、Context 或类型配置版本，不包含记忆对象版本。重试不得切换这些输入版本。第一阶段失败时只重试第一阶段；第二阶段失败时复用已经校验的第一阶段结果和同一 `context_ref`，不得重新判断用户立场。若原 `context_ref` 已无法解析，系统应明确标记为不可精确回放。

### 16.3 无效的第一阶段输出

- 模型输出无法解析：在固定第一阶段 Prompt 和 Schema 下有限重试；
- `kind` 未启用或 Schema 不匹配：拒绝对应结果；
- payload 未通过 Validator：拒绝对应结果；
- stance 不属于三种允许值：拒绝对应结果；
- support/oppose 缺少 basis 或 basis 无效：拒绝对应结果；
- uncertain 携带 basis：拒绝对应结果；
- Evidence 不属于本次 Conversation Context：拒绝对应结果；
- 输出包含记忆对象版本或 `expected_version`：拒绝对应结果；
- 同一知识在第一阶段出现多个互相冲突的 stance：要求模型重新生成完整结果；
- 重试后仍无效：Run 失败，不查询 Knowledge。

Parser 和 Validator 只检查确定性规则，不补造知识，也不重新判断 stance。

### 16.4 无效的第二阶段输出

- decision 不属于四种允许值：拒绝对应决策；
- `result_refs` 不存在：拒绝对应决策；
- `target_ref` 不属于本次 Knowledge Context：拒绝对应决策；
- `add` 或 `update` 缺少完整对象，或对象未通过类型 Validator：拒绝对应决策；
- `update`、`keep` 或 `delete` 缺少 `target_ref`：拒绝对应决策；
- uncertain 结果导致 `add`、`update` 或 `delete`：拒绝对应决策；
- 同一 `target_ref` 出现多个最终决策：要求模型重新生成完整决策集；
- 决策与类型身份、合并或冲突规则不一致：拒绝对应决策；
- 决策包含记忆对象版本或 `expected_version`：拒绝对应决策；
- 重试后仍无效：Run 失败。

第二阶段 Parser 和 Validator 不执行决策；Memory Agent 不提供 Knowledge 写 Tool。

### 16.5 Context 预算失败

- 第一阶段可选历史超限：按相关性裁剪，保留当前消息和 Evidence 锚点；
- 第二阶段现有 Knowledge 超限：缩小返回预算或按策略发起受限补充查询；
- 任一阶段的历史正文可以压缩，但必须保留原始引用；
- 必需指令、当前消息、Evidence、最小 Schema、类型规则和输出预留仍无法容纳：Run 失败；
- 分批处理时，第一阶段返回前必须合并同一抽取对象，第二阶段返回前必须保证每个现有对象只有一项决策。

### 16.6 只读依赖失败

- Knowledge Model 加载失败：Run 失败；
- Conversation Source 暂时失败：有限重试；
- 第一阶段没有结果：Workflow 不开放 Knowledge Context Tool，返回空决策；
- Knowledge Context Tool 暂时失败：使用相同请求有限重试；
- Knowledge Context 缺失或被截断：只对信息完整的结果继续决策，其余结果失败或按策略补充查询；
- Provider 返回不兼容类型、Schema 或缺失 stance：忽略非必需项，影响决策时失败；
- Model Runtime 暂时失败：使用对应阶段的固定输入有限重试。

Knowledge 产品内部的写入、对象版本冲突、事务、索引或发布失败不属于 Memory Agent 的失败分类。

## 17. 安全和隔离

每次 Conversation 和 Knowledge 读取必须绑定 Scope：

```text
workspace_id
subject_id
agent_id（可选）
thread_id（可选）
```

Knowledge Information Provider 负责读取权限、多租户隔离、敏感信息策略、审计和响应大小限制。Memory Agent 不能通过 Prompt 绕过 Tool 权限。

Knowledge Model Provider 属于受信扩展边界：

- 只加载允许的版本和类型；
- 两阶段 Prompt、Schema、Parser、Validator 和类型规则来自受控发布物；
- Parser 和 Validator 不得获得数据库凭据或 Knowledge 管理权限；
- 外部配置文本必须与 System 指令隔离。

## 18. 可观测性

每次 Run 至少记录：

- Run ID、Scope 和触发原因；
- Agent、Policy、Context Policy 和 Knowledge Model 版本；
- 启用的 `kind` 及两阶段 Prompt、Schema、Parser、Validator 和类型规则引用；
- 使用的 Message、第一阶段 `result_ref`、`context_ref` 和第二阶段 `target_ref`；
- Workflow 状态转换、各阶段开放的 Tools、实际 Tool 调用和被拒绝的越阶段调用；
- 两阶段各自的 Context 硬上限、分区预算和实际 token；
- 被裁剪、压缩、分批和补充查询的内容数量；
- 按 `kind`、stance 和 basis 统计的第一阶段结果数量；
- 按 `add`、`update`、`keep` 和 `delete` 统计的第二阶段决策数量；
- 各 Tool 和两阶段模型调用次数与耗时；
- 各阶段的重试、截断、不兼容项和失败原因；
- token 和模型成本。

普通日志只记录引用、版本、哈希和必要摘要，不复制完整消息、Knowledge payload 或 Evidence 正文。
