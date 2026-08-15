# Goren

**一个 Go 版的 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)。**

Goren 从用符合 Go 习惯的方式复刻 DeepSeek Harness 的核心职责、协议和 Agent 主流程开始。DeepSeek Harness 是起点，不是终点：Goren 不是围绕代码的 Harness，也不是围绕工作的 Harness，而是围绕人的 Harness。它的目标是让人在不同工具、任务和上下文之间仍然保有连续性、选择权与控制权。

## 当前功能对比

| 能力 | DeepSeek Harness | Goren 当前状态 |
| --- | --- | --- |
| 运行时与插件模型 | TypeScript 运行时与 Cordis 插件 | Go 运行时、`interface` 与静态链接 Factory——已实现 |
| Host 协议 | HTTP、RPC 与 WebSocket 的基准实现 | 已纳入范围的 HTTP、RPC 与 WebSocket 契约兼容——已实现 |
| Agent Loop | 流式输出、工具调用、续跑、取消与事件 | 核心循环与续跑流程——已实现 |
| 会话与持久化 | 会话生命周期及 JSONL、SQLite 持久化 | 会话生命周期、SQLite/sqlc 持久化与冷启动恢复——已实现 |
| LLM 集成 | 可插拔的 LLM 运行时 | 可扩展 LLM 边界下的 DeepSeek 集成——核心子集 |
| 工具与人机交互 | 广泛的工具目录、Question 与 Approval 流程 | 通用原生工具运行时及 Question、Approval 核心流程——核心子集 |
| Web 体验 | 完整且可扩展的 Web 应用 | 核心对话、历史、流式输出、Question 与 API Key 设置——核心子集 |
| Workspace | Workspace 生命周期与 Web 体验 | Registry、API 与 SQLite 持久化；Web 管理暂缓——核心子集 |
| Settings、Preset 与扩展 | 完整的配置和扩展能力 | 除主流程兼容所需路径外暂缓 |
| 编码与工作集成 | Shell、文件系统、Terminal、LSP、Sandbox、MCP、ACP、Jobs 等能力 | 暂缓；它们不是产品中心 |

“核心子集”表示端到端主流程已经可用，但尚未复刻 DeepSeek Harness 的完整能力面。可核验的当前状态见[实施进度](./zh-CN/08-implementation-progress.md)，详细架构见[中文详设索引](./zh-CN/README.md)。

英文版见 [README.md](./README.md)。

DeepSeek Harness 使用 MIT License，版权归 DeepSeek。复制或派生其实质代码时必须保留原许可证声明与归属信息。
