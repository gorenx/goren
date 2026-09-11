# Goren

**受 [DeepSeek Agent Harness](https://github.com/deepseek-ai/deepseek-harness) 架构与 Host 协议启发的 Go Agent 服务端。**

Goren 是独立实现，不是逐文件翻译，也不宣称与 Harness 完全兼容。项目的首要兼容目标是现有 TypeScript Client 与 Go 服务端之间的协议：HTTP/WebSocket 载体、RPC 信封、请求关联、取消、重连行为，以及本项目已纳入的 API Schema。

[English](./README.md)

## 当前可用能力

默认服务是一个 Go 可执行文件，内嵌仓库自有的 React/Vite 对话界面。当前已打通的端到端能力包括：

- Session 持久化、历史、队列操作、取消、标题、Workspace 和 SQLite 搜索；
- DeepSeek 流式对话、Session 级模型选择和图片 Prompt；
- 原生 Tool 执行，以及结构化提问与审批处理；
- stdio 或 Streamable HTTP MCP Client，包含工具刷新和 elicitation；
- Session 持久事实、Projection、Checkpoint 与冷启动恢复。

项目不包含 Harness 浏览器侧 Runtime、原版完整 Web 产品、SDK、ACP、MCP Server、本地 Shell/Filesystem/LSP Tool 等扩展面。Subagent 执行以及 Compaction/Retry 组件已有代码实现，但完整生产接线仍在进行中。

## 本地运行

Goren 当前要求 [`go.mod`](./go.mod) 声明的 Go 版本。生产 Web 构建已提交并嵌入 Go 二进制，因此运行服务不需要 Node.js。

```bash
export DEEPSEEK_API_KEY="your-api-key"
go run ./cmd/goren
```

打开 <http://127.0.0.1:3080>。也可以不设置环境变量，直接在 Web 设置中保存 DeepSeek API Key。Credential 不写入 Session 事件，也不会进入模型可见历史。

默认数据目录是当前平台用户配置目录下的 `goren/`。可用 `-data-dir` 指定其他位置：

```bash
go run ./cmd/goren -listen 127.0.0.1:3080 -data-dir ./data
```

启动参数：

```text
-listen <address>       HTTP 监听地址，默认 127.0.0.1:3080
-data-dir <path>        数据目录
-session-db <path>      Session SQLite 数据库
-workspace-db <path>    Workspace SQLite 数据库
-mcp-server <path>      MCP JSON 配置，可重复指定
-version <value>        host.describe 返回的版本
```

## 连接 MCP Server

可以从 Web 界面连接 MCP Server，也可以在启动时传入配置。stdio 配置文件示例：

```json
{
  "serverName": "local",
  "transport": "stdio",
  "command": "/absolute/path/to/mcp-server",
  "args": [],
  "cwd": "/absolute/path/to/workspace"
}
```

启动命令：

```bash
go run ./cmd/goren -mcp-server ./mcp-local.json
```

Streamable HTTP 配置可写为 `{"serverName":"remote","transport":"streamable-http","url":"http://127.0.0.1:9000/mcp"}`。只连接可信服务，配置文件不得包含秘密。

## 架构

Goren 使用 [GoAkt](https://github.com/tochemey/goakt) 管理运行身份、Mailbox、并发、生命周期和发布订阅。项目不再建立旧 Plugin/Scope Runtime，也不增加第二套 Service Locator。

- Agent Registry 拥有 Agent 身份、父子关系、创建事务和 child-first 关闭。
- AgentLoop 拥有单个 Agent 的执行。
- Session Grain 拥有追加日志、提交、Flush、恢复和关闭。
- Tool Registry 拥有已发布定义；每 Agent Tool Runtime 拥有活动 Tool 实例。
- LLM、Prompt、Workspace、Credentials、MCP 与 Persistence 保持独立所有权边界。
- 进程组合根位于 `internal/server`；Adapter 不拥有业务决策。

配置使用严格校验的 Go 类型，Provider 和 Tool 采用静态链接。SQLite Adapter 基于 `database/sql` 和 owner-defined contract，模型可见输入始终可以从 Session 日志重建。

## 开发验证

无凭据检查：

```bash
go test -short -count=1 ./...
go vet ./...
go build ./...
git diff --check
```

修改 Web UI 时：

```bash
pnpm -C web install --frozen-lockfile
pnpm -C web run build
```

非 `-short` 的完整测试还包含等待框架 passivation 的生命周期用例，因此耗时会明显更长。

## 许可证与归属

Goren 使用 [MIT License](./LICENSE)。

DeepSeek Harness 使用 MIT License，版权归 DeepSeek。复制或派生其实质代码时保留其许可证声明与归属信息。
