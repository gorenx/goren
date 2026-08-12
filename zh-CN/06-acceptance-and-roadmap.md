# 06 验收、评估与实施路线图

状态：Draft

本文负责 Memory Agent 输出的验收场景、质量指标、实施阶段和待确认决策。它不验收 Knowledge 如何存储、更新或发布。

以下场景默认启用部署方配置的 `profile_fact` 类型。

## 19. 验收场景

### 19.1 用户直接支持

输入：

```text
“我主要使用 Go 开发后端服务。”
```

预期：

- LLM 直接输出一个 `profile_fact`；
- payload 表达用户主要使用 Go 开发后端服务；
- `stance = support`；
- `basis = explicit`；
- Evidence 指向当前消息；
- 不输出 Knowledge 管理命令。

### 19.2 Agent 推断用户支持

前文询问：

```text
“以后都用简短回答，可以吗？”
```

用户回答：

```text
“就这样。”
```

预期：

- LLM 结合前文得到“用户偏好简短回答”；
- `stance = support`；
- `basis = inferred`；
- Evidence 包含当前用户消息，并保留用于指代解析的上下文引用。

### 19.3 已有相同 Knowledge

只读接口返回“用户主要使用 Go”，新输入再次表达相同事实。

预期：

- LLM 仍直接输出该知识；
- `stance = support`；
- `basis = explicit`；
- `knowledge_refs` 引用已有信息；
- 不增加“重复”状态。

### 19.4 用户直接修正

只读接口返回“用户主要使用 Go”，当前输入为：

```text
“我已经不用 Go 了，现在主要使用 Rust。”
```

预期返回两项：

- “主要使用 Go”：`stance = oppose`、`basis = explicit`；
- “主要使用 Rust”：`stance = support`、`basis = explicit`。

不输出冲突、取代、撤回、更新或删除等额外状态。

### 19.5 用户直接反对或撤回

只读接口返回“用户偏好简短回答”，当前输入为：

```text
“我不再支持之前的回答长度偏好。”
```

预期：

- 返回回答长度偏好对象；
- `stance = oppose`；
- `basis = explicit`；
- Evidence 指向当前消息；
- Agent 不决定下游是否删除该知识。

### 19.6 Agent 推断用户反对

前文询问：

```text
“还要保留之前的回答长度偏好吗？”
```

用户回答：

```text
“不用了。”
```

预期：

- LLM 结合前文得到用户反对继续保留回答长度偏好；
- `stance = oppose`；
- `basis = inferred`；
- 如果无法可靠解析“不用了”所指对象，则必须改为 `uncertain`。

### 19.7 不确定

输入是含糊表述、假设或转述，无法确定用户自己的最终立场。

预期：

- 能抽取知识对象时返回该对象；
- `stance = uncertain`；
- 不设置 `basis`；
- 不猜测为 support 或 oppose。

### 19.8 纯询问

输入：

```text
“我之前主要使用什么语言？”
```

预期返回空 `results`，不增加 `ignore` 等 stance。

### 19.9 当前输入重复

用户在多条消息中重复表达同一偏好。

预期只返回一个规范化知识对象，并合并当前消息 Evidence。

### 19.10 自定义知识类型

部署方增加 `long_term_instruction` 类型及其 Prompt、Schema、Parser 和 Validator，Agent 核心不修改。

输入：

```text
“以后审查代码时，先列出会阻断合并的问题。”
```

预期直接返回 `kind = long_term_instruction`、对应 payload、`stance = support` 和 `basis = explicit`。

### 19.11 Knowledge Context 不完整

只读接口返回 `truncated = true`，缺失信息会影响最终立场判断。

预期返回 `stance = uncertain`、不设置 basis，并在运行记录中保存截断状态和 `context_ref`。

### 19.12 Context 超限

当前消息、类型 Prompt/Schema、历史和 Knowledge Context 超过模型硬上限。

预期：

- 保留当前消息、Evidence、指令、Schema 和输出预留；
- 优先裁剪低相关 Knowledge，再压缩较早历史；
- 必要时分批处理并在返回前按知识对象合并；
- 任一模型调用均不超过硬上限；
- 无法容纳最小必需内容时明确失败。

## 20. 评估指标

语义质量：

- 按 `kind` 统计的知识抽取 precision 和 recall；
- `support`、`oppose`、`uncertain` 分类准确率；
- `explicit`、`inferred` 判断准确率；
- 四种确定性组合 `support+explicit`、`support+inferred`、`oppose+explicit`、`oppose+inferred` 的混淆矩阵；
- 当前输入内重复合并准确率；
- Evidence coverage；
- 临时内容误识别率；
- 不确定性校准。

接口质量：

- Knowledge Context 相关性和覆盖率；
- 截断率和不兼容项比例；
- `context_ref` 的审计可解析率；
- Agent 输出 Knowledge 管理命令的次数，目标为零。

运行质量：

- 每次 Run 的 LLM 和只读接口调用次数；
- P50/P95 延迟；
- token 和模型成本；
- Context 利用率、裁剪率和压缩率；
- 分批次数和失败率；
- 固定输入版本下的重试一致性。

## 21. 实施计划

### P0：契约和测试样本

- 定义 Knowledge Model Port 和知识类型定义；
- 定义只读 Knowledge Context Port；
- 定义通用结果信封、三种 stance 和两种确定性 basis；
- 提供一个测试用 `profile_fact` 类型；
- 建立直接支持、推断支持、直接反对、推断反对、不确定、修正、询问、重复和 Context 截断样本。

### P1：单类型最小闭环

```text
Message References
  -> Load Knowledge Model
  -> Optional Knowledge Context Request
  -> Build Context
  -> LLM Direct Output
  -> Parse and Validate
  -> Return Results
```

P1 同时覆盖 token 预算、输出预留、低相关内容裁剪和 Evidence 保留。

### P2：可配置类型

- 增加第二个不同 payload 的知识类型；
- 证明无需修改 Agent 核心；
- 覆盖多类型结果和当前输入内去重；
- 固定 Provider 兼容性测试。

### P3：Knowledge Information Provider

- 实现第二个只读 Provider；
- 验证 Scope 隔离、类型兼容、相关性、截断和审计引用；
- 确认 Agent 不依赖 Provider 的存储或生命周期概念。

### P4：回放与评估

- 建立统一回放和模型比较工具；
- 固定抽取、stance 和 basis 基准集；
- 验证相同输入版本的可重复性。

## 22. 待确认决策

1. 同一知识对象的规范化身份由 Prompt 还是确定性规则辅助判断；
2. `uncertain` 是否需要原因码；
3. Memory Agent 的触发时机；
4. Parser 和 Validator 是否只能是进程内受信实现；
5. `context_ref` 的审计保留期；
6. Knowledge Context 截断后是否允许一次受限补充查询；
7. 各 Context 分区的默认 token 预算；
8. 分批调用的最大批次数和合并失败策略。

## 23. 职责总结

```text
Trigger Source
  提供 Scope、Message 引用并触发 Run

Conversation Source
  返回原始消息和 Evidence 范围

Knowledge Model Provider
  提供知识类型、Prompt、Schema、Parser 和 Validator

Knowledge Information Provider
  通过只读接口提供相关 Knowledge

Memory Agent
  管理 Context
  调用 LLM 直接抽取知识并判断 stance 与 basis
  返回 explicit/inferred support、explicit/inferred oppose 或 uncertain

Model Runtime
  提供模型推理、Context 硬上限和 token 计数
```
