# raglibrarian

`raglibrarian` is a Go-based RAG system for a private technical-book library.
The eventual product will ingest books, retrieve evidence, and return answers
with traceable book, chapter, page, and passage citations.

The repository implements Milestones 2 through 7, including PDF/EPUB Catalog
upload, event-driven extraction, structured chunking, live lifecycle status,
Retrieval-owned indexing/reindexing/deletion, Qdrant search, and optional
grounded answers. Free-tier LLM usage is intentionally bounded, so grounded
answers can take up to 5 minutes in free environments. It does not perform
OCR.

Ingestion uses a chapter-aware page-window chunking profile. The default
passage target is two pages, with a hard maximum of three pages, 120-token
overlap, and a 512-token embedding input cap. Chapter or part boundaries flush
the current passage; overlap from a previous chapter is never carried into the
next chapter. This is a fixed versioned cross-service profile; tuning requires
introducing and validating a new supported profile so Catalog, Ingestion, and
Retrieval agree on the same digest and limits.

Retrieval treats vector similarity as a candidate signal, not final relevance,
when the optional Retrieval LLM provider is configured. Each retrieved passage
must pass passage-level LLM relevance assessment before it is returned. Passages
the model identifies as unrelated, including meta responses such as "the user is
asking..." or "the passage does not contain...", are excluded and Retrieval
backfills from later candidates until the requested evidence limit is filled or
the candidate scan budget is exhausted. Book matches are grouping metadata
derived from accepted passages only; the system does not request whole-book or
document-level LLM summaries. Grounded answers synthesize from the top accepted
passage only and render a deterministic book/page citation plus a short
synopsis.

Current verification as of July 27, 2026: Ingestion, Retrieval, and Answer
service tests pass for the chapter-aware chunking profile, Retrieval relevance
filtering, and top-passage grounded answer synthesis.

## Architecture decision

The architecture is additive: deploy a service boundary before its capability
grows. New product features are added as a service or event consumer, rather
than first being placed in the public API and extracted later.

```text
client -- HTTPS/HTTP --> edge-api -- mTLS gRPC --> identity-service --> Postgres
                         |
                         +-- mTLS gRPC --> catalog-service (upload/list/get)
                         +-- mTLS gRPC --> answer-service --> retrieval-service
                                      |
                                      +-- RabbitMQ --> ingestion Lambda/worker
                                      |
                                      +-- RabbitMQ --> retrieval-service --> Qdrant
```

- **edge-api** owns public HTTP, request validation, token verification, and
  route composition. It owns no business database or evolving aggregate.
- **identity-service** owns credentials, users, roles, and its `identity`
  Postgres schema. It is the only service that signs access tokens.
- **catalog-service** owns book metadata, original PDF/EPUB objects, lifecycle
  commands/status, minimal tombstones, and its transactional publication outbox.
- **ingestion-service** owns processing jobs and encrypted derived chunk
  artifacts. Its worker and Lambda adapters invoke the same application.
- **retrieval-service** owns embedding, Qdrant collections, evidence/book
  projections, and synchronous search over indexed chunks.
- **answer-service** owns bounded prompt construction, provider integration,
  and validation of grounded answer segments against Retrieval evidence IDs.
  When a free model ignores or rejects JSON mode, the provider retries with a
  documented plain-text format that requires a model-authored `Citations:`
  preamble and a matching answer line; the service never fabricates citation
  IDs.
- Internal gRPC ports and Postgres are private in Compose. Service-to-service
  calls use TLS 1.3 with client certificates.
- Future lifecycle work is added in its owning bounded context. Bounded event
  work may run as Lambda or a portable worker without
  becoming another microservice. Contracts remain versioned and additive. See
  the local architecture decision record in `docs/`.

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
| Real query/retrieval | Implemented | `/query` is authenticated and defaults to bounded evidence-only semantic results from Retrieval. |
| Sessions, refresh tokens, revocation | Implemented | Refresh tokens rotate in an `HttpOnly`, `SameSite=Strict` cookie; logout/replay invalidates the server-side session family. |
| Abuse controls | Implemented | Bounded in-process trusted-client-aware limits protect registration, verification, setup, login, and refresh. |
| Catalog PDF/EPUB lifecycle | Release candidate | Role-gated streaming upload, idempotent delete/reindex, minimal tombstones, private MinIO persistence, durable publication, and cleanup reconciliation; live M7 acceptance remains required. |
| PDF/EPUB ingestion and live status | Release candidate | Event-driven worker/Lambda adapters, sandboxed bounded extraction, deterministic chunk artifacts, deletion cleanup, Catalog lifecycle projection, and authenticated SSE with polling reconciliation. |
| Vectors and retrieval | Implemented | Retrieval owns vector indexing, Qdrant collections, evidence projection, search policy, and replay-safe indexing. |
| LLM answer synthesis | Release candidate | Optional `answer` mode uses the additive stateless Answer service, validates citations against returned evidence, and degrades to evidence-only results; protected real-provider staging remains required. |

## Delivery roadmap

Milestones 2, 3, and 5 are complete in the current checkout. Milestones 4, 6,
and 7 are release candidates. They require the controlled local, private
workers-first host, and provider-neutral serverless acceptance sequence in
[the release completion runbook](docs/release-candidate-completion.md) before
they are marked complete. Milestone 4 adds
asynchronous PDF extraction and deterministic chunk manifests through one
application shared by worker and Lambda adapters. Catalog projects monotonic
processing state, while Edge gives authenticated clients low-latency SSE hints
backed by authoritative polling reconciliation. Processing and notification
queues are bounded; duplicate, out-of-order, poison, malformed, encrypted,
image-only, and timeout paths terminate with stable behavior. M4 accepts
text-bearing PDFs only. M7 adds bounded EPUB spine extraction while OCR remains
later work.

The canonical service-by-service roadmap, data ownership, Lambda/worker
deployment policy, contracts, and acceptance gates are in
[docs/README.md](docs/README.md). The product requirements are in
[docs/spec_rag_tech_books.md](docs/spec_rag_tech_books.md). UI routes for admin,
books, evidence search, and optional grounded answers are implemented. Milestone
6 remains a release candidate until its protected real-provider gate passes.
M7 lifecycle/EPUB code remains open until its live convergence gate passes;
Internet-ready hardening follows it.

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
| `POST` | `/query` | Bearer token | Validates the session, then returns bounded semantic evidence. `limit` is the final primary evidence budget after score, visibility, and configured LLM relevance exclusions. Optional `mode: "answer"` adds validated grounded answer segments or safely degrades to evidence only; omitted mode defaults to `search`. `/query/` remains compatible. |

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
services/edge-api/   Public HTTP boundary and query routing
services/identity-service/
				     Identity domain, gRPC adapter, Postgres repository, schema baseline
services/catalog-service/
                     Catalog upload, metadata, object storage, processing status, and outbox
services/ingestion-service/
                     PDF extraction, chunking, worker/Lambda adapters, and artifacts
services/retrieval-service/
                     Retrieval indexing/search, evidence projection, and Qdrant ownership
services/answer-service/
                     Grounded synthesis, provider adapter, and citation validation
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
make local-reset  # optional only if you are cleaning stale local state
make dev-secrets
make bootstrap-verifier
make dev-certs
make app-bootstrap
make app-test
```

`make app-bootstrap` is the standard bootstrap and start path for an existing checkout.
It now covers the complete local product stack rather than a milestone-specific slice.
`make app-test` runs the current full repository gate set.
`make local-run` remains the underlying bootstrap command for the Compose stack.
The stateful test targets reset the test-only PostgreSQL databases and Qdrant
collection before they run, so repeated local, e2e, and CI executions start
from an empty test baseline and never touch real data.
`stack-up` now starts the current full stack profile set (M4, M5, and M6-capable),
applies Identity migrations with the migration-only role, and brings up the same
runtime used for current development at loopback `:8080`.
A disposable Mailpit SMTP fixture is private to the backend network; its inspection
UI is loopback-only on `:8025`.
If you do not yet have the Hugging Face `hf` CLI available, use
`make local-run-stub` for a full-stack local boot that swaps in the CI-compatible
TEI/provider stubs and does not fetch model files.
To stop that stub run, use `make local-stop-stub`.
`make dev` is an alias for this workflow.
For a deliberate fresh baseline, run `make local-reset` before `make local-run`.
For Retrieval model bootstrapping, use `make app-bootstrap`; it will reuse or
repair the pinned host model cache as needed.
If `hf` is not installed locally, bootstrap will use a temporary Docker-based
download path (`HF_BOOTSTRAP_IMAGE`, default `python:3.12-slim`) and still produce
the same pinned local cache.
`make m5-search-quality-test` uses the deterministic TEI-compatible stub so it
is reliable without a local model cache. To validate the pinned real model,
run `make m5-search-quality-test-real` after configuring Docker with at least
8 GiB of memory.
For optional grounded answers, configure the file-backed provider endpoint,
model, CA, and key documented in `.env.example`, then run `make m6-stack-up`.
The deterministic `make m6-answer-quality-test` and `make m6-contract-test`
targets do not require a real provider; `make m6-answer-quality-test-real` is a
protected staging gate and requires an existing authenticated fixture stack
plus reader and librarian token files. It recreates only `answer-service` with
the supplied HTTPS provider URL, model, and file-backed key, verifies that
configuration, and requires a grounded response from that provider. The
provider path first asks for JSON and then falls back to a plain-text response
that must include a model-authored `Citations:` preamble plus the answer body;
it never invents citation IDs from the search context. The recreated service
remains configured for the real provider after the gate.
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
make proto-breaking # reject protobuf changes that break main
make dev-secrets-test # fresh and additive local-secret upgrade regressions
make m4-worker-recovery-test # controlled local worker-down recovery contract
make contract-test # live mTLS, database, and broker-recovery contracts
make minio-runtime-test # live object-storage cleanup and pagination contracts
make m6-contract-test # live Answer/Edge/Retrieval mTLS authorization contracts
make m6-answer-quality-test # deterministic grounded-answer safety fixtures
make m6-e2e     # authenticated answer, degradation, and citation black-box tests
make ui-check    # UI install, lint, type-check, and production build
make security-check # secret, Dockerfile, and service-image scans
make full-gates  # complete local static, test, UI, and security gate
```

The workspace declares Go 1.26.5 as its minimum toolchain. CI and service
images use the same patched release; update all three together when raising
the minimum.

See [CONTRIBUTING.md](CONTRIBUTING.md) for workspace and module rules.
