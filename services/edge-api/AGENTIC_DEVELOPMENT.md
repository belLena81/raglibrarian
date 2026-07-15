# raglibrarian: Agentic Coding Brief

## Product definition

raglibrarian is a Go, microservice-oriented RAG system for a private library
of technical books. A user uploads a PDF or EPUB and asks a natural-language
question. The system retrieves the most relevant chunks and returns a grounded
answer whose citations identify the book, chapter, pages, and source passage.

The product is not a general chat bot. Its defining quality is **verifiable
answers over owned books**: abstain or state uncertainty when evidence is not
available, and never fabricate citations.

## Product outcomes

1. Librarians can upload and manage technical books.
2. Readers can search a library semantically and receive useful ranked chunks.
3. Readers can receive LLM-synthesised answers that cite those chunks.
4. Admins can manage roles, library health, and ingestion failures.
5. The system remains observable, secure, and runnable locally throughout
   additive delivery.

## Architecture and delivery order

| Stage | Outcome | Primary components |
|---|---|---|
| Complete | Secure service foundation | Edge API, Identity, Catalog, versioned mTLS gRPC |
| Next | Identity RBAC | singleton bootstrap, librarian applications and approval |
| Then | Durable upload | Catalog, MinIO, outbox, RabbitMQ, `BookUploadedV1` |
| Then | Traceable chunks | Ingestion Lambda/worker over one application use case |
| Then | Semantic retrieval | Retrieval index Lambda/worker, gRPC search, Qdrant |
| Then | Grounded answers | Answer service, LLM synthesis over retrieved evidence |
| Ongoing | Production diagnosis | OpenTelemetry traces, metrics, structured logs |

## Non-negotiable design constraints

- Go workspace, not a root module: each package/service has its own `go.mod`.
- HTTP is the public edge; internal service calls use versioned gRPC contracts.
- Postgres holds identities and relational metadata; object storage holds book
  files; Qdrant holds vectors plus filterable chunk metadata.
- Use asynchronous, idempotent ingestion. A retry must not create duplicate
  chunks or vectors.
- Treat Lambda and container workers as deployment adapters. AWS handlers and
  RabbitMQ commands call the same owning-context application use case; AWS SDK
  event types never enter domain/application code.
- Treat user credentials, tokens, book files, and LLM prompts as sensitive.
- Keep an end-to-end vertical slice working after every milestone.

## Definition of done for a change

- The ownership boundary and contract are explicit.
- Unit tests cover the changed rules and error paths.
- A public HTTP or gRPC contract change has a consumer/contract test.
- Configuration, migrations, generated contracts, and documentation change
  together when applicable.
- `make test` passes from the repository root; run `make e2e` when an HTTP
  workflow or service integration changed and the environment is available.
- The change does not emit secrets or unsupported citations.

## Role skills

| Role / skill | Trigger / scope | Required handoff |
|---|---|---|
| Solution architect — `$raglibrarian-solution-architecture` | New service, cross-service change, schema/event/proto decision, or fan-out design | ADR-style boundary, contract, data ownership, risk, acceptance criteria |
| Go backend — `$raglibrarian-go-backend` | Go service, handler, adapter, migration, or module change | Tests, contract/config impact, and security trigger |
| DevOps — `$raglibrarian-devops` | Compose, Docker, CI, runtime config, observability, port, or deployment change | Exposure/secrets assessment and rollback procedure |
| Automation QA — `$raglibrarian-automation-qa` | Contract, integration, E2E, regression, event, or release workflow | Scenarios, fixture lifecycle, commands, remaining gaps |
| Security guard — `$raglibrarian-security-guard` | Auth, upload, secrets, data flow, API, event, CI, dependency, logging, or LLM change | Findings, severity, required fixes, residual risk; blockers require re-review |

The security guard is independent of the implementing agent and is mandatory
for listed high-risk changes. It reduces risk; no review may claim to guarantee
that a system contains no vulnerabilities.

## Fan-out policy

Use one lead agent by default. Parallel work is useful only when branches are
independent and a single owner can integrate them without resolving competing
design decisions.

| Work shape | Fan out? | Agent split |
|---|---|---|
| Small bug or focused change in one package | No | Lead implements and tests. |
| New API or gRPC contract | Limited | One investigator reads consumers/compatibility; lead owns the contract and implementation. |
| A vertical slice spanning query, metadata, proto, migration | Yes, after the design is fixed | Contract investigator; service implementation owner; test/fixture owner. Lead integrates sequentially. |
| Retrieval feature | Yes | Retrieval/schema investigator; query orchestration owner; evaluation/fixture owner. One owner changes shared types. |
| Ingestion pipeline | Yes | Storage/parser investigator; event/idempotency designer; worker/test owner. Lead owns event schema and integration. |
| Security-sensitive change | Review only | Implementation remains single-owner; an independent security reviewer examines the diff and tests. |
| Incident/debugging | Initially | Parallel read-only hypotheses (logs/config, data path, recent changes); stop fan-out before edits. |
| Refactor across shared packages | Usually no | Lead establishes the migration plan; parallelise only mechanical, non-overlapping leaves. |

### Mandatory fan-out gates

Before fanning out, the lead writes down: the desired outcome, interfaces that
are frozen for this task, each agent's file/package ownership, test command,
and integration order. If any of those is unknown, investigate first rather
than parallelising implementation.

After agents return, the lead alone resolves interface conflicts, reviews the
combined diff, runs the full relevant test suite, and updates this brief or the
appropriate project documentation when the architecture changed.

### Never parallelise these edits

- One proto/API schema or its generated files.
- One migration series or database schema decision.
- `go.work`, a shared `go.mod`, dependency versions, or Docker Compose.
- The same package, test fixture, or public documentation file.
- Security policy and its implementation before the policy is decided.

## Agent task template

```text
Goal: <one independently verifiable result>
Scope: <owned directories/files>; do not edit <shared files>
Contract: <frozen interface, or “investigate only”>
Acceptance: <behaviour and test command>
Handoff: <summary, changed files, assumptions, commands run, risks>
```

Use this template for every delegated task. Keep research agents read-only;
give implementation agents exclusive ownership of their files.
