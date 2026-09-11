# Goren

**A Go Agent server inspired by the architecture and Host protocol of [DeepSeek Agent Harness](https://github.com/deepseek-ai/deepseek-harness).**

Goren is an independent implementation, not a line-by-line port and not a claim of complete Harness compatibility. Its primary compatibility target is the protocol between the existing TypeScript client and the Go server: HTTP and WebSocket carriers, RPC envelopes, request correlation, cancellation, reconnect behavior, and the API schemas adopted by this project.

[简体中文](./README.zh-CN.md)

## What works today

The default server is one Go executable with an embedded React/Vite conversation UI. The current end-to-end path includes:

- persistent sessions, history, queue operations, cancellation, titles, workspaces, and SQLite search;
- streamed DeepSeek conversations with per-session model selection and image prompts;
- native tool execution with structured questions and approval handling;
- MCP client connections over stdio or Streamable HTTP, including tool refresh and elicitation;
- durable Session facts, projections, checkpoints, and cold-start recovery.

The browser-side Harness runtime, full original Web product, SDKs, ACP, MCP Server, local shell/filesystem/LSP tools, and other extension surfaces are not included. Subagent execution and compaction/retry components exist in the codebase, but their complete production wiring is still in progress.

## Run locally

Goren currently requires the Go version declared in [`go.mod`](./go.mod). The production Web build is committed and embedded, so Node.js is not required to run the server.

```bash
export DEEPSEEK_API_KEY="your-api-key"
go run ./cmd/goren
```

Open <http://127.0.0.1:3080>. You can also save the DeepSeek API key from the Web settings instead of exporting it. Credentials are kept outside Session events and are never part of model-visible history.

By default, Goren stores data in the platform user-config directory under `goren/`. Use `-data-dir` to select another location:

```bash
go run ./cmd/goren -listen 127.0.0.1:3080 -data-dir ./data
```

Available startup options:

```text
-listen <address>       HTTP listen address (default 127.0.0.1:3080)
-data-dir <path>        data directory
-session-db <path>      Session SQLite database
-workspace-db <path>    Workspace SQLite database
-mcp-server <path>      MCP JSON config; repeat for multiple servers
-version <value>        version reported by host.describe
```

## Connect an MCP server

MCP servers can be connected from the Web UI or provided at startup. A stdio configuration file looks like this:

```json
{
  "serverName": "local",
  "transport": "stdio",
  "command": "/absolute/path/to/mcp-server",
  "args": [],
  "cwd": "/absolute/path/to/workspace"
}
```

Start Goren with:

```bash
go run ./cmd/goren -mcp-server ./mcp-local.json
```

For Streamable HTTP, use `{"serverName":"remote","transport":"streamable-http","url":"http://127.0.0.1:9000/mcp"}`. Only connect trusted servers; configuration files must not contain secrets.

## Architecture

Goren uses [GoAkt](https://github.com/tochemey/goakt) for runtime identity, mailboxes, concurrency, lifecycle, and publication. It does not recreate the legacy Plugin/Scope runtime or add a second service locator.

- Agent Registry owns Agent identity, parent/child relationships, creation, and child-first shutdown.
- AgentLoop owns the execution of one Agent.
- Session Grain owns the append-only log, commits, flush, recovery, and close.
- Tool Registry owns published definitions; each Agent Tool Runtime owns its active tool instances.
- LLM, Prompt, Workspace, Credentials, MCP, and persistence retain separate ownership boundaries.
- The process composition root lives in `internal/server`; adapters do not own business decisions.

Configuration is typed Go data with strict validation. Providers and tools are statically linked. SQLite adapters use `database/sql` with owner-defined contracts, and model-visible input remains reconstructable from the Session log.

## Development

Run the keyless checks with:

```bash
go test -short -count=1 ./...
go vet ./...
go build ./...
git diff --check
```

When changing the Web UI:

```bash
pnpm -C web install --frozen-lockfile
pnpm -C web run build
```

The non-short full suite also contains lifecycle tests that intentionally wait for framework passivation and can take substantially longer.

## License and attribution

Goren is available under the [MIT License](./LICENSE).

DeepSeek Harness is MIT-licensed and copyright DeepSeek. Substantial copied or derived portions retain the required license notice and attribution.
