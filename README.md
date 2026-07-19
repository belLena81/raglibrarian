# raglibrarian

`raglibrarian` is a Go-based RAG system for a private technical-book library.
The eventual product will ingest books, retrieve evidence, and return answers
with traceable book, chapter, page, and passage citations.

The repository implements Milestones 2 through 4, including Catalog upload,
event-driven text-PDF extraction, structured chunking, and live processing
status. It does not perform OCR, extract EPUB files, query Qdrant, call an LLM,
or return real retrieval results.

## Architecture decision

The architecture is additive: deploy a service boundary before its capability
grows. New product features are added as a service or event consumer, rather
than first being placed in the public API and extracted later.

```text
client -- HTTPS/HTTP --> edge-api -- mTLS gRPC --> identity-service --> Postgres
                         |
                         +-- mTLS gRPC --> catalog-service (upload/list/get)
                                      |
                                      +-- RabbitMQ --> ingestion Lambda/worker
```

- **edge-api** owns public HTTP, request validation, token verification, and
  route composition. It owns no business database or evolving aggregate.
- **identity-service** owns credentials, users, roles, and its `identity`
  Postgres schema. It is the only service that signs access tokens.
- **catalog-service** owns book metadata, original PDF objects, processing
  status, and its transactional publication outbox.
- **ingestion-service** owns processing jobs and encrypted derived chunk
  artifacts. Its worker and Lambda adapters invoke the same application.
- Internal gRPC ports and Postgres are private in Compose. Service-to-service
  calls use TLS 1.3 with client certificates.
- Future indexing, retrieval, and answer generation are added in
  their owning bounded contexts. Bounded event work may run as Lambda or a
  portable worker without becoming another microservice. Contracts remain
  versioned and additive. See the local architecture decision record in `docs/`.

## Agentic workflow and quality standards

Agents follow the repository roles and handoffs in [AGENTS.md](AGENTS.md).
Keep the UI lightweight: it owns presentation, form state, and simple client
validation only. Go services make all business, authorization, lifecycle, and
security decisions; the UI treats backend responses as authoritative.

We keep changes simple and focused. Each package and service has one clear
responsibility, dependencies point inward, and bounded contexts communicate
through explicit versioned contracts. Prefer low coupling, small additive
changes, and idiomatic readable Go over abstractions added for hypothetical
future needs.

Development follows clean architecture, DDD, and TDD: domain/application code
does not depend on transport or infrastructure, persistence stays with its
owning service, and work proceeds red-green-refactor with focused automated
tests. A change is ready only after its applicable formatting, lint, vet,
race, contract, integration, and security checks pass.

## Current implementation state

| Capability | State | Notes |
|---|---|---|
| Edge, Identity, Catalog, Ingestion processes | Implemented | Compose migrates owning schemas, then starts the long-running services. |
| Public auth API | Implemented | Privacy-preserving registration, email verification/resend, login, refresh, `/me`, and server-side logout. |
| Access tokens | Implemented | PASETO v4 public, Ed25519 signed by Identity and verified by Edge; 15-minute lifetime and `edge-api` audience. |
| Password storage | Implemented | bcrypt at cost 12; plaintext is never persisted. |
| Identity persistence | Implemented | One greenfield Identity schema baseline, least-privilege database roles, and Postgres adapters. |
| HTTP hardening | Implemented | Strict, bounded JSON, request/header timeouts, security headers, sanitized errors, and request IDs. |
| Real query/retrieval | Not implemented | `/query` is authenticated and returns `501`; it never fabricates citations. |
| Sessions, refresh tokens, revocation | Implemented | Refresh tokens rotate in an `HttpOnly`, `SameSite=Strict` cookie; logout/replay invalidates the server-side session family. |
| Abuse controls | Implemented | Bounded in-process trusted-client-aware limits protect registration, verification, setup, login, and refresh. |
| Catalog PDF upload/list/get | Implemented | Role-gated streaming upload, deterministic pagination, private MinIO persistence, durable publication, reconciliation, and fixed-label metrics. |
| PDF ingestion and live status | Implemented | Event-driven, idempotent worker/Lambda adapters, sandboxed streamed extraction, deterministic chunk artifacts, Catalog status projection, and authenticated SSE with polling reconciliation. |
| Vectors, retrieval, LLM | Not implemented | Future additive services. |

## Delivery roadmap

Milestones 2 through 4 are complete. Milestone 4 adds asynchronous PDF
extraction and deterministic chunk manifests through one application shared by
worker and Lambda adapters. Catalog projects monotonic processing state, while
Edge gives authenticated clients low-latency SSE hints backed by authoritative
polling reconciliation. Processing and notification queues are bounded;
duplicate, out-of-order, poison, malformed, encrypted, image-only, and timeout
paths terminate with stable behavior. M4 accepts text-bearing PDFs only; OCR
and EPUB remain later work.

The canonical service-by-service roadmap, data ownership, Lambda/worker
deployment policy, contracts, and acceptance gates are in
[docs/README.md](docs/README.md). The product requirements are in
[docs/spec_rag_tech_books.md](docs/spec_rag_tech_books.md). UI routes for admin,
books, and real query results are staged contracts until their owning milestone
is marked delivered.

## Security model

Local development generates the key pair into owner-readable files with
`make dev-secrets`:

- `IDENTITY_SIGNING_KEY`: a private Ed25519 key. Configure it only in
  `identity-service`.
- `EDGE_VERIFY_KEY`: the corresponding public key. Configure it only in
  `edge-api`.

Never commit either value, local certificates, connection strings, tokens, or
book content. The signing key is mounted only into Identity; Edge receives only
the public verification key. Identity database, bootstrap, email-protection,
and SMTP credentials are also delivered as files rather than environment
values. See [OPERATIONS.md](OPERATIONS.md) for rotation and migration guidance.

## Public API

| Method | Path | Authentication | Current behaviour |
|---|---|---|---|
| `GET` | `/healthz` | None | Edge process health. |
| `GET` | `/readyz` | None | Edge readiness; returns `503` until Identity gRPC health is serving. |
| `POST` | `/auth/register` | None | Accepts a bounded reader or librarian registration and returns the same generic response for privacy. |
| `POST` | `/auth/verify-email` | None | Consumes a single-use verification token and creates the account. |
| `POST` | `/auth/verification/resend` | None | Requests a bounded, privacy-preserving verification resend. |
| `POST` | `/auth/login` | None | Validates credentials, returns a short-lived access token, and sets a refresh cookie. |
| `POST` | `/auth/refresh` | Refresh cookie | Rotates the refresh token and returns a replacement access token. |
| `GET` | `/auth/me` | Bearer token | Returns the authoritative current principal after validating the live Identity session. |
| `POST` | `/auth/logout` | Bearer token | Revokes the Identity session and clears the refresh cookie. |
| `POST` | `/query` | Bearer token | Validates the session, then returns `501` until retrieval exists. `/query/` remains compatible. |

Request JSON is strict. Client-supplied `role` and `user_id` fields are
rejected; identity comes only from verified token claims.

Refresh credentials are browser-only and never appear in JSON. The cookie is
`Secure` by default; set `EDGE_INSECURE_REFRESH_COOKIE=true` only while using
plain HTTP for local development. This is not a production setting.

## Catalog object-storage operation

Catalog connects to MinIO with HTTPS by default. Set
`CATALOG_MINIO_ENDPOINT` to `host[:port]` only; schemes, paths, credentials,
queries, and fragments are rejected. `CATALOG_MINIO_INSECURE` accepts only
`true` or `false` and defaults to `false`.

For a private deployment CA, set `CATALOG_MINIO_CA_FILE` to a read-only PEM
bundle containing CA certificates. It becomes Catalog's exclusive trust root;
normal hostname validation and TLS 1.2+ remain required. Do not set a CA file
with insecure mode. The Compose `true` setting is an isolated local-development
exception only; production deployments must use HTTPS and either system roots
or a mounted private CA.

## Repository layout

```text
pkg/                 Focused auth/TLS/gRPC/process libraries and protobuf clients
services/edge-api/   Public HTTP boundary and query stub
services/identity-service/
				     Identity domain, gRPC adapter, Postgres repository, schema baseline
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
make dev-secrets
make bootstrap-verifier
make dev-certs
make stack-up
make e2e
```

`make stack-up` starts the full Compose stack on loopback `:8080`, applies
Identity migrations with the migration-only role, and then starts Identity
with its bounded runtime role. A disposable Mailpit SMTP fixture is private to
the backend network; its inspection UI is loopback-only on `:8025`. `make dev`
is an alias for this workflow.
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
make contract-test # live mTLS, database, and broker-recovery contracts
make minio-runtime-test # live object-storage cleanup and pagination contracts
make ui-check    # UI install, lint, type-check, and production build
make security-check # secret, Dockerfile, and service-image scans
make full-gates  # complete local static, test, UI, and security gate
```

The workspace declares Go 1.26.5 as its minimum toolchain. CI and service
images use the same patched release; update all three together when raising
the minimum.

See [CONTRIBUTING.md](CONTRIBUTING.md) for workspace and module rules.
