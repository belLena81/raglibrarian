# raglibrarian delivery roadmap

This is the canonical delivery roadmap for raglibrarian. The product
requirements live in [spec_rag_tech_books.md](spec_rag_tech_books.md), and the
binding service-boundary decision lives in
[architecture-decision-record.md](architecture-decision-record.md). Historical
plans are not active implementation guidance.

The roadmap uses vertical slices: every milestone ends with a demonstrable
user or operator outcome, deployable service health, and automated acceptance
coverage. A feature starts in its owning bounded context; it is never built in
Edge and extracted later.

## Target architecture

```text
client
  |
edge-api
  +-- identity-service ------ identity PostgreSQL schema
  +-- catalog-service ------- catalog PostgreSQL schema + original-book bucket
  +-- retrieval-service ----- retrieval PostgreSQL schema + Qdrant
  +-- answer-service -------- LLM provider
                                |
                         retrieval-service

catalog --BookUploadedV1--> ingestion Lambda/worker
catalog <--BookProcessingFailedV1-- ingestion Lambda/worker
ingestion --BookChunksReadyV1--> retrieval index Lambda/worker
catalog <--BookIndexedV1 / BookIndexingFailedV1-- retrieval index Lambda/worker
```

Synchronous calls are versioned gRPC over mTLS. Asynchronous delivery uses
versioned events, transactional outboxes, durable queues, idempotent consumers,
bounded retries, and dead-letter queues.

## Ownership and dependency rules

| Bounded context | Owns | Does not own |
|---|---|---|
| Edge API | Public HTTP, request validation, perimeter authentication, DTO mapping, routing | Business data, retrieval orchestration, ingestion, prompts |
| Identity | Accounts, credentials, roles, approvals, sessions, token signing | Books, upload authorization policy outside role facts |
| Catalog | Book metadata, original objects, public processing status, upload outbox | Extracted text, chunks, embeddings, vectors |
| Ingestion | Processing jobs, text-PDF extraction, chunk artifacts | Original-book metadata, OCR, EPUB, Qdrant, search |
| Retrieval | Embedding compatibility, evidence projection, Qdrant, semantic search | Book lifecycle, LLM answer synthesis |
| Answer | Prompt construction and grounded answer synthesis | Citation invention, vector storage, book metadata |

- A service is the only writer to its schema, migrations, bucket/prefix,
  queues, and indexes. Cross-service data access always uses a contract.
- Services share only additive protobuf/event contracts and focused platform
  libraries. They never share evolving domain aggregates or runtime config.
- Domain and application code do not depend on HTTP, gRPC, SQL, MinIO,
  RabbitMQ, Qdrant, LLM SDKs, clocks, or UUID generators. Consumer-owned ports
  point outward to adapters; composition happens in `internal/app`.
- Use narrow use-case interfaces. Do not accumulate registration, session,
  approval, catalog, and query behavior in one service object.
- No Go package under one service may import a package under another service.
- Parser, chunker, embedder, and indexer are not independent microservices.
  Extraction/chunking are one Ingestion application deployed as a Lambda or
  portable worker; indexing is a Retrieval application deployed the same way.
  Retrieval query remains an independently deployable gRPC service.
- Admin UI operations route to the service that owns the state. Add a reporting
  service only when it owns a genuine analytics read model.
- Add a Go module only when its delivery milestone begins.

## Lambda deployment policy

Lambda is a compute adapter, not a bounded context. The same application use
case must run behind a thin Lambda handler in AWS and a RabbitMQ worker adapter
in local Compose/CI. Domain code never imports the Lambda SDK, Amazon MQ event
types, IAM concepts, or environment-specific clients.

| Workload | Deployment decision | Reason |
|---|---|---|
| Edge, Identity, Catalog | Long-running service | HTTP/gRPC latency, streaming upload, connection pools, health, and graceful shutdown |
| Text-PDF extract and chunk | Lambda container image when bounded; portable worker deployment alternative | Event-driven, stateless per job, native parser dependencies fit a container image |
| Embed and index a bounded chunk batch | Lambda container image when bounded; portable worker deployment alternative | Idempotent event work that scales independently while remaining Retrieval-owned |
| Retrieval search | Long-running service | Low-latency gRPC and stable Qdrant/provider connections |
| Grounded synchronous answer | Long-running service initially | Predictable gRPC behavior and future response streaming; evaluate Lambda only for non-streaming async answers |
| Expired-artifact cleanup | Scheduled Lambda or local scheduled worker | Short, idempotent, owning-context maintenance |

AWS production uses an Amazon MQ for RabbitMQ event-source mapping with batch
size `1` for document jobs so one failure does not replay unrelated books. MQ
delivery is at least once, so the owning database inbox/business key—not the
Lambda runtime—is the idempotency authority. Each function has a dedicated
execution role, queue, secret reference, reserved concurrency, DLQ alarm, and
network policy restricted to its owned/read-only dependencies.

AWS currently limits a RabbitMQ event-source mapping to one concurrent Lambda
environment by default. Treat that as a throughput constraint: measure it
against the ingestion SLO, request a per-mapping increase only when justified,
or disable the mapping and deploy the portable worker. Events contain object or
manifest references rather than book/chunk bodies, keeping invocation payloads
bounded and private.

Functions have no public URL. Handlers validate the event producer, version,
ID, controlled bucket/prefix reference, and checksum before fetching data; they
never dereference an arbitrary URL from an event. Raw documents use a fresh
per-invocation temporary directory that is cleaned before return and is never
reused as an authorization, idempotency, or content cache.

MQ-triggered functions must finish within 14 minutes; configure the application
deadline below that limit. Temporary storage is bounded and disposable. A job
that cannot satisfy the configured file/page, memory, `/tmp`, or deadline limit
is rejected before expensive work with a visible resource-limit status. If the
accepted product envelope cannot reliably fit Lambda during load tests, deploy
the same application as the queue's container worker instead. Do not race both
adapters on one queue or fork business logic for the alternative deployment.
See the official AWS documentation for
[Amazon MQ event sources](https://docs.aws.amazon.com/lambda/latest/dg/with-mq.html),
[Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html),
and [ephemeral storage](https://docs.aws.amazon.com/lambda/latest/dg/configuration-ephemeral-storage.html).

## Contract rules

- Preserve existing public HTTP paths and protobuf field numbers.
- Make protobuf changes additive and run Buf lint and compatibility checks.
- Each event envelope carries an event ID, occurrence time, correlation ID,
  causation ID, producer, schema version, and idempotency business key.
- Edge derives actor identity from a verified, live Identity session. Clients
  cannot supply their own user ID or role. Internal services authorize the
  verified actor received from the authenticated Edge peer.
- Every external call has a deadline. Public errors are stable and sanitized.
- Uploaded documents, passages, prompts, tokens, and secrets are never logged.

## Milestone 1 — secure service foundation

**Status:** complete.

**Outcome:** a clean checkout runs Edge, Identity, Catalog, and PostgreSQL;
users can register, log in, call `/auth/me`, refresh, and log out.

Delivered:

- Separate Edge, Identity, and Catalog processes.
- PASETO v4 public access tokens, rotating refresh sessions, replay-family
  revocation, server-side session validation, and bounded bcrypt concurrency.
- TLS 1.3 mTLS, SAN-based peer authorization, service-specific secrets,
  dependency-aware readiness, and graceful shutdown.
- Live Identity and Catalog contract tests and black-box HTTP E2E.
- Authenticated `/query` returns truthful `501` until Retrieval exists.

The foundation invariants remain mandatory in all later milestones.

## Milestone 2 — Identity RBAC and approval

**Owning service:** Identity.

**Outcome:** an operator securely creates the singleton admin; verified readers
become active; verified librarians become pending; an admin lists, approves, or
rejects applications; only active accounts can log in.

Implementation:

- Define `librarian`, account status, display name, verification, and auditable
  review state in the greenfield Identity schema baseline.
- Protect initial admin creation with a one-time operator bootstrap code. Make
  creation atomic, permit exactly one admin, and never store or log the code.
- Split Identity application behavior into narrow registration, session,
  admin-bootstrap, and librarian-approval use cases.
- Move persistence ports inward and keep PostgreSQL in an outward adapter.
  Inject time and ID generation through application ports.
- Extend session validation additively to return current account role/status;
  Edge must authorize from this authoritative result rather than stale claims.
- Implement `/setup/status`, `/setup/admin`, pending librarian registration,
  `/admin/users/pending`, approve, and reject routes expected by the UI.
- Pending and rejected accounts receive no tokens and cannot log in.

Acceptance:

- Concurrent bootstrap attempts create one admin only; missing or invalid
  bootstrap codes fail closed.
- Reader and librarian registration remains privacy-preserving and requires a
  single-use email-verification token before account creation.
- Non-admin and stale-role sessions cannot approve or reject librarians.
- Identity unit, PostgreSQL integration, live mTLS contract, UI-compatible HTTP
  E2E, security, race, and abuse-concurrency tests pass.

## Milestone 3 — Catalog upload and durable publication

**Owning service:** Catalog.

**Outcome:** an approved librarian or admin uploads a PDF; authenticated users
can list books and retrieve metadata while processing status remains visible.

Implementation:

- Add the Catalog Book aggregate, application ports, migrations, pagination,
  status state machine, MinIO adapter, and transactional outbox.
- `POST /books` accepts bounded multipart input. Edge streams it over a
  client-streaming Catalog gRPC call without buffering the complete file.
- Support PDF only in this slice. Enforce a configurable size limit, content
  sniffing, generated object keys, checksum verification, and interrupted
  upload cleanup. MinIO remains private; Catalog alone writes original books.
- Add additive `UploadBook`, `ListBooks`, and `GetBook` RPCs and corresponding
  HTTP routes.
- Start RabbitMQ in this milestone. Persist book metadata and `BookUploadedV1`
  in one transaction; retry publication through the outbox with confirms.
- Include immutable book metadata, controlled object reference, checksum,
  media type, actor ID, and correlation data in `BookUploadedV1`.

Acceptance:

- Reader uploads, oversized input, spoofed media types, and client-selected
  object keys fail closed.
- Interrupted streams leave no usable partial book.
- Broker loss does not lose an accepted upload; publication resumes later.
- Duplicate publication is harmless, pagination is deterministic, and the
  complete upload/list/get workflow passes through Edge.

## Milestone 4 — PDF extraction and chunking

**Owning service:** Ingestion.

**Status:** implemented.

**Outcome:** every accepted PDF progresses to processing and then either
produces traceable chunks or displays a deterministic failure status.

Implementation:

- Introduce one Ingestion module containing the extraction/chunking application.
  Parser and chunker remain separate internal components behind narrow ports.
  Ship a thin Lambda container handler for AWS and a RabbitMQ worker command for
  local Compose/CI and as the production deployment alternative when Lambda
  cannot meet the accepted workload envelope.
- Consume `BookUploadedV1` idempotently. Read originals through read-only
  credentials and write derived artifacts only to an Ingestion-owned location.
- Preserve book, chapter, section, page range, chunk order, token bounds,
  extraction/structure/chunking profile, and checksums for every chunk. Carry
  chapter and section context across pages and permit bounded cross-page chunks
  without losing exact source spans.
- Emit `BookChunksReadyV1` with a versioned manifest reference, or
  `BookProcessingFailedV1` with a sanitized category. Catalog consumes these
  events to update status without reading Ingestion storage.
- Preflight file size/page count and enforce a sub-14-minute execution budget.
  Keep raw content only in encrypted transient storage for the invocation and
  never rely on warm-runtime state for correctness.
- Persist retry intent with the inbox state before acknowledging delivery.
  Worker and Lambda adapters apply the same retry/final-failure disposition;
  artifact cleanup is leased, retryable, and cannot starve newer failed jobs.
- Use a buffered post-commit outbox wakeup and bounded batch drain for normal
  low latency, retaining the periodic database scan as the recovery mechanism.
- AWS deploys exactly one active processing mode (`lambda`, `worker`, or
  `paused`) for a queue. Switching modes pauses the current consumer before
  enabling the replacement so two adapters never race the same document.

Acceptance:

- Duplicate and out-of-order events, restarts, parser timeouts, encrypted or
  malformed PDFs, and poison messages have deterministic retry/DLQ behavior.
- The same contract fixture passes through the Lambda handler and portable
  worker adapter; both produce identical application results and idempotency.
- Chunk boundaries and page citations are covered by stable document fixtures.
- Raw book content never appears in logs, traces, event error fields, or DLQs.
- Under processing profile `m4-slo-v1` (text PDF up to 25 MiB/500 pages,
  extracted text up to 64 MiB, five-book sample, two processing slots), the
  extracting status is visible within 2 seconds at p95, ready propagation from
  commit to Catalog is under 1 second, tiny documents finish within 10 seconds
  at p95, and mean ingestion stays below 120 seconds.

## Milestone 5 — Retrieval, indexing, and semantic search

**Owning service:** Retrieval.

**Outcome:** authenticated readers submit `/query` and receive real ranked
passages with book, chapter, page, and relevance evidence.

Implementation:

- Introduce one Retrieval module with an asynchronous index application and a
  synchronous search gRPC application. Deploy bounded index batches as a thin
  Lambda handler in AWS and as a RabbitMQ worker in local Compose/CI; both call
  the same Retrieval-owned use case.
- Consume `BookChunksReadyV1`, generate document embeddings, own the Qdrant
  collection, and perform idempotent vector upserts.
- Maintain an event-derived evidence/book projection locally so search does not
  synchronously fan out to Catalog.
- Version embedding provider, model, dimensions, chunking, and index schema;
  reject incompatible writes and queries.
- Bound manifest work into idempotent chunk batches so a Lambda invocation does
  not approach its payload, memory, temporary-storage, or duration limits.
  Reserved concurrency protects the embedding provider and Qdrant.
- Emit `BookIndexedV1` or `BookIndexingFailedV1` with a sanitized failure
  category for Catalog.
- Activate `/query` additively: retain `question`, add optional filters, and
  return `{query, results}` with retrieved evidence only.

Acceptance:

- Duplicate chunk manifests do not duplicate vectors.
- Model/dimension mismatch, embedding failure, and Qdrant loss fail predictably.
- Filters, empty results, ranking fixtures, citation accuracy, and the
  configured vector-latency objective have automated coverage.
- No result or citation is fabricated when retrieval has no evidence.

## Milestone 6 — optional grounded answers

**Owning service:** Answer.

**Outcome:** users choose evidence-only search or an LLM answer grounded in the
same returned passages.

Implementation:

- Introduce a stateless Answer service with provider-neutral `LLMProvider` and
  Retrieval client ports.
- Add an optional query mode that defaults to search. Extend responses
  additively with an optional `answer` while retaining evidence results.
- Validate every generated citation against retrieved result IDs. Unavailable
  or invalid synthesis degrades to evidence-only output.
- Isolate untrusted passage text from system instructions and bound context,
  output, concurrency, and request deadlines.

Acceptance:

- Prompt-injection fixtures cannot create unsupported citations or reveal
  secrets.
- LLM timeout, malformed output, empty evidence, and provider outage degrade
  safely and preserve truthful evidence.
- Raw prompts, passages, and model output are absent from logs and metrics.

## Milestone 7 — library lifecycle and format completion

**Owning services:** Catalog for commands/status, Ingestion for parsing,
Retrieval for index effects.

**Outcome:** librarians upload EPUB, delete books, and request reindexing while
all users see a consistent lifecycle state.

Implementation:

- Add EPUB as an Ingestion parser adapter without changing chunk contracts.
- Add Catalog-owned delete/reindex commands with idempotent versioned events.
- Retrieval consumes lifecycle events to remove or replace vectors; Catalog
  never accesses Qdrant.
- Use tombstones and explicit state transitions so partial failures remain
  retryable and stale/deleted books are not presented as indexed.
- Enable existing UI lifecycle actions only after the workflows are complete.

Acceptance:

- Delete and reindex tolerate replay, dependency loss, and partial completion.
- PDF and EPUB fixtures produce valid evidence and searchable results.
- Storage, metadata, and vector cleanup converge without cross-service writes.

## Milestone 8 — Internet-ready release

**Outcome:** a release candidate meets security, resilience, performance,
observability, backup, and recovery gates.

- Add trusted-client-aware rate limiting for registration, login, refresh,
  upload, query, and answer endpoints.
- Bound bcrypt, upload, ingestion, embedding, LLM, database, queue, connection,
  and goroutine concurrency.
- Add deadlines, circuit-breaking behavior, queue/backlog metrics, correlation
  across HTTP/gRPC/events, dashboards, and distributed tracing without content.
- Verify backup/restore for service schemas and buckets, RabbitMQ topology, and
  reproducible Qdrant reindexing.
- Verify Lambda aliases/rollback, execution-role least privilege, event-source
  mappings, reserved concurrency, DLQ alarms, container-image scanning, and the
  runbook for disabling the mapping before enabling the portable worker.
- Run concurrent auth, upload, ingestion, retrieval, and answer smoke/load
  tests against the product SLOs before Internet exposure.

## Definition of done for every milestone

- The user/operator outcome works through Edge and, where applicable, the UI.
- The owning service has liveness, dependency-aware readiness, graceful
  shutdown, least-privilege credentials, and isolated storage ownership.
- A Lambda deployment has equivalent invocation/error metrics, a pinned
  version/alias, least-privilege IAM, idempotency evidence, timeout headroom,
  DLQ alarms, and a tested worker fallback; it does not pretend to have service
  health endpoints.
- Domain and application unit tests, adapter integration tests, live mTLS
  contracts, event replay/failure tests, and black-box Compose E2E pass.
- Public protobuf/events are additive and pass Buf lint/compatibility checks.
- Format, lint, vet, race, architecture, secret, dependency, and vulnerability
  gates pass with the repository's pinned patched Go toolchain.
- Configuration, migrations, operational metrics, rollout/deactivation steps,
  and residual risks are documented. Destructive schema/event rollback is not
  used; old consumers remain compatible during rollout.

## Product targets

| Metric | MVP target |
|---|---:|
| Vector retrieval latency | under 100 ms at the Retrieval boundary |
| End-to-end grounded answer | under 3 seconds when provider latency permits |
| Average book ingestion | under 2 minutes for the agreed fixture profile |
| Service availability | 99.5% after Internet release |

Search quality is measured with a versioned offline query/evidence benchmark:
top-k hit rate, citation correctness, context coverage, and unsupported-answer
rate. Operational metrics never contain raw questions or book passages.
