# Goren

**A Go version of [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness).**

Goren begins by reproducing the core responsibilities, protocol, and Agent flow of DeepSeek Harness in idiomatic Go. DeepSeek Harness is the starting point, not the destination: Goren is not a harness for code and not a harness for work, but a harness around the person. Its direction is to help a person retain continuity, choice, and control across tools, tasks, and contexts.

## Current feature comparison

Status: **Available** means the current Goren implementation provides the named behavior; **Partial** names the implemented subset; **Deferred** means the capability is not in the current implementation scope; **Replaced** means Goren deliberately uses a Go-native design instead of copying the TypeScript mechanism.

### Runtime and protocol

| Capability | DeepSeek Harness | Goren today |
| --- | --- | --- |
| Language and runtime | TypeScript on Node.js | **Available:** Go |
| Plugin composition | Cordis scopes, services, events, and runtime plugins | **Available:** Go interfaces, stateful services, event scopes, and statically linked factories |
| Host transport | HTTP RPC plus Mux and Host WebSocket streams | **Available:** compatible `/api`, `/api/respond`, Mux, and Host carriers |
| RPC protocol | Typed client/server requests, responses, receipts, errors, and cancellation | **Available:** included envelopes and error semantics are contract-verified against the pinned TypeScript source |
| API discovery and dispatch | `host.describe` and plugin-contributed methods | **Available:** typed catalog, decoding, dispatch, and stable business/technical failure separation |
| Live frames | Session, interaction, workspace, status, error, and remote frame unions | **Partial:** included session, interaction, workspace, status, and error frames; remote execution is deferred |
| Plugin events | Decision, notification, and waterfall-style extension points | **Available:** typed Go handler interfaces and scoped publication |
| Type generation | Typert schemas, generated clients, and Host Gateway types | **Replaced:** protocol-compatible Go types and pinned cross-language contract fixtures; Typert generation is not copied |
| Dynamic configuration | Schemastery and JavaScript-capable configuration paths | **Replaced:** typed Go configuration; `!!js` evaluation is not supported |

### Agent, LLM, and tools

| Capability | DeepSeek Harness | Goren today |
| --- | --- | --- |
| Agent registry and lifecycle | Agent scopes, status, disposal, and session ownership | **Available:** Agent registry, lifecycle, status, and Session binding |
| Inbox | Follow-up, steer, inject, claim, and discard flow | **Available:** typed inbox targets, ordering, claim, and discard lifecycle |
| Agent loop | Steps, requests, streaming, tool execution, continuation, stopping, and errors | **Available:** end-to-end loop from prompt through `turn/end` |
| LLM runtime | Pluggable adapters, typed content, streaming chunks, and usage | **Available:** extensible adapter boundary and typed streaming contract |
| DeepSeek provider | Official DeepSeek routing and models | **Available:** official `https://api.deepseek.com` route with credential resolution |
| LLM catalog and selection | Provider/model discovery and per-session model selection | **Available:** provider/model APIs, session model list, and selection |
| Retry | Retry classification, delay, attempt events, and cancellation | **Available:** retry policy and Agent request-attempt integration |
| System prompt | Ordered sections, tool descriptions, assembly events, and change notification | **Available:** registry, ordering, rendering, assembly, and invalidation |
| Native tool runtime | Tool registry, schema validation, execution events, result validation, and cancellation | **Available:** generic native tool pipeline |
| Built-in tool catalog | Filesystem, shell, LSP, terminal, Web, goals, jobs, skills, subagents, and more | **Partial:** `ask_user_question` is the current built-in product tool; the broader catalog is deferred |
| User questions | Structured single/multiple-choice and custom answers | **Available:** service, Host protocol, Web card, response, and same-turn continuation |
| Approval | Policy, request, answerer, audit events, and Host interaction | **Partial:** service and Host protocol are available; the Web approval UI is deferred |

### Sessions, data, and product surfaces

| Capability | DeepSeek Harness | Goren today |
| --- | --- | --- |
| Session lifecycle | Create, list, dispose, events, flush, and resume | **Available:** main lifecycle, durable facts, flush boundary, cold recovery, and resume |
| Conversation | History, prompt, streamed reasoning/output, and tool messages | **Available:** HTTP/WebSocket main flow and Web rendering |
| Queue and cancellation | Read/edit/remove queued input and cancel an active turn | **Available:** queue baseline and mutation plus turn cancellation |
| Session projection | Extensible projections, cache, checkpoint, and live frames | **Available:** projection registry, fold, checkpoint/restore, baseline, and live frames |
| Session title | Fallback, manual rename, and automatic LLM title providers | **Partial:** stable fallback and manual rename; first/all-prompt LLM title providers are deferred |
| Session persistence | Pluggable persistence with JSONL and SQLite adapters | **Partial:** persistence boundary plus SQLite/sqlc adapter; JSONL adapter is not copied |
| Session query and export | SQLite query, search, log export, and query tool | **Deferred** |
| Session fork | Fork an existing conversation | **Deferred** |
| Workspace | Registry, ordering, archive, Session accounting, persistence, and Web management | **Partial:** registry, API, accounting, ordering/archive, and SQLite are available; Web management is deferred |
| Credentials | Provider/manager/store, environment values, local storage, and Host API | **Available:** environment precedence, owner-only local JSON store, write-only Host API, and Web DeepSeek API-key settings |
| Settings | Typed namespaces, file persistence, description, and mutation | **Deferred:** only the canonical absent-provider compatibility response is implemented |
| Agent presets and persona | Discovery, composition, selection, authoring, and persona prompt | **Deferred:** only the canonical empty-roster compatibility response is implemented |
| Web application | Extensible browser runtime and complete product UI | **Partial:** repository-owned React/Vite/Tailwind conversation UI with Sessions, history, streaming, Question, and credential settings |
| Attachments | Attachment storage, references, upload, and UI | **Deferred:** stable image-reference metadata exists only where consumed by LLM content |

### Extended capabilities

| Capability | DeepSeek Harness | Goren today |
| --- | --- | --- |
| Filesystem and editing | Local/sandbox filesystem, search, and string-replace tools | **Deferred** |
| Shell, subprocess, and terminal | Persistent Bash/PowerShell, subprocesses, and terminal sessions | **Deferred** |
| LSP | LSP lifecycle, stdio adapter, and tool integration | **Deferred** |
| Sandbox, guard, and permission presets | Local policies, Windows ACL/Landlock paths, timeout/repetition guards, and permission presets | **Deferred** |
| Web search and fetch | DeepSeek, Exa, and Perplexity search plus HTTP fetch tools | **Deferred** |
| MCP and ACP | MCP client bridge and ACP agent adapter | **Deferred** |
| Jobs, workflows, and subagents | Local jobs, worker workflows, in-process/external subagents, and control tools | **Deferred** |
| Goals, plans, TODOs, and skills | Goal driver, plan mode, TODO tool, and filesystem skills | **Deferred** |
| Context compaction | Basic compaction, tool-result pruning, checkpoints, and compact command | **Deferred** |
| Hooks and extensions | Codex/Claude hooks and Cordis host/client/tool/UI extensions | **Deferred** |
| Headless and SDKs | Headless bundle plus TypeScript and Python SDK surfaces | **Deferred** |
| Code runtime and E2B | Worker-thread code execution and E2B filesystem/subprocess adapters | **Deferred;** code execution is not the product center |

The matrix is a readable snapshot, not a second progress tracker. See the [implementation progress](./zh-CN/08-implementation-progress.md) for evidence levels and remaining gates, and the [Chinese design index](./zh-CN/README.md) for ownership and architecture.

The Chinese version is available in [README.zh-CN.md](./README.zh-CN.md).

DeepSeek Harness is MIT-licensed and copyright DeepSeek. Substantial copied or derived portions must preserve its license notice and attribution.
