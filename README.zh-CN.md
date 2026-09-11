# Goren

Go 编写的 Agent 服务端，将持久化对话、模型流式输出和工具执行集成在一个内嵌 Web 界面的可执行文件中。

Goren 借鉴 DeepSeek Agent Harness 的实现，直接使用 GoAkt 构建 Actor 运行时。项目将 Agent 执行、对话历史、模型调用和工具交互组织为职责与生命周期清晰的 Go 服务。

## 核心体验

通过内置对话界面，你可以配置 DeepSeek API Key、创建或选择 Session、查看历史、发送 Prompt，并实时接收 Agent 输出。工具需要补充信息时，可以直接回答结构化问题，继续当前执行轮次。

- **持久化对话。** Session 保存对话历史与执行事实，支持 SQLite 存储、搜索和恢复。
- **交互式执行。** 流式输出、取消、结构化提问和审批处理贯穿对话过程。
- **工具接入。** 原生工具和 MCP Client 连接将外部能力接入 Agent 执行；MCP 连接支持 stdio 与 Streamable HTTP。
- **单文件服务。** React Web 界面的生产构建内嵌在 Go 可执行文件中，运行服务无需 Node.js 运行时。

## 设计取向

Goren 将 Agent、Session、Tools 和 LLM 分为独立的职责主体。AgentLoop 负责单个 Agent 的执行；Session 拥有追加式记录，模型可见输入可以从中重建。GoAkt 提供运行身份、消息传递和生命周期管理。

各项能力通过具名 Go 配置和静态链接的 Provider 显式组装。凭据由独立组件管理，与设置及对话历史分离。存储适配器通过能力所属的契约持久化数据，业务行为由应用层的职责主体决定。

这些设计旨在让 Agent 行为更容易追踪、扩展和测试，并保持执行、持久化与传输之间的职责边界清晰。

## 与 DeepSeek Agent Harness 的关系

DeepSeek Agent Harness 为选定的实现模式和行为提供参考。Goren 独立确定所有权、生命周期和公开契约，不承诺完整的行为或协议兼容。

Goren 自行定义 HTTP `/api` 载体、WebSocket 下行、请求关联、取消和 API Schema。端点或事件名称相似，不代表现有 Harness Client 可以兼容接入。

项目聚焦 Go Agent 服务端及其核心对话界面。原版 Harness 浏览器运行时、完整 Web 产品和 SDK 不在这一范围内；ACP、MCP Server 等更广的接口仍属后续范围。MCP Client 连接能力不等于 Goren 提供 MCP Server。

项目仍在持续开发。仓库中存在某个组件，并不意味着它已经接入默认服务或具备生产可用性。

## 许可证

Goren 使用 [MIT License](./LICENSE)。复制或派生自 DeepSeek Agent Harness 的部分保留适用的许可证声明与归属信息。
