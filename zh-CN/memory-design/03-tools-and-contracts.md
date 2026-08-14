# 03 接口与数据契约

状态：Draft

本文负责 Memory Agent 使用的 Tools，以及两阶段输入输出的数据契约。两阶段语义见[工作流与知识决策](./02-workflow-and-decisions.md)，Knowledge 查询边界见[Knowledge 信息接口](./04-knowledge-context-interface.md)。

## 11. Agent Tools

Tool 是 Memory Agent 内部提供给 Agent 和 Workflow 的受控能力。外部 Provider 实现 Tool 背后的读取接口，但不直接进入 Agent Context。每次调用都必须经过 Workflow 的阶段权限和该 Tool 适用的输入、Scope、预算校验。

### 11.1 Knowledge Model Tool

```text
load_knowledge_model(version) -> KnowledgeModel
```

返回本次启用的知识类型定义。每种类型包含：

- `kind` 和 Schema 版本；
- 类型语义、抽取范围和非目标；
- 第一阶段的 Prompt、输出 Schema、Parser 和 Validator；
- 第二阶段的 Prompt、输出 Schema、Parser 和 Validator；
- 对象身份、合并和冲突解决规则。

两阶段的 Prompt、Schema、Parser、Validator 和类型规则必须作为一个兼容版本发布。`Entity`、`Relation`、`Claim` 不是固定内建类型；部署方可以通过该 Tool 和 Prompt 定义任意 `kind`。Agent 核心不得为某个 `kind` 增加硬编码分支。

### 11.2 Conversation Tool

```text
read_conversation(scope, source_refs) -> Conversation
```

按 Run 的 Scope 和 Message 引用返回有序消息、角色、时间以及可引用的 Evidence 范围。Tool 不判断哪些内容应成为 Knowledge，也不返回超出请求 Scope 的消息。

### 11.3 Knowledge Context Tool

```text
request_knowledge_context(request) -> KnowledgeContext
```

该 Tool 只读，并且只能在第一阶段结果产生并通过校验后调用。请求包含 Scope、Knowledge Model 版本、第一阶段结果和返回预算；响应包含第二阶段判断所需的现有 Knowledge、现有 stance、不透明引用、可选 Evidence、截断状态和 token 用量。

Tool 不暴露 Repository 方法、表结构、查询语言、对象生命周期或写入能力。

## 12. 数据契约

示例中的 `profile_fact` 是部署方配置的类型，不是 Agent 内建类型。

本章中的 `schema_version` 是知识类型 Schema 版本，不是记忆对象版本。所有 Agent Tool 请求、响应和 Run Output 都不得包含记忆对象版本或 `expected_version`。

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

实际消息由 Memory Agent 使用 Conversation Tool 读取。调用方不传入已经拼装好的 Prompt。

### 12.2 Knowledge Model

```json
{
  "version": "static-memory-v1",
  "types": [
    {
      "kind": "profile_fact",
      "schema_version": "1",
      "stage1_prompt_ref": "profile-fact-stage1-v1",
      "stage1_output_schema_ref": "profile-fact-stage1-output-v1",
      "stage1_parser_ref": "profile-fact-stage1-parser-v1",
      "stage1_validator_ref": "profile-fact-stage1-validator-v1",
      "stage2_prompt_ref": "profile-fact-stage2-v1",
      "stage2_output_schema_ref": "profile-fact-stage2-output-v1",
      "stage2_parser_ref": "profile-fact-stage2-parser-v1",
      "stage2_validator_ref": "profile-fact-stage2-validator-v1",
      "identity_rules_ref": "profile-fact-identity-v1",
      "merge_rules_ref": "profile-fact-merge-v1"
    }
  ]
}
```

Run 记录这些稳定引用。Prompt 正文、Schema 内容和 Parser 实现由受信 Provider 按版本加载。

### 12.3 第一阶段结果

第一阶段只根据 Conversation Context 抽取知识对象并判断用户立场：

```json
{
  "stance_results": [
    {
      "result_ref": "result_1",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Go"
      },
      "stance": "oppose",
      "basis": "explicit",
      "evidence": ["message_101"]
    },
    {
      "result_ref": "result_2",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Rust"
      },
      "stance": "support",
      "basis": "explicit",
      "evidence": ["message_101"]
    }
  ]
}
```

`stance` 只能为 `support`、`oppose` 或 `uncertain`。当 stance 为 `support` 或 `oppose` 时，`basis` 必须为 `explicit` 或 `inferred`；`uncertain` 必须省略 `basis`。

### 12.4 Knowledge Context Request

Agent 使用已经通过第一阶段校验的结果发起查询：

```json
{
  "scope": {
    "workspace_id": "workspace_1",
    "subject_id": "user_1"
  },
  "knowledge_model_version": "static-memory-v1",
  "stance_results": [
    {
      "result_ref": "result_1",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Go"
      },
      "stance": "oppose",
      "basis": "explicit"
    },
    {
      "result_ref": "result_2",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Rust"
      },
      "stance": "support",
      "basis": "explicit"
    }
  ],
  "evidence_depth": "references",
  "max_items": 20,
  "max_tokens": 3000
}
```

Provider 可以根据类型身份规则把这些结果转换为检索条件，但 Agent 不传入表名、索引名或查询语言。

### 12.5 Knowledge Context Response

```json
{
  "context_ref": "knowledge_context_42",
  "matches": [
    {
      "result_ref": "result_1",
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
          "stance": "support",
          "basis": "explicit",
          "evidence": ["message_80"]
        }
      ]
    },
    {
      "result_ref": "result_2",
      "items": []
    }
  ],
  "truncated": false,
  "token_count": 210
}
```

响应必须为每个请求中的 `result_ref` 返回一个 `matches` 条目；`items = []` 明确表示没有相关现有对象。`context_ref` 和 `knowledge_ref` 是不透明引用，Agent 不从引用格式推断存储、对象版本或生命周期信息。现有 Knowledge 的 `basis` 可以缺失，因为外部系统不一定保留该信息；现有 `stance` 必须存在，第二阶段才能比较立场。

### 12.6 第二阶段决策

第二阶段 LLM 根据第一阶段结果、Knowledge Context 和类型规则进行身份匹配、合并与冲突解决，只能返回以下决策：

```json
{
  "decisions": [
    {
      "decision": "update",
      "target_ref": "knowledge_item_9",
      "result_refs": ["result_1"],
      "object": {
        "kind": "profile_fact",
        "schema_version": "1",
        "payload": {
          "subject": "user_1",
          "attribute": "primary_language",
          "value": "Go"
        },
        "stance": "oppose",
        "basis": "explicit",
        "evidence": ["message_101"]
      }
    },
    {
      "decision": "add",
      "result_refs": ["result_2"],
      "object": {
        "kind": "profile_fact",
        "schema_version": "1",
        "payload": {
          "subject": "user_1",
          "attribute": "primary_language",
          "value": "Rust"
        },
        "stance": "support",
        "basis": "explicit",
        "evidence": ["message_101"]
      }
    }
  ]
}
```

决策字段只能为：

```text
add
update
keep
delete
```

- `add`：没有可复用的现有对象，返回需要新增的完整对象；
- `update`：存在同一对象，但合并后的 payload、stance、basis 或 Evidence 需要替换，必须携带 `target_ref` 和完整对象；
- `keep`：现有对象已经正确，必须携带 `target_ref`；
- `delete`：类型规则和当前语义判断要求现有对象不再保留，必须携带 `target_ref`。

每项决策必须引用促成它的 `result_refs`。`update`、`keep` 和 `delete` 的 `target_ref` 必须来自本次 Knowledge Context。stance 不直接映射为某个决策；LLM 必须结合现有对象和类型规则判断。decision 不携带对象当前版本或 `expected_version`。

### 12.7 Run Output

```json
{
  "run_id": "run_123",
  "knowledge_model_version": "static-memory-v1",
  "context_ref": "knowledge_context_42",
  "stance_results": [
    {
      "result_ref": "result_1",
      "kind": "profile_fact",
      "schema_version": "1",
      "payload": {
        "subject": "user_1",
        "attribute": "primary_language",
        "value": "Go"
      },
      "stance": "oppose",
      "basis": "explicit",
      "evidence": ["message_101"]
    }
  ],
  "decisions": [
    {
      "decision": "update",
      "target_ref": "knowledge_item_9",
      "result_refs": ["result_1"],
      "object": {
        "kind": "profile_fact",
        "schema_version": "1",
        "payload": {
          "subject": "user_1",
          "attribute": "primary_language",
          "value": "Go"
        },
        "stance": "oppose",
        "basis": "explicit",
        "evidence": ["message_101"]
      }
    }
  ]
}
```

Run Output 保留第一阶段结果，便于审计立场判断；`decisions` 是第二阶段的语义结论。第一阶段没有结果时，省略 `context_ref` 并返回空数组。Agent 返回这些结论后即完成；Memory Agent 不提供 Knowledge 写 Tool，也不确认下游是否执行成功。

下游写入组件消费 decision 时，使用 `target_ref` 定位写入目标。只有在构造实际数据库写命令时，它才取得并注入 `expected_version`。该字段不回填到 Run Output，也不由 Agent 管理。
