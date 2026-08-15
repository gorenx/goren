# Repository Guidelines

## Project Direction and Source Authority

Goren's mainline is a Go reimplementation of the DeepSeek Harness Agent server architecture. The reference checkout is `../deepseek-harness`; the pinned compatibility baseline and scope are owned by `zh-CN/01-porting-scope-and-baseline.md`. Do not silently follow a newer source checkout.

Treat DeepSeek Harness's existing responsibility boundaries as the default implementation map. Preserve its Service Definition / Provider / Consumer ownership, event owners, lifecycle boundaries, and canonical capability names unless Go language constraints, a demonstrated dependency defect, or an explicit exclusion requires a change. Do not mechanically mirror every npm package or source file, but do not invent a new domain decomposition merely to make the Go layout look different. Document evidence and compatibility impact before deviating from a source boundary.

The primary compatibility target is the protocol between the existing TypeScript client and the Go Agent server. Preserve the Host half of `packages/client/connection`, the `host/apiproxy` wire contracts, HTTP `/api` carrier, WebSocket downlinks, request correlation, cancellation, trust fence, and included API schemas. The source path says `client`, but responsibility rather than directory name controls inclusion.

The inbound Connection Host uses Echo v5 (`github.com/labstack/echo/v5`) rather than a directly assembled `net/http` router/server. Keep Echo inside `internal/connection`; API Proxy and core packages must not depend on `echo.Context`. Do not use Echo's permissive binder for protocol envelopes: decode with the repository's strict codecs. Install a custom Echo error handler and recovery mapping so framework defaults never change the pinned HTTP status, JSON envelope, or headers. Access the underlying request/response only inside the WebSocket bridge and cancellation/transport middleware required by `coder/websocket`.

Exclude the Web UI, browser-side Connection implementation, React/client runtime packages, DeepSeek Harness `packages/sdk`, Python SDKs, generated client artifacts, and Web-only static hosting. Do not import their runtime code into Go. Typert is an internal dispatch mechanism for selected Remote endpoints, not the overall client protocol. Headless, ACP, MCP, and Typert-backed auxiliary endpoints are deferred unless the current task explicitly brings them into scope.

Configuration is owner-defined Go typed config. Do not implement or embed `!!js`, Goja, Node.js evaluation, Cordis expression interpolation, or another scripting substitute. CLI flags, environment variables, and optional config files must be decoded into named Go structs with strict unknown-field handling and explicit validation before a Plugin is created. Derived defaults and platform selection belong in explicit Go functions or the composition root.

SQLite adapters use sqlc-generated access over `database/sql`. Keep migrations and query files with the owning storage adapter, keep generated packages repository-private, and map generated rows into owner-defined types before returning through a capability interface. Do not hand-edit generated files or expose sqlc/driver types from Session or domain contracts. Pin and run the sqlc generation/check workflow when SQLite code is introduced.

JSONL, SQLite, and other storage adapters persist business data but do not own business decisions. Consumer-owned application or capability interfaces define the operations and transaction intent. Session/application services create events, assign semantic meaning, choose repair and projection behavior, and decide atomic use-case boundaries. Adapters only encode/decode, perform I/O or requested transactions, map storage records, enforce technical durability limits, and translate storage failures.

The existing `llm` package predates this direction. Reuse correct transport or adapter internals where they fit, but do not describe its current public API as DeepSeek Harness-compatible. Do not create a second parallel LLM runtime or preserve the old identity through compatibility wrappers; migrate the owning API coherently.

## Repository Structure and Documentation

- `README.md` contains English project background only.
- `README.zh-CN.md` contains Chinese project background only.
- `zh-CN/README.md` is the authoritative design index and ownership map.
- `zh-CN/NN-*.md` contains the ordered authoritative Harness port design.
- `zh-CN/memory-design/` is isolated historical design. Do not read or cite it for Harness work unless the task explicitly targets it.
- `zh-CN/assets/` stores images referenced by Chinese documentation.
- `llm/docs/zh-CN/` describes the pre-port implementation and is migration evidence, not the target API.

Every cohesive implemented capability package must add or update a colocated `README.zh-CN.md`. For now, do not add package-local English `README.md` files. The package README explains the package's responsibilities and non-responsibilities, operating model, upstream/downstream interactions, lifecycle, and failure/cancellation behavior, and uses Mermaid for interaction or process diagrams. It links to the owning `zh-CN/NN-*.md` documents for cross-package contracts and to `zh-CN/08-implementation-progress.md` for implementation evidence instead of creating a second source of truth. Add these READMEs only to real implemented packages, not placeholders, small ownerless helper directories, generated code, or test-only directories.

Write detailed design in Simplified Chinese. Preserve canonical TypeScript public names, event names, wire fields, configuration keys, and established domain terms. Update `zh-CN/README.md` whenever an authoritative design document changes responsibility or order. Link to the owning document instead of duplicating contracts. Keep unresolved choices explicit; do not turn proposals into accepted behavior silently.

Use `cmd/` for executable entry points, public root packages only for real extension contracts, and `internal/` for repository-private runtime, assembly, and adapters. Keep colocated `*_test.go` files and package-local `testdata/`. Do not create placeholder package trees.

Keep package behavior tests colocated with their owner. Put repository-wide architecture and policy checks in `tests/architecture/` so they remain outside production packages while still running under `go test ./...`.

## Compatibility and Plugin Rules

For included surfaces, compatibility means the same observable semantics: HTTP paths and status behavior, WebSocket paths and frame direction, RPC envelopes and correlation, canonical names, JSON field presence, discriminants, event ordering, error codes, cancellation, disposal, and replay. Source Cordis Profile syntax and dynamic configuration behavior are explicitly excluded. Go syntax does not need to imitate TypeScript decorators, declaration merging, or generated `.d.ts` files. Every compatibility claim must name a source symbol or fixture at the pinned commit and a Go test or implementation location.

Runtime plugins implement the repository's Go `Plugin` interface and are instantiated by explicitly registered, statically linked factories. Each Factory owns a named config type, strict decoding, validation, and construction; raw configuration may exist only at the inbound/catalog erasure boundary and must not reach Plugin business logic. Do not use the standard-library `plugin` package, unsafe symbol loading, or a second service locator. Runtime mount, replacement, and unload are allowed only for compiled factories. Every service, event listener, adapter, tool, and provider registration is an owned effect and must have an idempotent disposer. Failed startup rolls back acquired effects in reverse order.

Preserve the Service Definition / Service Provider / Consumer split. Consumers depend on capability interfaces, never concrete providers. Agent, Session, Tools, and LLM remain distinct owners. Model-visible input must be reconstructable from the append-only Session log. Do not change the Agent loop to add behavior that belongs on an existing plugin, event, tool, provider, or policy seam.

Use idiomatic Go objects and methods instead of translating the TypeScript implementation's functional composition style. A stateful capability, lifecycle owner, coordinator, registry, gateway, or service must be a named struct that owns its invariants and exposes behavior through methods. Use interfaces only at real consumer/provider boundaries. Function adapters and callbacks are appropriate only for naturally stateless strategies, event handlers, or composition-root seams; do not reproduce TypeScript closure chains, function factories, or object-literal services as the primary Go design.

## Build and Verification

This repository has a Go module. Use the standard commands unless repository wrappers replace them:

- `go mod tidy`
- `go fmt ./...`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `git diff --check`

Run focused tests while iterating, then the applicable full checks. Prefer table-driven tests when cases share behavior. Documentation-only changes require Markdown link validation and `git diff --check`; do not claim Go behavior was verified by a documentation check. Protocol work also requires TypeScript-to-Go golden fixtures or differential tests. Real-provider tests must self-skip without credentials and remain separate from keyless acceptance.

When one task changes code and documentation, commit them separately. Code-facing tests and dependency files belong with implementation; design documents and indexes belong in a documentation commit.

## Implementation Integrity and Naming

Implement end-to-end behavior and fix incorrect ownership or abstractions at the source. Do not leave feature-critical TODOs, empty implementations, silent fallbacks, or duplicate compatibility paths. Keep exported declarations documented and dependencies flowing from inbound adapter to application/runtime to capability interface, with providers wired only in the composition root.

Variables, parameters, receivers, and named returns must not reuse a function or type name, including capitalization-only variants such as `model Model` or `client Client`. Preserve established names by default. Before changing a public Go API, TypeScript-compatible name, wire field, event, configuration key, or persisted shape, document the evidence, compatibility impact, and migration, then update implementations, callers, fixtures, tests, and design together.

Names for types and fields must first identify what the object is. If a name concatenates associated objects, processing steps, or storage details, check whether the type mixes responsibilities and repair the boundary before lengthening the name. Correct private names only when the evidence and impact are local and complete; do not mix unrelated naming cleanup into a porting change.

Add comments when a type's responsibility, lifecycle, or boundary cannot be understood from its name and context. Do not mechanically comment every field or restate the code.

## Architecture Boundaries

Group code by source-aligned business capability or use case. Do not keep unrelated DTOs, services, adapters, and mappers in broad packages, and do not create ownerless `utils`, `common`, or `helpers` packages. A small cohesive concept may remain in a few files; avoid one-file-per-function or one-directory-per-type layouts.

Consumers own their minimal interfaces and anti-corruption mappings. Do not create a global model with optional fields or type branches. Domain and capability-definition packages must not depend on CLI, HTTP, external SDKs, database drivers, or delivery mechanisms. Connect outbound adapters only in the composition root.

When a feature touches an oversized or mixed-responsibility area, migrate along the feature's actual source responsibility while keeping each step compilable and testable. Do not let unrelated cleanup block delivery, and do not continue implementation on a confirmed incorrect abstraction or dependency direction.

## Security and Configuration

Never commit credentials. `.env` is ignored; document required variables with safe placeholders in `.env.example` or another tracked example. Configuration must store credential references rather than secret values where possible, and secrets must never appear in logs, Session events, errors, fixtures, config dumps, or telemetry. Subprocess, filesystem, and sandbox providers must enforce policy in the operation that performs the effect.
