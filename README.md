# Goren

**A Go version of [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness).**

Goren begins by reproducing the core responsibilities, protocol, and Agent flow of DeepSeek Harness in idiomatic Go. DeepSeek Harness is the starting point, not the destination: Goren is not a harness for code and not a harness for work, but a harness around the person. Its direction is to help a person retain continuity, choice, and control across tools, tasks, and contexts.

## Current feature comparison

| Capability | DeepSeek Harness | Goren today |
| --- | --- | --- |
| Runtime and plugin model | TypeScript runtime with Cordis plugins | Go runtime with interfaces and statically linked factories — available |
| Host protocol | Canonical HTTP, RPC, and WebSocket implementation | Included HTTP, RPC, and WebSocket contracts are compatible — available |
| Agent loop | Streaming, tool calls, continuation, cancellation, and events | Core loop and continuation flow — available |
| Sessions and persistence | Session lifecycle with JSONL and SQLite persistence | Session lifecycle with SQLite/sqlc persistence and cold recovery — available |
| LLM integration | Pluggable LLM runtime | DeepSeek integration behind an extensible LLM boundary — core subset |
| Tools and human interaction | Broad tool catalog, Question, and Approval flows | Generic native tool runtime plus Question and Approval core — core subset |
| Web experience | Full extensible Web application | Core conversation UI, history, streaming, Question, and API-key settings — core subset |
| Workspace | Workspace lifecycle and Web experience | Registry, API, and SQLite persistence; Web management is deferred — core subset |
| Settings, presets, and extensions | Full configuration and extension surfaces | Deferred beyond the compatibility paths required by the main flow |
| Coding and work integrations | Shell, filesystem, terminal, LSP, sandbox, MCP, ACP, jobs, and related capabilities | Deferred; they are not the product center |

“Core subset” means the end-to-end main flow is available while the wider DeepSeek Harness surface is not yet reproduced. See the [implementation progress](./zh-CN/08-implementation-progress.md) for the evidence-backed status and the [Chinese design index](./zh-CN/README.md) for detailed architecture.

The Chinese version is available in [README.zh-CN.md](./README.zh-CN.md).

DeepSeek Harness is MIT-licensed and copyright DeepSeek. Substantial copied or derived portions must preserve its license notice and attribution.
