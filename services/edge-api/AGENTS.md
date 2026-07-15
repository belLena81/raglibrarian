# Edge API Agent Guide

This directory is raglibrarian's public HTTP perimeter and routing process.
Treat the repository-root `AGENTS.md` as the role and
security policy, the root `README.md` as the product brief, and
`CONTRIBUTING.md` as the workspace workflow reference.

## Mission

Expose stable, bounded HTTP contracts and route verified actors to the service
that owns each capability. Keep Edge usable after every milestone without
turning it into an Identity, Catalog, Ingestion, Retrieval, or Answer service.

## Ownership and boundaries

- `cmd/`, `server.go`, `handler/`, and `middleware/` own HTTP transport,
  request validation, authentication, and error mapping.
- Edge-owned consumer interfaces sit beside the handler/application code that
  needs them; gRPC or Lambda clients are outward adapters.
- Do not add identity, catalog, ingestion, retrieval, answer, prompt, or storage
  business rules here. Edge maps public DTOs and routes calls only.
- Do not import packages from another service. Depend on generated versioned
  contracts and translate at the boundary.

## Engineering rules

- Preserve the existing Go workspace: each module has its own `go.mod`; add
  dependencies from the module that uses them, then run `go work sync` at the
  repository root.
- Run repository-wide targets from the repository root. Use focused `go test
  ./...` in this module while iterating, then `make test`; use `make e2e` for
  changed HTTP contracts when infrastructure is available.
- Prefer contract-first changes. Define domain/request/response types and tests
  before wiring an external service.
- Never log tokens, passwords, raw book contents, or secret configuration.
- Return citations only for retrieved evidence. Do not make the LLM invent a
  source, page number, chapter, or quotation.
- Keep handlers thin and preserve the existing error-response conventions.

## Current implementation state

- Edge, Identity, and Catalog are independent processes connected by mTLS.
- Identity signs PASETO access tokens and owns rotating server-side sessions;
  Edge verifies tokens and validates the live session for protected requests.
- `/query` and `/query/` are authenticated and return truthful `501` until the
  Retrieval milestone supplies real evidence.
- The next milestone adds Identity-owned admin bootstrap and librarian approval;
  Edge exposes and maps those contracts without owning their rules.

## Multi-agent coordination

The lead agent owns the task plan, public interfaces, integration, and the
final verification. Fan out only for independent, read-mostly investigation or
for disjoint file ownership. Do not run two implementation agents against the
same package, protocol, migration, generated artifact, `go.mod`, or `go.work`.

See `AGENTIC_DEVELOPMENT.md` for the project brief, recommended skills, and
the exact fan-out decision rules.
