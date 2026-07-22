# Architecture Decision Record: additive microservices

**Status:** accepted

## Decision

Deploy service boundaries from the first functional milestone. New product
capabilities are added as services or event consumers; do not first put their
business logic in the public API process and extract it later.

The initial topology is:

```text
client -> edge-api -> identity-service
                   -> catalog-service
```

`edge-api` owns public HTTP, token validation, and routing. It owns no business
data. `identity-service` owns users, credentials, roles, and sessions.
`catalog-service` owns book metadata and ingestion status. All three are
separate processes with versioned gRPC contracts from their first release.

Later capabilities are additive:

```text
catalog-service --BookUploadedV1--> ingestion Lambda/worker
ingestion Lambda/worker --BookChunksReadyV1--> retrieval index Lambda/worker
retrieval index Lambda/worker --BookIndexedV1--> catalog-service
edge-api -> retrieval-service -> Qdrant
edge-api -> answer-service -> retrieval-service (optional synthesis)
```

## Boundary rules

- A service is the only writer to its database/schema and owns its migrations.
  Another service never reads its tables directly.
- Share versioned protobuf/event contracts and small platform libraries only;
  do not share evolving domain aggregates between services.
- Shared platform modules remain single-purpose: token cryptography, internal
  TLS, peer authorization, logging, process primitives, and generated contracts.
  Runtime configuration and business models remain inside the owning service.
- Use Buf compatibility checks for gRPC contracts. Introduce additive fields
  and new versions instead of breaking existing consumers.
- Publish domain events through a transactional outbox. Consumers are
  idempotent and deduplicate by event ID/business key.
- Start the broker with the first upload workflow. Do not build a synchronous
  ingestion flow that must later be replaced by queues.
- Validate PASETO at the edge. Internal services accept actor metadata only on
  authenticated internal gRPC connections; never expose internal ports.

## Clarifications for later milestones

- Identity role/bootstrap/approval behavior is delivered before upload so
  Catalog authorization depends on an existing, independently tested role.
- The first upload is a bounded PDF multipart stream through Edge to Catalog.
  Edge does not buffer or parse the file; Catalog owns metadata, the generated
  object key, the original object, and `BookUploadedV1` publication.
- Ingestion owns processing jobs and derived chunk artifacts. Retrieval alone
  owns embedding compatibility, evidence projections, and Qdrant access.
- Parser, chunker, embedder, and indexer are application components, not four
  microservices. Extraction/chunking belong to Ingestion; indexing belongs to
  Retrieval.
- Admin operations call the service that owns the state. Do not add an Admin
  proxy service unless a separate analytics read model becomes a bounded
  context with its own data and contract.

## Lambda as a deployment adapter

Lambda does not define a service boundary. Bounded asynchronous work may use a
thin Lambda handler while local Compose/CI and workloads outside the accepted
Lambda envelope use a RabbitMQ worker command over the same application use
case:

- Ingestion text-PDF extraction and chunking may run as a Lambda container.
- Retrieval embedding/index batches may run as a Lambda container.
- Short owning-context cleanup tasks may run as scheduled Lambdas.

Edge, Identity, Catalog, Retrieval search, and synchronous Answer remain
long-running services because they require stable HTTP/gRPC, streaming or
pooled connections, dependency-aware readiness, or predictable latency.

AWS uses Amazon MQ for RabbitMQ event-source mappings with batch size one for
document jobs. Delivery is at least once, so a service-owned inbox/business key
provides idempotency. Functions use dedicated least-privilege roles, secret
references, reserved concurrency, bounded temporary storage, DLQs, and alarms.
RabbitMQ mappings default to one concurrent Lambda environment; measured
throughput must justify a mapping-specific limit increase or selection of the
portable worker deployment.
Only one processing mode may consume the document queue. Production mode
changes use an explicit `lambda` -> `paused` -> `worker` (or reverse) handoff;
the paused state is verified before the replacement consumer is enabled.
Functions have no public URL and accept only validated versioned events with
controlled object references; an event can never direct a function to fetch an
arbitrary URL or object prefix.
MQ-triggered work must finish below AWS's 14-minute limit. Work outside the
configured file/page/resource budget fails with a visible bounded status. If
accepted workloads cannot fit reliably, disable the event-source mapping and
deploy the same application as the queue's portable container worker without
duplicating business logic or running competing adapters. See AWS's
[Amazon MQ integration](https://docs.aws.amazon.com/lambda/latest/dg/with-mq.html)
and [Lambda quotas](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html).

## Current state and delivery order

The one-time extraction is complete: Edge, Identity, Catalog, Ingestion,
Retrieval, and Answer are separate owning contexts/process adapters. Identity
RBAC, Catalog upload, Retrieval-owned indexing/search, Qdrant evidence
projection, and optional grounded `/query` answers are implemented in the
current checkout. Answer is stateless, calls Retrieval over mTLS, and owns its
provider-neutral synthesis and citation-validation boundary.

Milestone 4 Ingestion remains a release candidate until protected AWS staging
and controlled restart/DLQ acceptance pass. Milestone 6 Answer remains a
release candidate until protected real-provider staging passes. Those gates are
operational evidence, not boundary changes.

The remaining vertical slices are delivered in this order:

1. EPUB and library lifecycle completion.
2. Internet-ready hardening.

The canonical acceptance criteria are in [README.md](README.md).
