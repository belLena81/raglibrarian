# SPEC-1: RAG technical-book library

## Background

raglibrarian is a private technical-book library. Readers ask natural-language
questions and receive relevant passages or an optional synthesized answer whose
book, chapter, page range, and supporting text can be verified. Librarians
curate the collection, and a singleton administrator approves librarians and
operates the system.

This document defines product requirements and quality targets. The delivery
sequence is maintained only in [README.md](README.md); the service-boundary
decision is maintained in
[architecture-decision-record.md](architecture-decision-record.md).

## Requirements

### Must have

- Semantic search over owned technical books using natural-language questions.
- Structured evidence containing book, chapter or section, page range, passage,
  and relevance score.
- Optional answer synthesis grounded exclusively in retrieved evidence.
- Three roles:
  - **Reader:** browse metadata, search, and request grounded answers.
  - **Librarian:** reader capabilities plus upload and book lifecycle commands.
  - **Admin:** singleton operator who approves librarians and views operational
    state.
- PASETO authentication with short-lived access tokens, rotating refresh
  sessions, immediate server-side revocation, and live role/status validation.
- PDF and EPUB ingestion into structure-aware, traceable chunks.
- Embeddings and vector similarity search through Retrieval-owned Qdrant.
- Service-owned PostgreSQL schemas, private object storage, and versioned
  protobuf/event contracts.
- Asynchronous ingestion from the first upload using RabbitMQ, transactional
  outboxes, idempotent consumers, bounded retries, and dead-letter queues.
- Clean-checkout local startup and independently verifiable service health.

### Should have

- Metadata filters by tags, author, and publication year.
- Versioned embedding, chunking, and index configurations.
- Reindex and deletion workflows that converge safely after partial failure.
- Search-quality evaluation with stable questions and expected evidence.
- Operational dashboards for latency, errors, queue backlog, ingestion state,
  and provider health.

### Could have

- Hybrid keyword/vector search and reranking.
- Markdown, HTML, and DOCX ingestion adapters.
- Query history and saved searches, subject to an explicit privacy design.
- Librarian correction of extracted structure and chunks.
- A reporting service backed by its own event-derived analytics read model.

### Not in the MVP

- Collaborative book editing or real-time annotation.
- Multi-tenant organization isolation.
- A general-purpose chatbot without library evidence.
- Separate parser, chunker, embedder, or indexer microservices; these are
  application components deployed through Lambda or worker adapters.
- A proxy Admin service that writes Identity or Catalog state.

## Architecture

### Services

1. **Edge API**
   - Public HTTP entry point.
   - Strict input validation, PASETO verification, live session enforcement,
     stable error mapping, and routing.
   - No business database, domain aggregate, ingestion logic, retrieval logic,
     or prompt construction.

2. **Identity Service**
   - Accounts, credentials, roles, librarian applications, sessions, and token
     signing.
   - Atomic one-time administrator bootstrap.
   - Sole writer to the Identity schema.

3. **Catalog Service**
   - Book metadata, original objects, lifecycle commands, and user-visible
     processing/indexing status.
   - Transactional publication of upload and lifecycle events.
   - Sole writer to the Catalog schema and original-book bucket.

4. **Ingestion bounded context**
   - Idempotent processing jobs, extraction, structure detection, and chunking.
   - Owns derived chunk manifests/artifacts; never writes Catalog or Qdrant.
   - Parser and chunker are replaceable internal components.
   - Deployed as a Lambda container for bounded AWS jobs and as a portable
     RabbitMQ worker locally or when Lambda cannot meet the product envelope.

5. **Retrieval Service**
   - Document/query embeddings, index compatibility, evidence projection,
     Qdrant collections, vector search, and context assembly.
   - Sole writer and API owner for Qdrant.
   - Indexing is deployed as a Lambda/worker adapter; the low-latency query
     server remains a long-running service in the same bounded context.

6. **Answer Service**
   - Provider-neutral LLM integration and grounded synthesis.
   - Validates answer citations against Retrieval evidence.
   - Stateless for the MVP and does not own query history.

### Communication

```text
client -> Edge -> Identity
               -> Catalog
               -> Retrieval
               -> Answer -> Retrieval

Catalog --BookUploadedV1--> Ingestion Lambda/worker
Ingestion --BookChunksReadyV1--> Retrieval index Lambda/worker
Ingestion --BookProcessingFailedV1--> Catalog
Retrieval --BookIndexedV1 / BookIndexingFailedV1--> Catalog
```

- Public traffic terminates at Edge. Internal synchronous calls use TLS 1.3
  mTLS and additive versioned gRPC.
- The synchronous dependency graph is acyclic. Event choreography may update
  Catalog status, but no consumer reads or writes another service's tables.
- Edge derives actor context from a verified, live session. Internal services
  accept it only from an authorized Edge certificate and still enforce their
  own role policy.
- Every call carries a correlation ID and deadline. Events also carry event,
  causation, occurrence, producer, schema-version, and idempotency metadata.

### Compute deployment

Bounded, event-triggered compute may use Lambda without becoming a new service:

- PDF/EPUB extraction and chunking may run in an Ingestion Lambda container.
- Bounded embedding/index batches may run in a Retrieval Lambda container.
- Short owning-context cleanup jobs may use scheduled Lambda functions.

Edge, Identity, Catalog, Retrieval search, and synchronous Answer remain
long-running services. They need stable HTTP/gRPC behavior, streaming or pooled
connections, dependency-aware readiness, or predictable response latency.

Lambda handlers are outward adapters over the same application ports used by
local RabbitMQ worker commands. Business code has no AWS SDK/event dependency.
AWS production uses Amazon MQ for RabbitMQ event-source mappings with batch size
one for document jobs. Functions are idempotent because delivery is at least
once; the owning inbox/business key is authoritative. Each function has a
dedicated least-privilege execution role, queue, secret reference, reserved
concurrency, failure alarm, and DLQ.

An Amazon MQ RabbitMQ event-source mapping has one concurrent Lambda execution
environment by default. This is an explicit capacity constraint, not assumed
autoscaling. Load tests decide whether to request a mapping-specific increase
or deploy the portable worker. Broker events carry controlled object/manifest
references, never book or chunk bodies.

Functions have no public URL. The handler validates producer, schema version,
event ID, controlled bucket/prefix reference, and checksum before fetching an
object; event values are never treated as arbitrary URLs. Raw documents use a
fresh per-invocation temporary directory that is cleaned before return and is
not correctness, authorization, or cache state.

Amazon MQ-triggered functions have a 14-minute maximum, and Lambda temporary
storage is finite. Application deadlines leave shutdown headroom, work is split
into bounded batches, and raw content is transient. Jobs outside configured
file/page, memory, storage, or duration limits fail with a visible bounded
status. If accepted workloads cannot fit reliably, disable the event-source
mapping and deploy the same application through the container-worker adapter;
do not run competing adapters on one queue. See AWS's
[Amazon MQ integration](https://docs.aws.amazon.com/lambda/latest/dg/with-mq.html),
[Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html),
and [ephemeral-storage configuration](https://docs.aws.amazon.com/lambda/latest/dg/configuration-ephemeral-storage.html).

### Storage ownership

| Store | Writer | Readers |
|---|---|---|
| Identity PostgreSQL schema | Identity | Identity only |
| Catalog PostgreSQL schema/outbox | Catalog | Catalog only |
| Original-book bucket | Catalog | Catalog; Ingestion via read-only object contract |
| Ingestion PostgreSQL schema/inbox/outbox | Ingestion | Ingestion only |
| Derived chunk-artifact location | Ingestion | Ingestion; Retrieval via read-only manifest contract |
| Retrieval PostgreSQL projection/inbox/outbox | Retrieval | Retrieval only |
| Qdrant collections | Retrieval | Retrieval only |

Each service has distinct credentials and migrations. Sharing one PostgreSQL or
MinIO deployment does not imply shared ownership. Object references are
service-generated, validated, and scoped by least-privilege policy.

## Domain behavior

### Identity and RBAC

- Initial administrator creation requires a one-time operator bootstrap code
  and succeeds only while no administrator exists.
- Reader registration creates an active account.
- Librarian registration creates a pending account/application and issues no
  session until approved.
- Only a live administrator session can list, approve, or reject applications.
- Approval activates the librarian role; rejection remains auditable and
  cannot log in.
- Protected authorization uses Identity's current role/status, not only the
  role embedded in an older access token.

### Book lifecycle

The public state machine uses explicit transitions such as:

```text
pending -> processing -> indexed
                    \-> failed
indexed -> reindexing -> indexed | failed
any non-deleted state -> deleting -> deleted
```

Catalog owns transition validity and presentation. Other services report facts
through events; they do not update Catalog rows.

The first upload release accepts bounded PDF multipart data at Edge and streams
it to Catalog over gRPC. Edge never buffers the complete book or parses it.
Catalog validates content, chooses the object key, calculates a checksum, and
atomically persists metadata plus `BookUploadedV1`. EPUB is added as an
Ingestion adapter after the PDF workflow is stable.

### Ingestion and chunking

The parser preserves document structure where available:

```text
book -> chapter -> section -> overlapping token-bounded chunks
```

Each chunk records the book ID, stable chunk ID, chapter/section label, page
range, order, token bounds, content checksum, extraction version, and chunking
version. Exact window and overlap defaults are configuration owned by
Ingestion and captured in the manifest; changing them creates a new version
rather than silently rewriting indexed evidence.

Malformed, encrypted, unsupported, or resource-exhausting documents produce a
sanitized failure event after bounded retries. Raw text is never placed in log
messages, tracing attributes, broker error fields, or DLQ diagnostic headers.
Preflight rejects work that cannot meet the configured Lambda budget before
expensive parsing. The portable-worker deployment alternative uses the
identical application contract and produces the same events.

### Retrieval

Retrieval consumes a versioned chunk manifest, embeds chunk content, derives a
document embedding from the normalized chunk-vector centroid, and upserts both
vector levels idempotently. The collection schema records embedding
provider/model, dimensions, distance metric, chunking version, and index
version. Incompatible data fails before a partial index is advertised.

The index application divides a manifest into bounded idempotent batches.
Reserved concurrency prevents Lambda bursts from overwhelming the embedding
provider or Qdrant. Lambda and worker adapters share contract fixtures and
produce equivalent results.

Search accepts a bounded question, optional metadata filters, and a bounded
result limit. It returns stored chunk evidence and additive document-level hits
with nested stored evidence. Empty retrieval is a successful empty result, not
an invitation to synthesize citations.

The public search shape remains compatible with the UI contract:

```json
{
  "query": "How does replication work?",
  "results": [
    {
      "book": {"title": "...", "author": "...", "year": 2024, "tags": []},
      "chapter": "Replication",
      "pages": [101, 102],
      "passage": "...",
      "score": 0.87
    }
  ],
  "documents": [
    {
      "book": {"title": "...", "author": "...", "year": 2024, "tags": []},
      "chunk_count": 42,
      "pages": [1, 250],
      "score": 0.79,
      "evidence": [
        {
          "book": {"title": "...", "author": "...", "year": 2024, "tags": []},
          "chapter": "Replication",
          "pages": [101, 102],
          "passage": "...",
          "score": 0.87
        }
      ]
    }
  ]
}
```

### Grounded answers

Answer receives a question and Retrieval evidence through an internal contract.
Passages are untrusted data, not instructions. Prompts clearly separate system
policy, user question, and evidence; context and output are bounded.

Every citation in a generated answer must resolve to a returned evidence ID.
When synthesis times out, fails validation, or is unavailable, the request
degrades to evidence-only output. No source, quotation, chapter, or page is
invented.

## Public interfaces

Existing paths and protobuf field numbers remain stable. Planned additions are
delivered only in the milestone that owns them:

- Identity: setup status/bootstrap, pending librarian registration, application
  list/approve/reject, and authoritative session profile.
- Catalog: client-streaming upload, paginated list, metadata retrieval, delete,
  and reindex commands.
- Retrieval: semantic `Search` with filters and structured evidence.
- Answer: grounded `Answer` over Retrieval evidence.
- `/query`: existing `question`; additive filters and optional answer mode;
  response retains `query` and `results` and later adds optional `answer`.

Events are versioned contracts:

- `BookUploadedV1`
- `BookChunksReadyV1`
- `BookProcessingFailedV1`
- `BookIndexedV1`
- `BookIndexingFailedV1`
- Versioned deletion and reindex events when those workflows are introduced.

Delivery is at least once. Producers use transactional outboxes and publisher
confirms. Consumers use durable queues, inbox/event and business-key
deduplication, bounded retry/backoff, and dead-letter routing.

## Security and privacy

- Never log credentials, tokens, private keys, connection strings, book text,
  raw queries, prompts, passages, or model responses.
- Validate and bound all public input before expensive work. Content type and
  extension are hints; uploaded bytes are inspected.
- Use service-specific mTLS identities, database users, broker users, bucket
  policies, and provider credentials. Internal ports and storage remain private.
- Use dedicated Lambda execution roles and secret references; do not place
  credentials in deployment artifacts or event payloads. Warm `/tmp` content
  and global memory are never correctness or authorization state.
- Apply `Cache-Control: no-store` to authentication and private query content.
- Trust forwarded client addresses only from explicitly configured proxies.
- Rate limiting and bounded bcrypt/upload/embedding/LLM concurrency are release
  blockers before Internet exposure.
- Treat dependencies, PDFs, EPUBs, retrieved text, and model output as untrusted.

## Quality and operational targets

| Metric | MVP target |
|---|---:|
| Vector retrieval latency | under 100 ms at the Retrieval boundary |
| End-to-end grounded answer | under 3 seconds when provider latency permits |
| Average ingestion | under 2 minutes for the agreed book fixture profile |
| Availability | 99.5% after Internet release |

Every delivered capability requires domain/application unit tests, real-adapter
integration tests, mTLS contract tests, event replay/failure tests, and a
black-box workflow through Edge. Race, lint, vet, architecture, protobuf,
secret, dependency, and vulnerability checks are release gates.

Search quality uses a versioned offline benchmark containing questions and
expected evidence. Track top-k hit rate, citation correctness, context coverage,
unsupported-answer rate, ingestion throughput, queue backlog, and dependency
latency. Metrics and traces contain identifiers and measurements, never private
content.
