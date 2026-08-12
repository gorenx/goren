# 05 可靠性、安全与可观测性

状态：Draft

本文负责 Memory Agent 自身的不变量、只读依赖失败、重试、安全隔离和运行观测要求。验收与阶段计划见[验收、评估与实施路线图](./06-acceptance-and-roadmap.md)。

## 16. 不变量和失败处理

### 16.1 不变量

1. 一次 Run 的 Prompt、Schema、Parser、Validator 和结果必须使用同一个 `knowledge_model_version`。
2. Agent 核心不得根据具体 `kind` 编写抽取或立场判断分支。
3. LLM 直接输出知识对象、最终 stance 和确定性立场的 basis，不引入额外中间语义状态。
4. `stance` 只能是 `support`、`oppose` 或 `uncertain`。
5. `support` 和 `oppose` 必须携带 `basis = explicit | inferred`；`uncertain` 不得携带 basis。
6. `inferred` 必须有足够的上下文 Evidence，不得作为低置信度猜测的替代标签。
7. 每项结果都必须绑定当前输入 Evidence。
8. `knowledge_refs` 只能引用本次 Knowledge Context 返回的信息。
9. Knowledge Context Port 对 Agent 只读。
10. 输出不得包含 Knowledge 管理命令。
11. 信息不足时必须返回 `uncertain`，不得猜测为 `support` 或 `oppose`。
12. 同一知识在一次结果中只能出现一项。
13. 每次模型调用的输入加预留输出不得超过模型 Context 硬上限。
14. 当前消息、Evidence 锚点和启用类型的最小输出 Schema 不能因压缩而丢失。

### 16.2 重试与回放

精确回放至少固定：

```text
run_id
agent_version
policy_version
context_policy_version
knowledge_model_version
source_refs
knowledge_context_ref
```

重试不得切换这些输入版本。若原 `context_ref` 已无法解析，系统应明确标记为不可精确回放。

### 16.3 无效模型输出

- 模型输出无法解析：在固定 Prompt 和 Schema 下有限重试；
- `kind` 未启用或 Schema 不匹配：拒绝结果；
- payload 未通过 Validator：拒绝对应结果；
- stance 不属于三种允许值：拒绝对应结果；
- support/oppose 缺少 basis 或 basis 无效：拒绝对应结果；
- uncertain 携带 basis：拒绝对应结果；
- Evidence 或 Knowledge 引用不存在：拒绝对应结果；
- 同一知识出现多个互相冲突的 stance：要求模型重新生成完整结果；
- 重试后仍无效：Run 失败。

Parser 和 Validator 只检查确定性规则，不补造知识，也不重新判断 stance。

### 16.4 Context 预算失败

- 可选内容超限：按优先级裁剪；
- 历史内容超限：压缩正文并保留原始引用；
- Knowledge Context 超限：缩小返回预算；
- 必需指令、当前消息、Evidence、最小 Schema 和输出预留仍无法容纳：Run 失败；
- 分批处理时，返回前必须合并同一知识并消除冲突结果。

### 16.5 只读依赖失败

- Knowledge Model 加载失败：Run 失败；
- Conversation Source 暂时失败：有限重试；
- Knowledge Context Port 暂时失败：使用相同请求有限重试；
- Knowledge Context 缺失或被截断：能够确定时继续，否则返回 `uncertain`；
- Provider 返回不兼容类型或 Schema：忽略非必需项，必需项不兼容时失败；
- Model Runtime 暂时失败：使用固定输入有限重试。

Knowledge 产品内部的写入、事务、索引或发布失败不属于 Memory Agent 的失败分类。

## 17. 安全和隔离

每次 Conversation 和 Knowledge 读取必须绑定 Scope：

```text
workspace_id
subject_id
agent_id（可选）
thread_id（可选）
```

Knowledge Information Provider 负责读取权限、多租户隔离、敏感信息策略、审计和响应大小限制。Memory Agent 不能通过 Prompt 绕过接口权限。

Knowledge Model Provider 属于受信扩展边界：

- 只加载允许的版本和类型；
- Prompt、Schema、Parser 和 Validator 来自受控发布物；
- Parser 不得获得数据库凭据或 Knowledge 管理权限；
- 外部配置文本必须与 System 指令隔离。

## 18. 可观测性

每次 Run 至少记录：

- Run ID、Scope 和触发原因；
- Agent、Policy、Context Policy 和 Knowledge Model 版本；
- 启用的 `kind`、Schema、Prompt、Parser 和 Validator 引用；
- 使用的 Message、`context_ref` 和 `knowledge_ref`；
- Context 硬上限、分区预算和实际 token；
- 被裁剪、压缩和分批处理的内容数量；
- 按 `kind`、stance 和 basis 统计的结果数量；
- 只读接口和模型调用次数与耗时；
- 重试、截断、不兼容项和失败原因；
- token 和模型成本。

普通日志只记录引用、版本、哈希和必要摘要，不复制完整消息、Knowledge payload 或 Evidence 正文。
