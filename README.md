# raglibrarian

`raglibrarian` is a Go-based RAG system for a private technical-book library.
The eventual product will ingest books, retrieve evidence, and return answers
with traceable book, chapter, page, and passage citations.

The repository is currently at **Milestone 1**: a secure, runnable service
foundation. It does not yet ingest files, query Qdrant, call an LLM, or return
real retrieval results.

## Architecture decision

The architecture is additive: deploy a service boundary before its capability
grows. New product features are added as a service or event consumer, rather
than first being placed in the public API and extracted later.

```text
client -- HTTPS/HTTP --> edge-api -- mTLS gRPC --> identity-service --> Postgres
                         |
                         +-- mTLS gRPC --> catalog-service (health + Check scaffold)
```

- **edge-api** owns public HTTP, request validation, token verification, and
  route composition. It owns no business database or evolving aggregate.
- **identity-service** owns credentials, users, roles, and its `identity`
  Postgres schema. It is the only service that signs access tokens.
- **catalog-service** is an independently deployable mTLS gRPC boundary. It
  exposes standard health and `Catalog.Check`; book metadata is its future responsibility.
- Internal gRPC ports and Postgres are private in Compose. Service-to-service
  calls use TLS 1.3 with client certificates.
- Future ingestion, indexing, retrieval, and answer generation are added in
  their owning bounded contexts. Bounded event work may run as Lambda or a
  portable worker without becoming another microservice. Contracts remain
  versioned and additive. See the local architecture decision record in `docs/`.

## Current implementation state

| Capability | State | Notes |
|---|---|---|
| Edge, Identity, Catalog processes | Implemented | Compose migrates Identity, then starts all three services. |
| Public auth API | Implemented | Register, login, refresh, `/me`, and server-side logout. Public registration creates readers only. |
| Access tokens | Implemented | PASETO v4 public, Ed25519 signed by Identity and verified by Edge; 15-minute lifetime and `edge-api` audience. |
| Password storage | Implemented | bcrypt at cost 12; plaintext is never persisted. |
| Identity persistence | Implemented | Identity-owned users migration and Postgres repository. |
| HTTP hardening | Implemented | Strict, bounded JSON, request/header timeouts, security headers, sanitized errors, and request IDs. |
| Real query/retrieval | Not implemented | `/query` is authenticated and returns `501`; it never fabricates citations. |
| Sessions, refresh tokens, revocation | Implemented | Refresh tokens rotate in an `HttpOnly`, `SameSite=Strict` cookie; logout/replay invalidates the server-side session family. |
| Rate limiting / Redis | Not implemented | Required before an Internet-facing deployment. |
| File upload, ingestion, vectors, LLM | Not implemented | Future additive services. |

## Delivery roadmap

Milestone 1 is complete. The next deliverable is **Milestone 2: Identity RBAC
and approval**—secure singleton-admin bootstrap, pending librarian registration,
and admin approval/rejection. Book upload follows only after its role dependency
is independently usable.

The canonical service-by-service roadmap, data ownership, Lambda/worker
deployment policy, contracts, and acceptance gates are in
[docs/README.md](docs/README.md). The product requirements are in
[docs/spec_rag_tech_books.md](docs/spec_rag_tech_books.md). UI routes for admin,
books, and real query results are staged contracts until their owning milestone
is marked delivered.

## Security model

Run `make keygen` to produce two values:

- `IDENTITY_SIGNING_KEY`: a private Ed25519 key. Configure it only in
  `identity-service`.
- `EDGE_VERIFY_KEY`: the corresponding public key. Configure it only in
  `edge-api`.

Never commit either value, local certificates, connection strings, tokens, or
book content. The signing key must never be present in Edge configuration.

## Public API

| Method | Path | Authentication | Current behaviour |
|---|---|---|---|
| `GET` | `/healthz` | None | Edge process health. |
| `GET` | `/readyz` | None | Edge readiness; returns `503` until Identity gRPC health is serving. |
| `POST` | `/auth/register` | None | Creates a `reader`, returns a short-lived access token, and sets a refresh cookie. |
| `POST` | `/auth/login` | None | Validates credentials, returns a short-lived access token, and sets a refresh cookie. |
| `POST` | `/auth/refresh` | Refresh cookie | Rotates the refresh token and returns a replacement access token. |
| `GET` | `/auth/me` | Bearer token | Returns verified claims after validating the Identity session. |
| `POST` | `/auth/logout` | Bearer token | Revokes the Identity session and clears the refresh cookie. |
| `POST` | `/query` | Bearer token | Validates the session, then returns `501` until retrieval exists. `/query/` remains compatible. |

Request JSON is strict. Client-supplied `role` and `user_id` fields are
rejected; identity comes only from verified token claims.

Refresh credentials are browser-only and never appear in JSON. The cookie is
`Secure` by default; set `EDGE_INSECURE_REFRESH_COOKIE=true` only while using
plain HTTP for local development. This is not a production setting.

## Repository layout

```text
pkg/                 Focused auth/TLS/gRPC/process libraries and protobuf clients
services/edge-api/   Public HTTP boundary and query stub
services/identity-service/
                     Identity domain, gRPC adapter, Postgres repository, migration
services/catalog-service/
                     Independent catalog gRPC scaffold
tools/healthcheck/   Operational HTTP/gRPC probe binary
api/proto/           Versioned gRPC source contracts
tests/e2e/           Black-box HTTP tests
```

`go.work` defines the Go workspace; there is intentionally no root `go.mod`.

## Local development

Prerequisites: Go 1.26.5+, Docker Compose, `psql`, OpenSSL, `protoc`, and the
Go protobuf generators for contract generation. Install the generators once:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
```

Generated Go bindings under `pkg/proto/` are intentionally not committed.
Every Go build/test target generates them automatically; run `make proto` to
lint and generate them explicitly after changing a `.proto` contract.

```bash
cp .env.example .env
make keygen >> .env
make dev-certs
make stack-up
make e2e
```

`make stack-up` starts the full Compose stack on `:8080` and applies Identity
migrations before starting Identity. `make dev` is an alias for this workflow.
Identity and Catalog expose standard gRPC health services inside the private
Compose network. `make contract-test` verifies both services over mTLS.

Development certificate sources remain host-only with mode `0600`. Compose
mounts only the CA certificate and each service's own certificate/key. Service
processes load those assigned files and drop to the distroless non-root account
before accepting traffic; the CA private key and peer private keys are never
mounted into a service container.

## Quality commands

Run commands from the repository root:

```bash
make test        # unit tests
make test-race   # race-detector tests
make fmt-check   # fail when Go formatting differs
make vet         # per-module go vet
make lint        # golangci-lint per module
make vuln        # govulncheck per module
make proto-check # Buf contract lint
make contract-test # live Identity/Catalog mTLS and database contracts
```

The workspace declares Go 1.26.5 as its minimum toolchain. CI and service
images use the same patched release; update all three together when raising
the minimum.

See [CONTRIBUTING.md](CONTRIBUTING.md) for workspace and module rules.
