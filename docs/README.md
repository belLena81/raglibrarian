# raglibrarian delivery roadmap

This is the canonical delivery roadmap for raglibrarian. The product
requirements live in [spec_rag_tech_books.md](spec_rag_tech_books.md), and the
binding service-boundary decision lives in
[architecture-decision-record.md](architecture-decision-record.md). Historical
plans are not active implementation guidance.

The roadmap uses vertical slices: every delivery slice ends with a demonstrable
user or operator outcome, deployable service health, and automated acceptance
coverage. The active bootstrap/test path is app-level, via `make app-bootstrap`
and `make app-test`; the milestone sections below remain for delivery tracing.
A feature starts in its owning bounded context; it is never built in Edge and
extracted later.

Previously verified on July 28, 2026: Ingestion, Retrieval, and Answer service
tests covered the chapter-aware chunking profile, chunk/document vector recall,
reciprocal-rank fusion, Retrieval relevance filtering, bounded document-evidence
hydration, and grounded answer synthesis over validated evidence.

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

Local development and CI use the single runtime Compose profile
`raglibrarian`. Test-only containers run under the separate `tests` profile,
and CI uses `docker-compose.yml` plus `docker-compose.ci.yml`.

## Ownership and dependency rules

| Bounded context | Owns | Does not own |
|---|---|---|
| Edge API | Public HTTP, request validation, perimeter authentication, DTO mapping, routing | Business data, retrieval orchestration, ingestion, prompts |
| Identity | Accounts, credentials, roles, approvals, sessions, token signing | Books, upload authorization policy outside role facts |
| Catalog | Book metadata, original objects, public processing status, upload outbox | Extracted text, chunks, embeddings, vectors |
| Ingestion | Processing jobs, PDF/EPUB extraction, chunk artifacts and cleanup | Original-book metadata, OCR, Qdrant, search |
| Retrieval | Embedding compatibility, evidence projection, Qdrant, semantic search | Book lifecycle, LLM answer synthesis |
| Answer | Prompt construction and grounded answer synthesis | Citation invention, vector storage, book metadata |

- A service is the only writer to its schema, migrations, bucket/prefix,
  queues, and indexes. Cross-service data access always uses a contract.
- Services share only additive protobuf/event contracts and focused platform
  libraries. They never share evolving domain aggregates or runtime config.
- Contract-owned schema/profile constants remain versioned compatibility
  decisions. Operational policy such as deadlines, budgets, limits, retry
  intervals, provider response sizes, and concurrency caps is loaded from
  validated config and threaded into application constructors.
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
- Make protobuf changes additive and run `make proto-check proto-breaking`;
  the compatibility gate compares `api/proto` with the main-branch contract.
- Each event envelope carries an event ID, occurrence time, correlation ID,
  causation ID, producer, schema version, and idempotency business key.
- Edge derives actor identity from a verified, live Identity session. Clients
  cannot supply their own user ID or role. Internal services authorize the
  verified actor received from the authenticated Edge peer.
- Every external call has a deadline. Public errors are stable and sanitized.
- Uploaded documents, passages, prompts, tokens, and secrets are never logged.
- Answer final-answer caching is optional and disabled by default. Retrieval
  still runs before any cache lookup so corpus visibility, filters, and selected
  evidence are current. A reusable cached answer requires matching auth scope,
  filters, limit, minimum evidence score, Retrieval profile, generator profile,
  the complete ordered generator evidence context, answer intent, exact
  canonical topic and guard-token sets, and, for non-exact normalized queries,
  query embedding similarity. Broader lexical or semantic matches are measured
  as near misses and never skip generation. Logs and metrics expose only
  bounded sanitized cache diagnostics, never raw queries, passages, embeddings,
  prompts, fingerprints, or cached answer bodies.
- Retrieval summary assessment caching is separate from the final-answer cache
  and remains disabled unless `RETRIEVAL_SUMMARY_CACHE_TTL` is positive. It
  stores provider profile, keyed normalized question/passage fingerprints,
  relevance, summary, and expiry in Retrieval-owned PostgreSQL state. Query
  semantic metadata is retained only when optional negative reuse is enabled.
  Similar-query reuse is deliberately limited to negative relevance decisions;
  positive summaries require an exact question/passage cache key so a summary
  written for one wording is not reused as grounded content for another.

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
- Authenticated `/query` returned truthful `501` in this foundation slice until
  Retrieval was delivered; the current checkout now routes `/query` to
  Retrieval for evidence-only search.

The foundation invariants remain mandatory in all later milestones.

## Milestone 2 — Identity RBAC and approval

**Owning service:** Identity.

**Status:** complete.

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

**Status:** complete.

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

**Status:** release candidate; controlled local, private workers-first, and
provider-neutral serverless acceptance plus restart/DLQ evidence must pass
before this milestone can be marked complete.

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
  extraction/structure/chunking profile, and checksums for every chunk. Use the
  `chapter-page-window-v1` profile by default: target two source pages, cap a
  passage at three source pages and 800 embedding-input tokens, keep 120-token
  overlap only inside the same chapter, and flush without overlap at chapter or
  part boundaries. Treat these values as a fixed versioned cross-service
  contract; do not tune them with Ingestion-only runtime overrides.
- Keep the chunk sequence validator and overlap-only cleanup in place. They are
  part of the Retrieval embedding/indexing contract, not defensive duplication:
  Ingestion previously produced an overlap-only tail whose `token_end` did not
  advance, and Retrieval correctly rejected the manifest before indexing. Future
  chunker refactors must prove every emitted chunk has sequential order,
  monotonically advancing token bounds, and overlap no larger than the profile.
- Emit `BookChunksReadyV1` with a versioned manifest reference, or
  `BookProcessingFailedV1` with a sanitized category. Catalog consumes these
  events to update status without reading Ingestion storage.
- Preflight file size/page count and enforce a sub-14-minute execution budget.
  Keep raw content only in encrypted transient storage for the invocation and
  never rely on warm-runtime state for correctness.
- Treat the source-size and page-count envelopes as a shared processing
  contract. The current `m4-slo-v1` profile accepts documents up to 25 MiB and
  1000 pages; Ingestion extraction config, Retrieval manifest validation,
  Catalog processing-event validation, Compose/local host env, docs, and UI
  copy must be updated together. A document under 25 MiB can still exceed the
  page-count envelope, so PDF page-limit failures use the sanitized
  `pdf_page_limit_exceeded` diagnostic while public UI remains category based.
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
- Under processing profile `m4-slo-v1` (text PDF up to 25 MiB/1000 pages,
  extracted text up to 64 MiB, five-book sample, two processing slots), the
  extracting status is visible within 2 seconds at p95, ready propagation from
  commit to Catalog is under 1 second, tiny documents finish within 10 seconds
  at p95, and mean ingestion stays below 120 seconds.

## Milestone 5 — Retrieval, indexing, and semantic search

**Owning service:** Retrieval.

**Status:** complete in the current checkout.

**Outcome:** authenticated readers submit `/query` and receive real ranked
passages with book, chapter, page, and relevance evidence.

Implementation:

- One Retrieval module contains an asynchronous index application and a
  synchronous search gRPC application. Bounded index batches run through thin
  Lambda adapters in AWS and a RabbitMQ worker in local Compose/CI; both call
  the same Retrieval-owned use cases.
- Consume `BookChunksReadyV1`, generate chunk embeddings plus centroid-derived
  document embeddings, own the Qdrant collection, and perform idempotent vector
  upserts.
- Maintain an event-derived evidence/book projection locally so search does not
  synchronously fan out to Catalog.
- Version embedding provider, model, dimensions, chunking, and index schema
  through the Retrieval index profile digest; reject incompatible writes and
  queries.
- Treat manifest integrity validation as an embedding/indexing gate. Retrieval
  must reject stale profiles, non-advancing chunk windows, duplicate batch
  inputs, or dimension mismatches before upserting Qdrant vectors or emitting
  `BookIndexedV1`.
- Bound manifest work into idempotent chunk batches so a Lambda invocation does
  not approach its payload, memory, temporary-storage, or duration limits.
  Reserved concurrency protects the embedding provider and Qdrant.
- Emit `BookIndexedV1` or `BookIndexingFailedV1` with a sanitized failure
  category for Catalog.
- `/query` is active and additive: it retains `question`, accepts optional
  filters, and returns `{query, results, documents}` with retrieved evidence
  only.

Acceptance:

- Duplicate chunk manifests do not duplicate vectors.
- Model/dimension mismatch, embedding failure, and Qdrant loss fail predictably.
- Filters, empty results, chunk/document ranking fixtures, citation accuracy,
  and the configured vector-latency objective have automated coverage.
- No result or citation is fabricated when retrieval has no evidence.

## Current planned work

Milestone 7 lifecycle/format completion is a release candidate pending its live
EPUB/reindex/delete convergence gate. Milestone 8 Internet-ready hardening
follows it. Milestones 4 and 6 also remain release candidates until their
controlled serverless and real-provider acceptance gates pass.

## Milestone 6 — optional grounded answers

**Owning service:** Answer.

**Status:** release candidate in the current checkout; protected real-provider
acceptance remains required.

**Outcome:** users choose evidence-only search or an LLM answer grounded in the
same returned passages. Free-tier LLM usage is intentionally bounded, so
grounded answers may take up to 5 minutes in free environments.

Implementation:

- Introduce a stateless Answer service with provider-neutral `LLMProvider` and
  Retrieval client ports.
- Retrieval-side LLM calls assess individual passages against the user
  question. The model must return structured relevance, not a whole-book or
  document summary. A high vector score is not sufficient when the configured
  LLM says the passage is irrelevant.
- Retrieval excludes passages assessed as irrelevant, including meta responses
  such as "the user is asking..." or "the passage does not contain...". Search
  backfills from later vector candidates until the requested limit is filled or
  the candidate scan budget is exhausted.
- `limit` means the final primary evidence budget after vector-score filtering,
  index visibility checks, and LLM relevance exclusions. With `limit: 5`, the
  response contains at most five accepted primary evidence passages; fewer may
  be returned when fewer relevant passages remain.
- Book matches are grouping metadata derived from accepted passages only. They
  must not introduce additional supporting passages and must not trigger
  whole-book or document-level LLM summaries.
- Valid JSON assessments retain structured relevance filtering. With
  `RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE=json_or_plain`, a bounded, sanitized plain
  text response is treated as the summary for an already score-filtered
  passage. Set `strict_json` to require the structured relevance contract.

  This compatibility decision is needed because some OpenAI-compatible,
  especially free-tier, models return a successful chat completion as plain
  text even when asked for `response_format: {"type":"json_object"}`. Treating
  every such response as a provider failure exhausts the configured candidate
  scan and can turn otherwise available retrieval evidence into an unavailable
  search result. `json_or_plain` preserves answers for those providers without
  exposing raw model output: only a non-empty, bounded summary that passes the
  meta/refusal/instruction filters is displayed. It deliberately treats that
  accepted summary as relevant for the passage already admitted by Retrieval's
  score and index-visibility filters. Deployments that require the model to
  make the relevance decision must set `strict_json` and use a provider that
  honors the structured-output contract.

  A provider error or rejected provider reply does not make search unavailable:
  Retrieval discards that model output, opens a per-search provider circuit,
  and uses deterministic local summaries for the failed and remaining
  score-filtered candidates. An explicit, valid `relevant:false` assessment is
  still honored and excluded. This prevents a misbehaving or rate-limited
  optional enrichment provider from consuming the entire search deadline while
  preserving the distinction between a valid negative assessment and a failed
  assessment.

  `RETRIEVAL_SUMMARY_LLM_MAX_CALLS` is an external-call budget, not an evidence
  exclusion policy. A value of `0`, or exhaustion of a positive budget, uses
  the same deterministic local-summary path for remaining candidates rather
  than returning an empty result set.

  If the Retrieval summary provider is disabled, search remains vector-only and
  uses deterministic local passage summaries. Configuring an external provider
  sends the question and passage to that provider; do not configure one unless
  its data-handling policy is approved for the indexed content.
- `RETRIEVAL_SUMMARY_LLM_MAX_CALLS` bounds passage assessment calls per search.
  Its default is sized to cover Retrieval's candidate scan budget so LLM
  exclusions can still backfill to the requested result limit under normal
  conditions.
- `RETRIEVAL_SUMMARY_CACHE_TTL` enables the optional Retrieval-side
  summary-assessment cache when positive. `RETRIEVAL_SUMMARY_CACHE_MAX_ENTRIES`
  must also be positive when the cache is enabled and bounds stored rows. An
  enabled cache also requires a stable secret at
  `RETRIEVAL_SUMMARY_CACHE_HMAC_KEY_FILE`; changing it intentionally makes
  existing keyed entries unreachable until expiry cleanup removes them.
  `RETRIEVAL_SUMMARY_CACHE_NEGATIVE_REUSE` controls similar-query negative
  reuse, and
  `RETRIEVAL_SUMMARY_CACHE_NEGATIVE_MINIMUM_COSINE` gates semantic compatibility.
  `RETRIEVAL_SUMMARY_CACHE_NEGATIVE_CANDIDATE_LIMIT` bounds the number of
  negative rows considered for one lookup.
  `RETRIEVAL_SUMMARY_CACHE_CLEANUP_INTERVAL`,
  `RETRIEVAL_SUMMARY_CACHE_CLEANUP_TIMEOUT`, and
  `RETRIEVAL_SUMMARY_CACHE_CLEANUP_BATCH_SIZE` bound the independent expiry
  cleanup. Cleanup runs at startup and periodically even when cache writes are
  disabled so TTL remains a retention boundary.
  The default is disabled (`0s`) so retrieval-side LLM behavior remains
  unchanged until explicitly configured.
- TEI embedding diagnostics always log response byte count and SHA-256 digest.
  Set `RETRIEVAL_TEI_LOG_RAW_RESPONSE=true` only for local diagnosis when the
  embedding provider response body itself must be inspected; the raw prefix is
  bounded by `RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES` and defaults to 4096
  bytes. Do not enable this in shared or production logs.
- Ingestion host mode sets `INGESTION_COMMAND_FAILURE_TRACE=1` so parser
  command failures include only sanitized diagnostics in the service log:
  command basename and argument count, an allowlisted reason, exit code or
  signal, and parser-stderr byte count plus a truncated SHA-256 digest. Raw
  parser stderr and arguments are never logged. For local owner-controlled
  debugging, set `INGESTION_DEBUG_DUMP_PDFTEXT_DIR` to an absolute private
  directory to write the raw `pdftotext` stdout stream as a `0600` file; the
  service log records only the dump path, byte count, and SHA-256. Keep the
  setting empty in shared and production environments. If a streaming page
  consumer fails after `pdftotext` starts successfully, preserve and log the
  downstream stage error instead of relabeling it as a parser failure.
- `INGESTION_PARSER_SANDBOX_MEMORY_BYTES` defaults to 1536 MiB. Keep it aligned
  with `INGESTION_MEMORY_LIMIT_BYTES` and `INGESTION_WORK_CONCURRENCY`: EPUB
  parsing runs a Go child process inside `parser_sandbox`, and the Go runtime
  needs more virtual address space than its live heap suggests. A lower
  `RLIMIT_AS` can surface as `epub_parser_invalid_args` or a Go runtime
  out-of-memory stack even when the EPUB is within archive limits.
- The parser seccomp policy denies direct and `io_uring` networking, namespace
  changes, process/session detachment, and process creation. Legacy `clone` is
  permitted only with `CLONE_THREAD` so the Go EPUB parser can create runtime
  threads; the sandbox intentionally does not restore `RLIMIT_NPROC`.
- EPUB parser sandbox incident note: if the first attempt fails with
  `epub_parser_invalid_args` and the sanitized diagnostics indicate a resource
  failure, check the parser address-space budget before treating it as malformed
  EPUB argv. Keep the 1536 MiB default and the worker overcommit guard unless a
  replacement parser/runtime is proven with live EPUB uploads.
- Answer synthesis receives only the top accepted passage after Retrieval
  exclusions and minimum-score filtering. Answer service owns the final
  human-readable citation format: book title, author when available, page range,
  and one short provider-generated synopsis.
- Chunking/model tuning must be evaluated with quality fixtures before changing
  defaults. Compare at least the current profile, a one-page profile with
  10-15% overlap, and a two-page/three-page-max profile. Measure Recall@5,
  Precision@5 after LLM exclusions, answer citation support rate, provider call
  count, TEI latency/errors, and indexed chunk-count growth. Promote any chosen
  change as a new versioned profile accepted by Catalog, Ingestion, and
  Retrieval together.
- Add an optional query mode that defaults to search. Extend responses
  additively with an optional `answer` while retaining evidence results.
- Validate every generated citation against retrieved result IDs. Unavailable
  or invalid synthesis degrades to evidence-only output.
- Isolate untrusted passage text from system instructions and bound context,
  output, concurrency, and request deadlines.
- Keep the service stateless and expose it only through mTLS gRPC. Edge owns the
  public mode selector, a separate answer-request rate limit, and one truthful
  direct-Retrieval fallback when Answer is unavailable.
- Run the provider over HTTPS with an operator-configured CA and file-backed
  key on an isolated egress network. The deterministic provider stub exists
  only in the explicit test profile.
- If a free model ignores or rejects JSON mode, retry once with a documented
  plain-text format that must start with a model-authored `Citations:` line and
  a matching answer line. The service validates those citation IDs against the
  retrieved evidence set and rejects plain text that omits or invents
  citations.

Acceptance:

- Prompt-injection fixtures cannot create unsupported citations or reveal
  secrets.
- LLM timeout, malformed output, empty evidence, and provider outage degrade
  safely and preserve truthful evidence.
- Raw prompts, passages, and model output are absent from logs and metrics.
- Deterministic quality, race, protobuf compatibility, and live mTLS contract
  gates pass locally; the protected real-provider quality gate is still open.

## Milestone 7 — library lifecycle and format completion

**Owning services:** Catalog for commands/status, Ingestion for parsing,
Retrieval for index effects.

**Status:** release candidate; focused local gates pass, while the protected
live M7 convergence test remains required.

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

## Definition of done for every delivery slice

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
