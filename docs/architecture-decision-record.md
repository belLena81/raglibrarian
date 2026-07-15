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
catalog-service --book.uploaded.v1--> ingestion consumer
ingestion consumer --chunks.ready.v1--> indexing consumer
indexing consumer --book.indexed.v1--> catalog-service
edge-api -> retrieval-service -> Qdrant
edge-api -> answer-service (optional synthesis over retrieval evidence)
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

## Migration from the current codebase

Before adding book or ingestion features, turn `services/query` into the
edge-api process and run the existing authentication persistence/use case in an
independent identity-service process. Create catalog-service for book work.
This is a small, one-time extraction while the code is still small; subsequent
features must extend the service map rather than repeat this pattern.
