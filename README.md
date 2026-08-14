# Goren

Goren is a Go reimplementation of the Agent server architecture of [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness). Its primary compatibility target is the protocol between the existing TypeScript client and the Go Agent server: RPC envelopes, HTTP and WebSocket carriers, API method contracts, events, cancellation, and errors. Plugin composition uses Go interfaces and statically linked factories.

The project does not port the Web UI, browser client implementation, DeepSeek Harness SDK, or Python SDK. It does implement the server half of the TypeScript client Connection protocol so a compatible client can connect to the Go Agent server. Headless, ACP, MCP, and Typert-backed auxiliary endpoints are deferred unless a later scope decision includes them.

The current `llm` package predates this project direction. It is implementation material for the migration, not evidence that the DeepSeek Harness API is already compatible. Compatibility is measured against the pinned TypeScript source baseline and cross-language contract fixtures.

The Chinese project background is available in [README.zh-CN.md](./README.zh-CN.md). The authoritative design starts at the [Chinese design index](./zh-CN/README.md).

DeepSeek Harness is MIT-licensed and copyright DeepSeek. Substantial copied or derived portions must preserve its license notice and attribution.
