# API Proxy Session 子模块

`apiproxy/session` 拥有浏览器 Session API 的具体实现。它不是 `session` 领域的副本，也不定义 RPC DTO、frame 或持久化格式；路径表达的是“API Proxy 内的 Session capability implementation”。权威跨模块设计见 [`zh-CN/16-session-api-gateway-and-live-frames.md`](../../zh-CN/16-session-api-gateway-and-live-frames.md)。

## 职责

本子模块包含：

- `Gateway`：实现根 `apiproxy.SessionAPI` 的稳定 façade，只委派 method；
- `sessionReader`：`list`、`history` 与 search visibility；
- `sessionLifecycle`：`create`、`rename` 与 Workspace attach；
- `sessionModels`：model catalog、route validation 与 selection；
- `sessionConversation`：`prompt`、pending queue mutation 与 `cancel`；
- `SearchGateway`：固定 current user/assistant surface 的 Session Search adapter。

`AgentSessions` 负责：

- 按 Session ID 串行化并发创建或 cold resume；
- 在创建、恢复和幂等采用后重新校验 `cwd`、`agentPreset` 与 ordinary/subagent ownership；
- 为每个 live Agent 安装并持有 `ModelSelectionRef`；
- Agent dispose 时删除与该实例绑定的 selection 引用；
- 把 Directory Provisioner 作为创建新 Session 前唯一的文件系统边界。

本子模块消费根 `apiproxy` 的 typed request/result、canonical RPC error constructor 与 Session wire projector，但不拥有其协议定义。本子模块也不负责 Mux/Host frame、连接 subscriber、数据库读写实现或 Agent Loop。`sesspersist.Persistence` 只提供 cold inspection；恢复规则仍由 `AgentSessions` 调用 Agent Registry 的 `Resume` 完成。

## 工作原理

```mermaid
flowchart LR
    A[Root SessionAPI registration] --> B[Gateway facade]
    B --> C1[Reader]
    B --> C2[Lifecycle]
    B --> C3[Models]
    B --> C4[Conversation]
    C2 --> D[AgentSessions]
    C3 --> D
    C4 --> D
    D --> C{live Agent exists?}
    C -->|yes| E[validate ordinary ownership and adoption]
    C -->|no| F[Persistence Inspect]
    F -->|cold Session| G[Agent Registry Resume]
    F -->|missing and create allowed| H[Directory Provisioner]
    H --> I[Agent Registry Create]
    G --> E
    I --> E
    E --> J[Agent and ModelSelectionRef]
```

同一 Session ID 的并发调用共享一次 acquisition，但不共享 adoption decision。等待者拿到 Agent 后仍用自己的 `cwd` 和 `agentPreset` 再校验，避免先到请求替后到请求接受冲突配置。

## 上下游与生命周期

- 上游：根 `apiproxy.RegisterSessionAPI` 与 Assembly composition root。
- 下游：Agent Registry、Session LiveStore、Session Persistence、Default Model、Directory Provisioner。
- `AgentSessions` 跟随 API Proxy Plugin Scope；Agent dispose listener 也由该 Scope 撤销。
- 技术错误原样返回上游；`CWDConflictError`、`PresetConflictError` 与 `SubagentOwnershipError` 保留结构化事实，再由本子模块转换为根协议定义的 canonical RPC error。

## Actor 迁移中的职责

`api.go`、`decode.go` 现在拥有本领域全部请求和响应契约，根包不保留别名。
`reader_ports.go` 定义独立快照、活动 Session 查询、持久化历史及投影缓存端口；`read.go` 保留历史分页和基线规则。
`agents.go` 的 activation 只校验创建／恢复和接管条件，不维护创建等待表或模型缓存；Agent 端口由 Actor Client 实现。
`gateway.go` 装配这些业务用例，框架调度不进入业务对象。

模型选择不再使用 Scope／Plugin。队列提升要求单次 Promote 操作，禁止移除和 Steer 分开发送。
当前 Gateway 及纯业务测试可构建运行；Actor Client 与生产装配仍在接入。
迁移依据见 [API Proxy 设计](../../zh-CN/actor-design/38-api-proxy-module.md)，问题和未完成项见 [集成记录](../../zh-CN/actor-design/40-integration-findings.md)。

模型配置查询和修改属于 Models 端口，Actor Client 直接请求 LLM；Agent 端口不提供配置方法。Prompt 的路由准入读取 LLM 配置，AgentLoop 只接收用户消息并提交生成内容。
