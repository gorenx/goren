# Goren

Goren 是 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) Agent 服务端架构的 Go 复刻。首要兼容目标是现有 TypeScript 客户端与 Go Agent 服务端之间的通信协议，包括 RPC 信封、HTTP/WebSocket 载体、API 方法、事件、取消和错误语义；插件组合使用 Go `interface` 与静态链接的 Factory 实现。

默认服务内嵌一个只覆盖主流程的极简 Web UI，可创建和选择会话、读取历史、发送文本并渲染 Agent 流式回复。项目不复制原版基于 React/plugin runtime 的完整 DeepSeek Harness Web 产品、浏览器客户端运行时、DeepSeek Harness SDK 或 Python SDK。Headless、ACP、MCP 及依赖 Typert 的辅助端点均为 Deferred，只有后续范围决策明确纳入时才实现。

当前 `llm` 包早于这次项目主线调整，只是迁移时可复用的实现材料，不能证明已经兼容 DeepSeek Harness API。兼容性必须以固定的 TypeScript 源码基线和跨语言契约 fixtures 为准。

详细复制范围、Go 技术架构、协议兼容策略、技术选型和实施路线图从[中文详设索引](./zh-CN/README.md)进入。英文背景见 [README.md](./README.md)。

DeepSeek Harness 使用 MIT License，版权归 DeepSeek。复制或派生其实质代码时必须保留原许可证声明与归属信息。
