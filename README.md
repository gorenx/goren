# Goren

A Go Agent server with persistent conversations, streaming model output, and tool execution in one executable with an embedded Web interface.

Goren draws on the implementation of DeepSeek Agent Harness and uses GoAkt directly for its Actor runtime. It brings Agent execution, conversation history, model access, and tool interactions into a Go service with explicit ownership and lifecycle boundaries.

## Core experience

The built-in conversation interface lets you configure a DeepSeek API key, create or select a Session, browse history, send prompts, and follow live Agent output. When a tool needs more information, you can answer structured questions and continue the active turn.

- **Persistent conversations.** Sessions retain conversation history and execution facts, with SQLite storage, search, and recovery support.
- **Interactive execution.** Streaming output, cancellation, structured questions, and approval handling support the conversation as it progresses.
- **Tool integration.** Native tools and MCP client connections bring external capabilities into Agent execution. MCP connections support stdio and Streamable HTTP.
- **A self-contained server.** The production React Web interface is embedded in the Go executable; running the server requires no Node.js runtime.

## Design direction

Goren keeps Agent, Session, Tools, and LLM as distinct owners. AgentLoop runs one Agent; Session owns the append-only record from which model-visible input can be reconstructed. GoAkt provides runtime identity, messaging, and lifecycle management.

Capabilities are composed explicitly through typed Go configuration and statically linked providers. Credentials have a separate owner from settings and conversation history. Storage adapters persist data through capability-owned contracts, while application owners decide business behavior.

This structure is intended to make Agent behavior easier to trace, extend, and test while keeping execution, persistence, and transport responsibilities clear.

## Relationship to DeepSeek Agent Harness

DeepSeek Agent Harness is a reference for selected implementation patterns and behaviors. Goren makes its own decisions about ownership, lifecycle, and public contracts; it does not promise complete behavioral or protocol compatibility.

Goren owns its HTTP `/api` carrier, WebSocket downlinks, request correlation, cancellation, and API schemas. Similar endpoint or event names do not establish compatibility with an existing Harness client.

The project focuses on the Go Agent server and its core conversation interface. The original Harness browser runtime, full Web product, and SDKs are outside this scope. Broader interfaces such as ACP and MCP Server are deferred. An MCP client connection does not make Goren an MCP Server.

Goren is under active development. A component's presence in the repository does not by itself mean it is connected to the default server or ready for production use.

## License

Goren is available under the [MIT License](./LICENSE). Copied or derived portions of DeepSeek Agent Harness retain the applicable license notices and attribution.
