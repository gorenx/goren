# 03 接口与数据契约

状态：Draft

本文负责 Knowledge Model、只读 Knowledge Context，以及 Memory Agent 输入输出的数据契约。立场含义见[工作流与立场判断](./02-workflow-and-decisions.md)，Knowledge 查询边界见[Knowledge 信息接口](./04-knowledge-context-interface.md)。

## 11. 接口

### 11.1 Knowledge Model Port

```text
LoadKnowledgeModel(version) -> KnowledgeModel
```

返回本次启用的知识类型定义。每种类型包含：

- `kind` 和 Schema 版本；
- 类型语义、抽取范围和非目标；
- Prompt；
- 输出 Schema；
- Parser；
- Validator。

Prompt、Schema、Parser 和 Validator 必须作为一个兼容版本发布。Agent 核心不得为某个 `kind` 增加硬编码分支。

### 11.2 Knowledge Context Port

```text
RequestKnowledgeContext(request) -> KnowledgeContext
```

该接口只读。请求包含 Scope、Knowledge Model 版本、启用类型、输入 Focus 和返回预算；响应包含 LLM 判断所需的相关 Knowledge、不透明引用、可选 Evidence、截断状态和 token 用量。

接口不暴露 Repository 方法、表结构、查询语言、对象生命周期或 Knowledge 管理命令。

## 12. 数据契约

示例中的 `profile_fact` 是部署方配置的类型，不是 Agent 内建类型。

### 12.1 Run Input

```json
{
  "run_id": "run_123",
  "scope": {
    "workspace_id": "workspace_1",
    "subject_id": "user_1",
    "thread_id": "thread_9"
  },
  "source_refs": ["message_100", "message_101"],
  "knowledge_model_version": "static-memory-v1",
  "agent_version": "memory-agent-v1",
  "policy_version": "default-v1",
  "context_policy_version": "context-v1"
}
```

实际消息由 Memory Agent 从 Conversation Source 读取。调用方不传入已经拼装好的 Prompt。

### 12.2 Knowledge Model

```json
{
  "version": "static-memory-v1",
  "types": [
    {
      "kind": "profile_fact",
      "schema_version": "1",
      "prompt_ref": "profile-fact-v1",
      "output_schema_ref": "profile-fact-output-v1",
      "parser_ref": "profile-fact-parser-v1",
      "validator_ref": "profile-fact-validator-v1"
    }
  ]
}
```

Run 记录这些稳定引用。Prompt 正文、Schema 内容和 Parser 实现由受信 Provider 按版本加载。

### 12.3 Knowledge Context Request

```json
{
  "scope": {
    "workspace_id": "workspace_1",
    "subject_id": "user_1"
  },
  "knowledge_model_version": "static-memory-v1",
  "kinds": ["profile_fact"],
  "focus": "primary development language",
  "evidence_depth": "references",
  "max_items": 20,
  "max_tokens": 3000
}
```

### 12.4 Knowledge Context Response

```json
{
  "context_ref": "knowledge_context_42",
  "items": [
    {
      "knowledge_ref": "knowledge_item_9",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Go"
      },
      "evidence": ["message_80"]
    }
  ],
  "truncated": false,
  "token_count": 180
}
```

`context_ref` 和 `knowledge_ref` 是不透明引用。Agent 不从引用格式推断存储、版本或生命周期信息。

### 12.5 Run Output

```json
{
  "run_id": "run_123",
  "knowledge_model_version": "static-memory-v1",
  "results": [
    {
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Go"
      },
      "stance": "oppose",
      "evidence": ["message_101"],
      "knowledge_refs": ["knowledge_item_9"]
    },
    {
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Rust"
      },
      "stance": "support",
      "evidence": ["message_101"],
      "knowledge_refs": []
    }
  ]
}
```

`stance` 只能为：

```text
support
oppose
uncertain
```

输出不包含中间对象、对象间关系状态或 Knowledge 管理命令。
