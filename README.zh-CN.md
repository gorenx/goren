# Goren

**用 Go 重建 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) Agent 服务端。**

## 项目目标

Goren 的目标是用符合 Go 习惯的方式重建 DeepSeek Harness Agent 服务端架构。首要兼容目标是现有 TypeScript Client 与 Go 服务端之间的协议，包括 HTTP/WebSocket 载体、RPC 信封、API 方法、事件、取消、重连与错误语义。

Goren 保留源系统可观察的行为和职责边界，同时用 typed Go config、`interface`、有状态 Service 与静态链接 Factory 替代 TypeScript 专属机制。它是行为复刻，不是逐文件翻译，也不移植 Node.js/Cordis Runtime。

当前默认服务首先以单一 Go 可执行文件交付可运行的对话主流程，并内嵌自有 Web UI：配置 DeepSeek API Key、创建和恢复 Session、提交 Prompt、接收 Agent 流式输出，以及回答结构化 Question。这不表示已经兼容原版完整 Web 产品、SDK 或仍为 Deferred 的辅助能力；准确基线与边界由复制范围设计统一定义。

## 当前功能对比

状态说明：**已实现**表示 Goren 当前已经提供该行为；**部分实现**会明确已完成的子集；**暂缓**表示该能力不在当前实现范围内；**已替换**表示 Goren 有意采用 Go 原生设计，没有复制 TypeScript 机制。

### 运行时与协议

| 能力 | DeepSeek Harness | Goren 当前状态 |
| --- | --- | --- |
| 语言与运行时 | TypeScript、Node.js | **已实现：** Go |
| 插件组合 | Cordis Scope、Service、Event 与运行时插件 | **已实现：** Go `interface`、状态对象、事件 Scope 与静态链接 Factory |
| Host 传输 | HTTP RPC，以及 Mux、Host 两条 WebSocket 事件流 | **已实现：** 兼容的 `/api`、`/api/respond`、Mux 与 Host 载体 |
| RPC 协议 | 有类型的 Client/Server Request、Response、Receipt、Error 与取消语义 | **已实现：** 纳入范围的信封与错误语义已和固定 TypeScript 源码完成契约验证 |
| API 发现与分发 | `host.describe` 与插件贡献的 Method | **已实现：** 类型化 Catalog、解码、分发及稳定的业务失败/技术失败边界 |
| 实时 Frame | Session、Interaction、Workspace、Status、Error 与 Remote Frame Union | **部分实现：** 已纳入 Session、Interaction、Workspace、Status 与 Error Frame；Remote execution 暂缓 |
| 插件事件 | Decision、Notification 与 Waterfall 扩展点 | **已实现：** 类型化 Go Handler `interface` 与 Scoped publication |
| 类型生成 | Typert Schema、生成式 Client 与 Host Gateway 类型 | **已替换：** 协议兼容的 Go 类型与固定跨语言契约 fixture；不复制 Typert 生成 |
| 动态配置 | Schemastery 与支持 JavaScript 的配置路径 | **已替换：** typed Go config；不支持 `!!js` evaluator |

### Agent、LLM 与工具

| 能力 | DeepSeek Harness | Goren 当前状态 |
| --- | --- | --- |
| Agent Registry 与生命周期 | Agent Scope、状态、销毁与 Session 归属 | **已实现：** Agent Registry、生命周期、状态与 Session 绑定 |
| Inbox | Follow-up、Steer、Inject、Claim 与 Discard 流程 | **已实现：** 有类型的 Inbox Target、顺序、Claim 与 Discard 生命周期 |
| Agent Loop | Step、Request、流式输出、工具执行、续跑、停止与错误 | **已实现：** 从 Prompt 到 `turn/end` 的端到端循环 |
| LLM Runtime | 可插拔 Adapter、有类型 Content、Stream Chunk 与 Usage | **已实现：** 可扩展 Adapter 边界与类型化流式契约 |
| DeepSeek Provider | DeepSeek 官方路由与模型 | **已实现：** 使用 Credential 解析的官方 `https://api.deepseek.com` 路由 |
| LLM Catalog 与选择 | Provider/Model 发现及 Session 级模型选择 | **已实现：** Provider/Model API、Session Model List 与选择 |
| Retry | Retry 分类、等待、Attempt Event 与取消 | **已实现：** Retry Policy 与 Agent Request Attempt 集成 |
| System Prompt | 有序 Section、工具描述、Assembly Event 与变更通知 | **已实现：** Registry、排序、渲染、组装与失效通知 |
| Native Tool Runtime | Tool Registry、Schema 校验、执行事件、结果校验与取消 | **已实现：** 通用原生 Tool Pipeline |
| 内置工具目录 | 文件系统、Shell、LSP、Terminal、Web、Goal、Job、Skill、Subagent 等 | **部分实现：** 当前产品内置工具主要是 `ask_user_question`；其他目录暂缓 |
| User Questions | 结构化单选、多选与自定义回答 | **已实现：** Service、Host 协议、Web Card、Respond 与同一 Turn 续跑 |
| Approval | Policy、Request、Answerer、Audit Event 与 Host Interaction | **部分实现：** Service 与 Host 协议已实现；Web Approval UI 暂缓 |

### Session、数据与产品界面

| 能力 | DeepSeek Harness | Goren 当前状态 |
| --- | --- | --- |
| Session 生命周期 | Create、List、Dispose、Event、Flush 与 Resume | **已实现：** 主生命周期、持久化事实、Flush 边界、冷启动恢复与 Resume |
| 对话 | History、Prompt、流式 Reasoning/Output 与 Tool Message | **已实现：** HTTP/WebSocket 主流程与 Web 渲染 |
| Queue 与取消 | 读取/编辑/删除排队输入及取消运行中的 Turn | **已实现：** Queue Baseline、Mutation 与 Turn Cancellation |
| Session Projection | 可扩展 Projection、Cache、Checkpoint 与实时 Frame | **已实现：** Projection Registry、Fold、Checkpoint/Restore、Baseline 与实时 Frame |
| Session Title | Fallback、手工 Rename 与自动 LLM Title Provider | **已实现：** Fallback、Rename/Refresh，以及可配置的 First-Prompt 或 All-Prompts LLM 生成 |
| Session 持久化 | 可插拔 Persistence，以及 JSONL、SQLite Adapter | **部分实现：** Persistence 边界与 SQLite/sqlc Adapter；未复制 JSONL Adapter |
| Session Query 与导出 | 精确读取、过滤、关系追踪、SQLite 搜索、日志导出与 Query Tool | **部分实现：** live-preferred Query Service、可重建 SQLite/FTS5 Index 与 `session.search` 已实现；导出和 Agent Query Tool 暂缓 |
| Session Fork | 从既有对话创建 Fork | **不在当前范围内** |
| Workspace | Registry、排序、Archive、Session Accounting、持久化与 Web 管理 | **部分实现：** Registry、API、Accounting、排序/Archive 与 SQLite 已实现；Web 管理暂缓 |
| Credentials | Provider/Manager/Store、环境变量、Local Store 与 Host API | **已实现：** 环境变量优先、Owner-only Local JSON Store、只写 Host API 与 Web DeepSeek API Key 设置 |
| Settings | Typed Namespace、文件持久化、Describe 与 Mutation | **暂缓：** 当前只实现标准 absent-provider 兼容响应 |
| Agent Preset 与 Persona | 发现、组合、选择、创作与 Persona Prompt | **暂缓：** 当前只实现标准 empty-roster 兼容响应 |
| Web 应用 | 可扩展浏览器 Runtime 与完整产品 UI | **部分实现：** 仓库自有 React/Vite/Tailwind 对话 UI，包含 Session、History、Streaming、Question 与 Credential 设置 |
| Attachment | Attachment Store、Reference、Upload 与 UI | **暂缓：** 只保留已被 LLM Content 消费的稳定 Image Reference Metadata |

### 扩展能力

| 能力 | DeepSeek Harness | Goren 当前状态 |
| --- | --- | --- |
| 文件系统与编辑 | Local/Sandbox 文件系统、搜索和字符串替换工具 | **暂缓** |
| Shell、Subprocess 与 Terminal | 持久 Bash/PowerShell、子进程与终端会话 | **暂缓** |
| LSP | LSP 生命周期、stdio Adapter 与 Tool 集成 | **暂缓** |
| Sandbox、Guard 与 Permission Preset | Local Policy、Windows ACL/Landlock、超时/重复 Guard 与权限预设 | **暂缓** |
| Web Search 与 Fetch | DeepSeek、Exa、Perplexity 搜索及 HTTP Fetch Tool | **暂缓** |
| MCP 与 ACP | MCP Client Bridge 与 ACP Agent Adapter | **暂缓** |
| Job、Workflow 与 Subagent | Local Job、Worker Workflow、进程内/外 Subagent 与控制工具 | **暂缓** |
| Goal、Plan、TODO 与 Skill | Goal Driver、Plan Mode、TODO Tool 与 Filesystem Skill | **暂缓** |
| Context Compaction | Basic Compaction、Tool Result Pruning、Checkpoint 与 Compact Command | **暂缓** |
| Hook 与 Extension | Codex/Claude Hook，以及 Cordis Host/Client/Tool/UI Extension | **暂缓** |
| Headless 与 SDK | Headless Bundle，以及 TypeScript、Python SDK Surface | **暂缓** |
| Code Runtime 与 E2B | Worker-thread Code Execution，以及 E2B 文件系统/子进程 Adapter | **暂缓；**代码执行不是产品中心 |

该矩阵只提供易读的当前快照，不作为第二份进度记录。证据等级与剩余 Gate 由实施进度设计统一记录。

英文版见 [README.md](./README.md)。

DeepSeek Harness 使用 MIT License，版权归 DeepSeek。复制或派生其实质代码时必须保留原许可证声明与归属信息。
