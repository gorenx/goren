# Repository Guidelines

## Repository State and Structure

This repository is currently a design-first Go project. It has no `go.mod`, Go packages, tests, `Makefile`, or task runner, so do not describe prospective commands as verified checks.

Keep documentation responsibilities explicit:

- `README.md` contains the English project background only.
- `README.zh-CN.md` contains the Chinese project background only.
- `zh-CN/README.md` is the detailed-design index and ownership map.
- `zh-CN/NN-*.md` contains the ordered, authoritative Chinese design, split by responsibility rather than arbitrary document size.
- `zh-CN/notes/` preserves historical inputs; notes are not the current source of truth.
- `zh-CN/assets/` stores images referenced by Chinese documentation.

When implementation begins, add packages only for real responsibilities. Prefer `cmd/` for executable entry points, `internal/` for repository-private code, colocated `*_test.go` files, and package-local `testdata/` fixtures. Do not create empty placeholder trees.

## Documentation Changes

Write detailed design in Simplified Chinese and prefix design filenames and titles with a two-digit reading-order number. Keep public names, wire fields, Go identifiers, and established domain terms in their canonical form. Update `zh-CN/README.md` whenever a design document is added, removed, renamed, reordered, or changes responsibility. Link to the owning document instead of duplicating contracts or decisions across files. Preserve unresolved choices as explicit open decisions; do not silently turn proposals into accepted behavior.

## Build, Test, and Development Commands

After the Go module is initialized, use standard Go commands unless repository wrappers replace them:

- `go mod tidy` synchronizes module requirements and checksums.
- `go build ./...` compiles every package.
- `go test ./...` runs the complete test suite.
- `go test -race ./...` checks concurrent code for data races.
- `go vet ./...` reports suspicious Go constructs.
- `go fmt ./...` formats Go source files.

For documentation-only changes, verify Markdown links and run `git diff --check`. Report Go commands as not applicable until a module exists.

## Code, Tests, and Reviews

Use idiomatic Go formatting and naming. Keep package names short and lowercase, exported declarations documented, and ownership boundaries visible in dependencies. Prefer table-driven tests where several cases share behavior, and run the narrowest relevant test during iteration before the full suite. Keep commits focused; pull requests should state the responsibility changed, design implications, checks run, and unresolved decisions.

## Security and Configuration

Never commit credentials. `.env` is ignored; document required variables with safe placeholders in a tracked file such as `.env.example`.

## Implementation Integrity

Prioritize correctness and an end-to-end working feature. Fix root causes instead of layering patches over an incorrect abstraction, data model, transaction boundary, or dependency direction. Do not substitute temporary behavior for the requested implementation or leave feature-critical logic as TODOs, empty implementations, or unverified follow-up work.

Add comments when a type's responsibility, lifecycle, or boundary cannot be understood accurately from its name and context. Do not mechanically comment every struct or field, and do not restate the fields a type contains.

## Naming and Refactoring

Names for types and fields must first answer what the object is. If a name has to concatenate associated objects, processing steps, or storage details, check whether the type has multiple reasons to change; split distinct responsibilities instead of hiding them in a longer name.

Variable names, including local variables, parameters, receiver names, and named return values, must not reuse the name of a function or type such as a struct, interface, or type alias. A capitalization-only difference does not make the names distinct: avoid declarations such as `model Model` or `client Client`; choose a name that states the variable's role instead.

Preserve established names by default. Do not rename code incidentally for brevity, uniformity, or personal preference. Treat a name as a design defect when it misstates domain ownership, responsibility, lifecycle, or invariants and has caused incorrect dependencies, duplicate interfaces, misuse, or recurring adapter patches.

Before renaming, classify the issue:

- If the object and responsibility are correct but the name is wrong, rename it.
- If the object mixes responsibilities, repair the boundary first.
- If two interfaces have different responsibilities, keep distinct names.
- If two interfaces duplicate one responsibility, remove the duplicate entry point.

Private names touched by the current feature may be corrected when the evidence and impact are local and complete. Before changing a public contract, cross-module call, external API, configuration key, serialized field, persistence shape, or stable documentation term, explain the evidence, impact, and migration. Update production code, callers, tests, and design documentation together; do not mix unrelated naming cleanup into the change.

## Architecture Boundaries

Do not keep unrelated DTOs, helpers, mappers, services, and adapters in a broad package. Group code by business capability or use case, and introduce a subpackage only when an independent boundary emerges. A small cohesive concept may remain in a few files; avoid one-file-per-function or one-directory-per-type layouts.

Place cross-domain mapping at the consumer-owned anti-corruption boundary and expose only the consumer's minimal DTO or stable identifiers. Do not create a global model with optional fields or type branches, and do not place mappings in ownerless `utils`, `common`, or `helpers` packages.

Consumers own their interfaces. Keep dependencies flowing from inbound adapter to application or use case to domain, and connect outbound adapters in the composition root. Domain packages must not depend on CLI, HTTP, external SDKs, database drivers, or delivery mechanisms.

When a feature touches an oversized file, flat package, or mixed responsibility, migrate along the feature's actual boundary while keeping each step compilable and testable. Do not let unrelated cleanup block delivery, and do not continue implementation on a confirmed incorrect abstraction or dependency direction.
