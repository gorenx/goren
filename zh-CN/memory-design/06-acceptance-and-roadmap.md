# 06 验收、评估与实施路线图

状态：Draft

本文负责 Memory Agent 两阶段输出的验收场景、质量指标、实施阶段和待确认决策。它验收 Agent 是否正确返回语义决策，不验收 Knowledge 如何执行、存储或发布这些决策。

以下场景默认启用部署方配置的 `profile_fact` 类型。该类型允许保存 support 和 oppose 立场；除非场景明确要求移除对象，否则立场变化使用 `update`。

## 19. 验收场景

### 19.1 Agent、Tools 与 Workflow 组合

预期：

- Memory Agent 内同时存在 Agent、Tools 和 Workflow 三类职责；
- Workflow 固定 Run 版本、阶段顺序、状态转换和 Tool 权限；
- Agent 在阶段内使用 LLM 做语义判断，并只能请求当前阶段开放的 Tools；
- Tools 对各自适用的输入、Scope 和预算做确定性校验，并适配外部 Provider；
- 第一阶段请求 Knowledge Context Tool 会被拒绝并记录；
- 第二阶段完成后不再允许 Tool 调用。

### 19.2 用户直接支持，现有 Knowledge 无匹配项

输入：

```text
“我主要使用 Go 开发后端服务。”
```

预期：

- 第一阶段输出一个 `profile_fact`；
- payload 表达用户主要使用 Go 开发后端服务；
- `stance = support`；
- `basis = explicit`；
- Evidence 指向当前消息；
- 第一阶段完成后才查询 Knowledge；
- 第二阶段输出 `add`，并携带完整对象。

### 19.3 Agent 推断用户支持，现有 Knowledge 无匹配项

前文询问：

```text
“以后都用简短回答，可以吗？”
```

用户回答：

```text
“就这样。”
```

预期：

- 第一阶段结合前文得到“用户偏好简短回答”；
- `stance = support`；
- `basis = inferred`；
- Evidence 包含当前用户消息，并保留用于指代解析的上下文引用；
- 第二阶段在无匹配项时输出 `add`。

### 19.4 已有相同 Knowledge

第一阶段得到“用户主要使用 Go”，`stance = support`、`basis = explicit`；Knowledge Context Tool 返回语义和立场均相同的现有对象。

预期：

- 第一阶段结果不受现有 Knowledge 影响；
- 第二阶段输出 `keep`；
- `target_ref` 引用已有对象；
- 不增加 duplicate 或其他中间状态。

### 19.5 用户直接修正

现有 Knowledge 是“用户主要使用 Go”，当前输入为：

```text
“我已经不用 Go 了，现在主要使用 Rust。”
```

预期第一阶段返回两项：

- “主要使用 Go”：`stance = oppose`、`basis = explicit`；
- “主要使用 Rust”：`stance = support`、`basis = explicit`。

第二阶段预期：

- 对现有 Go 对象输出 `update`，完整对象的 stance 为 oppose；
- 对没有匹配项的 Rust 对象输出 `add`；
- 不输出四种 decision 之外的中间语义状态。

### 19.6 用户明确要求删除已有 Knowledge

现有 Knowledge 是“用户偏好简短回答”，当前输入为：

```text
“删除这条回答长度偏好，不要再保留。”
```

预期：

- 第一阶段识别目标对象，输出 `stance = oppose`、`basis = explicit`；
- 第二阶段根据类型规则和明确删除意图输出 `delete`；
- `target_ref` 指向现有对象；
- Agent 返回决策后不执行删除。

### 19.7 Agent 推断用户反对

前文询问：

```text
“还要保留之前的回答长度偏好吗？”
```

用户回答：

```text
“不用了。”
```

预期：

- 第一阶段得到用户反对继续保留回答长度偏好；
- `stance = oppose`；
- `basis = inferred`；
- 如果能够可靠解析目标对象，第二阶段按 `profile_fact` 类型规则输出 `update`；
- 如果无法可靠解析“不用了”所指对象，第一阶段必须改为 `uncertain`。

### 19.8 不确定且已有匹配项

当前输入是含糊表述、假设或转述，无法确定用户自己的最终立场；现有 Knowledge 存在匹配对象。

预期：

- 第一阶段能抽取对象时返回 `stance = uncertain`；
- 不设置 `basis`；
- 第二阶段只允许对现有对象输出 `keep`；
- 不输出 `add`、`update` 或 `delete`。

### 19.9 不确定且无匹配项

第一阶段返回 `stance = uncertain`，Knowledge Context 无匹配对象。

预期第二阶段返回空 `decisions`，不新增不确定知识。

### 19.10 纯询问

输入：

```text
“我之前主要使用什么语言？”
```

预期第一阶段返回空 `stance_results`，Workflow 不开放 Knowledge Context Tool，最终返回空 `decisions`。

### 19.11 当前输入重复

用户在多条消息中重复表达同一偏好。

预期第一阶段只返回一个规范化对象并合并当前消息 Evidence；第二阶段只为该对象返回一个决策。

### 19.12 自定义知识类型

部署方增加 `long_term_instruction` 类型及其两阶段 Prompt、Schema、Parser、Validator、身份和合并规则，Agent 核心不修改。

输入：

```text
“以后审查代码时，先列出会阻断合并的问题。”
```

预期第一阶段返回 `kind = long_term_instruction`、对应 payload、`stance = support` 和 `basis = explicit`；第二阶段根据现有 Knowledge 返回四种允许决策之一。

### 19.13 Knowledge Context 不完整

第一阶段已完成，Knowledge Context Tool 返回 `truncated = true`，缺失信息会影响身份匹配、合并或冲突解决。

预期：

- 不修改第一阶段 stance 或 basis；
- 对受影响对象不猜测 `add`、`update`、`keep` 或 `delete`；
- 按策略发起一次受限补充查询，仍不足时 Run 明确失败；
- 运行记录保存截断状态和 `context_ref`。

### 19.14 Context 超限

当前消息、类型 Prompt/Schema、历史和 Knowledge Context 超过模型硬上限。

预期：

- 第一阶段保留当前消息、Evidence、指令、Schema 和输出预留；
- 第二阶段保留第一阶段结果、相关 Knowledge、类型规则和输出预留；
- 优先裁剪各阶段的低相关可选内容，再压缩较早历史；
- 必要时分批处理，并分别完成第一阶段对象合并和第二阶段决策合并；
- 任一模型调用均不超过硬上限；
- 无法容纳最小必需内容时明确失败。

### 19.15 记忆对象版本边界

已有 Knowledge 存在匹配对象，第二阶段输出 `update` 或 `delete`。

预期：

- Knowledge Context Tool 不返回记忆对象版本；
- Agent 的第一阶段结果和第二阶段 decision 不包含当前版本或 `expected_version`；
- `schema_version` 只表示知识类型 Schema；
- 下游写入组件消费 decision，并在构造实际数据库写命令时取得和注入 `expected_version`；
- 数据库报告版本冲突时，由下游写入组件处理，不改变 Agent 的对象抽取生命周期或 Agent 状态机。

## 20. 评估指标

第一阶段语义质量：

- 按 `kind` 统计的知识抽取 precision 和 recall；
- `support`、`oppose`、`uncertain` 分类准确率；
- `explicit`、`inferred` 判断准确率；
- 四种确定性组合的混淆矩阵；
- 当前输入内重复合并准确率；
- Evidence coverage；
- 临时内容误识别率和不确定性校准。

第二阶段决策质量：

- `add`、`update`、`keep`、`delete` 分类准确率；
- 对象身份匹配准确率；
- 合并后完整对象的 Schema 和语义准确率；
- 冲突解决准确率；
- `target_ref` 和 `result_refs` 引用准确率；
- uncertain 误产生变更决策的次数，目标为零。

Tool 与 Workflow 质量：

- 第一阶段开放 Knowledge Context Tool 的次数，目标为零；
- Agent 越阶段调用 Tool 且未被拒绝的次数，目标为零；
- Tool 调用缺少 Scope 或绕过输入校验的次数，目标为零；
- Knowledge Context 相关性和覆盖率；
- 截断率和不兼容项比例；
- `context_ref` 的审计可解析率；
- Agent 调用 Knowledge 写 Tool 的次数，目标为零。
- Agent、Tool Context 或 decision 携带记忆对象版本的次数，目标为零；

运行质量：

- 每次 Run 的两阶段 LLM 和各 Tool 调用次数；
- 两阶段 P50/P95 延迟；
- token 和模型成本；
- Context 利用率、裁剪率和压缩率；
- 分批次数、补充查询次数和失败率；
- 固定输入版本下的重试一致性。

## 21. 实施计划

### P0：Agent、Tools、Workflow 契约和测试样本

- 定义 Agent、Tools 和 Workflow 的职责及协作边界；
- 定义 Knowledge Model Tool 和可配置知识类型定义；
- 定义 Conversation Tool；
- 定义第一阶段 stance/basis 结果契约；
- 定义以第一阶段结果为输入的只读 Knowledge Context Tool；
- 定义第二阶段 `add`、`update`、`keep`、`delete` 决策契约；
- 明确类型 Schema 版本与记忆对象版本的边界，并禁止 Agent 契约携带 `expected_version`；
- 建立各阶段 Tool 权限测试；
- 提供一个测试用 `profile_fact` 类型；
- 建立直接支持、推断支持、直接反对、推断反对、不确定、修正、删除、询问、重复和 Context 截断样本。

### P1：单类型最小闭环

```text
Workflow: Receive Run
  -> Tools: Load Knowledge Model + Read Conversation
  -> Workflow: Enter Stage 1, disable Knowledge Context Tool
  -> Agent: Extract + Stance
  -> Workflow: Validate Stance Results
  -> Workflow: Enter Stage 2, enable Knowledge Context Tool
  -> Agent + Tool: Request Existing Knowledge
  -> Agent: Reconcile + Decide
  -> Workflow: Validate and Return Results
```

P1 同时覆盖两阶段 token 预算、输出预留、低相关内容裁剪和 Evidence 保留。它不实现 Knowledge 写入。

`expected_version` 的注入属于后续下游写入组件实现，不进入 Memory Agent P1。

### P2：可配置类型

- 增加第二个不同 payload 和合并规则的知识类型；
- 证明无需修改 Agent 核心；
- 覆盖多类型结果、身份匹配和当前输入内去重；
- 固定两阶段 Provider 兼容性测试。

### P3：Knowledge Information Provider

- 实现第二个只读 Provider；
- 验证 Scope 隔离、类型兼容、相关性、现有 stance、截断和审计引用；
- 确认 Agent 不依赖 Provider 的存储或生命周期概念。

### P4：回放与评估

- 建立统一回放和模型比较工具；
- 固定抽取、stance、basis 和四种决策的基准集；
- 验证第二阶段重试不会重新判断第一阶段立场；
- 验证相同输入版本的可重复性。

## 22. 待确认决策

1. 各知识类型如何定义对象身份、合并和冲突解决规则；
2. 哪些类型允许长期保存 oppose 立场，哪些情况必须输出 delete；
3. `uncertain` 是否需要原因码；
4. Memory Agent 的触发时机；
5. Parser 和 Validator 是否只能是进程内受信实现；
6. `context_ref` 的审计保留期；
7. Knowledge Context 截断后受限补充查询的最大次数；
8. 两阶段各 Context 分区的默认 token 预算；
9. 分批调用的最大批次数和合并失败策略。

## 23. 职责总结

```text
Trigger Source
  提供 Scope、Message 引用并触发 Run

Conversation Source
  返回原始消息和 Evidence 范围

Knowledge Model Provider
  提供知识类型、两阶段 Prompt/Schema/Parser/Validator 和类型规则

Knowledge Information Provider
  实现 Knowledge Context Tool 背后的只读能力
  第一阶段完成后提供相关现有 Knowledge 和 stance

Downstream Memory Writer
  消费 Agent decisions
  仅在构造实际数据库写命令时注入 expected_version
  处理对象版本冲突和写入重试

Memory Agent
  Agent
    第一阶段抽取知识并判断 stance 与 basis
    第二阶段合并或解决冲突并作出决策
  Tools
    受控读取 Conversation、Knowledge Model 和相关 Knowledge
  Workflow
    控制阶段、Tool 权限、状态转换、校验和重试
  最终返回 add、update、keep 或 delete，不执行这些决策

Model Runtime
  提供模型推理、Context 硬上限和 token 计数
```
