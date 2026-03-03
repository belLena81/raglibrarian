# 📚 raglibrarian

> A production-grade Retrieval-Augmented Generation (RAG) system for technical books — built in Go with microservices, event-driven ingestion via RabbitMQ, gRPC inter-service communication, and Qdrant vector search.

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/architecture-microservices-orange)](docs/architecture.md)
[![Vector DB](https://img.shields.io/badge/vector--db-Qdrant-red)](https://qdrant.tech)
[![Message Broker](https://img.shields.io/badge/broker-RabbitMQ-FF6600?logo=rabbitmq)](https://rabbitmq.com)

---

## What is raglibrarian?

`raglibrarian` ingests technical PDF books and answers structured questions about their content using LLM-powered semantic search. Instead of asking "what does this book say?", you can ask:

- *"Which book is better for beginners learning memory management in Go?"*
- *"On which pages is BDD compared with TDD? Give me the book and chapter."*
- *"List all sections across my library where concurrency patterns are discussed."*

Every answer returns a structured response: 📘 book title · 📑 chapter · 📄 page numbers · ✂️ extracted passage.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                        API Gateway                                   │
└───────────────────────────────┬─────────────────────────────────────┘
                                │ REST
                    ┌───────────▼───────────┐
                    │     Query Service      │  Go / long-running
                    │  chi + gRPC client     │
                    └──┬─────────────────┬──┘
               gRPC    │                 │   gRPC
        ┌──────────────▼──┐         ┌────▼────────────────┐
        │Retrieval Service │         │  Metadata Service    │  Go / long-running
        │ Qdrant gRPC SDK  │         │  pgx + Postgres      │
        └──────────────────┘         └─────────────────────┘

━━━━━━━━━━━━━━━━━━━━━━━━━━━ RabbitMQ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Exchange: pdf.ingestion        Exchange: pdf.indexing
  ┌──────────────┐               ┌──────────────┐        ┌────────────┐
  │ pdf.uploaded │               │chunks.ready  │        │index.done  │
  └──────┬───────┘               └──────┬───────┘        └─────┬──────┘
         │                              │                       │
  ┌──────▼───────┐               ┌──────▼───────┐       ┌──────▼──────┐
  │ PDF Ingest λ │               │  Embedder λ   │       │ Metadata    │
  │  pdfcpu      │               │  tiktoken +   │       │ Updater λ   │
  │  chunker     │               │  Anthropic    │       │ gRPC call   │
  └──────────────┘               └───────────────┘       └─────────────┘
```

The system is split into two planes:

**Synchronous (gRPC)** — the query path. User queries hit the Query Service, which fans out over gRPC to the Retrieval Service (Qdrant vector search) and Metadata Service (Postgres enrichment), then synthesises an LLM answer in-process.

**Asynchronous (RabbitMQ + Lambda)** — the ingestion pipeline. Book uploads trigger an event chain: PDF parsing → chunking → embedding → vector indexing → metadata update. Each stage is an independent Lambda consuming from a durable RabbitMQ queue.

---

## Repository Structure

```
raglibrarian/
│
├── services/
│   ├── query/              # REST API + RAG orchestration (chi + gRPC client)
│   ├── retrieval/          # Vector search service (Qdrant gRPC)
│   └── metadata/           # Book/chapter/page CRUD (Postgres + gRPC server)
│
├── lambda/
│   ├── ingest/             # PDF parse + chunk (pdfcpu)
│   ├── embedder/           # Embed chunks → write to Qdrant (Anthropic SDK)
│   ├── metadata-updater/   # Update index status via gRPC → Metadata Service
│   └── reindex-scheduler/  # Cron: find stale books, re-emit ingestion events
│
├── pkg/
│   ├── chunker/            # Exportable recursive text splitter
│   ├── proto/              # Shared protobuf definitions + generated Go stubs
│   └── events/             # RabbitMQ message types and publisher/consumer helpers
│
├── migrations/             # golang-migrate SQL migrations
├── deployments/
│   ├── terraform/          # Lambda + API Gateway + IAM
│   └── k8s/                # Manifests for long-running services
│
├── ui/                     # Git submodule → raglibrarian-ui
├── docker-compose.yml      # Local: Postgres + Qdrant + RabbitMQ + services
├── Makefile
└── docs/
    ├── architecture.md
    ├── adr/                # Architecture Decision Records
    └── api/                # OpenAPI spec (swag generated)
```

> **`ui/`** is a Git submodule pointing to [`raglibrarian-ui`](https://github.com/yourname/raglibrarian-ui). See [UI Setup](#ui-setup) below.

---

## Services

| Service | Type | Port | Responsibility |
|---|---|---|---|
| `query` | Long-running | 8080 (HTTP), 9090 (gRPC) | User-facing REST API, RAG orchestration, LLM synthesis |
| `retrieval` | Long-running | 9091 (gRPC) | Semantic vector search against Qdrant with payload filtering |
| `metadata` | Long-running | 9092 (gRPC) | Book/chapter/page CRUD, index status, metadata filtering |
| `ingest` λ | Lambda | — | PDF parse, text extraction, recursive chunking |
| `embedder` λ | Lambda | — | Tokenise chunks, generate embeddings, write to Qdrant |
| `metadata-updater` λ | Lambda | — | Consume `index.done`, update book status via gRPC |
| `reindex-scheduler` λ | Lambda (cron) | — | Find stale books, re-emit `pdf.uploaded` events |

---

## Tech Stack

| Concern | Technology |
|---|---|
| Language | Go 1.22 |
| REST framework | `go-chi/chi` |
| Service transport | gRPC + protobuf |
| Message broker | RabbitMQ (`amqp091-go`) |
| Vector database | Qdrant (gRPC SDK) |
| Metadata database | PostgreSQL (`pgx/v5`) |
| DB migrations | `golang-migrate` |
| PDF parsing | `pdfcpu` + `ledongthuc/pdf` |
| Text chunking | `langchaingo/textsplitter` |
| Tokeniser | `tiktoken-go` |
| LLM | Anthropic Claude (`anthropic-sdk-go`) |
| Async jobs | `riverqueue/river` (local dev) |
| Lambda runtime | `aws-lambda-go` |
| Infrastructure | Terraform (Lambda) + Docker Compose (local) |
| Observability | OpenTelemetry + Zap + Prometheus |
| Testing | testify + testcontainers-go + httpmock |
| API docs | swaggo/swag |

---

## RabbitMQ Exchange Design

```
Exchange: pdf.ingestion  (topic, durable)
  pdf.uploaded    → ingest Lambda queue
  pdf.removed     → cleanup Lambda queue (delete vectors + metadata)

Exchange: pdf.indexing   (topic, durable)
  chunks.ready    → embedder Lambda queue
  index.done      → metadata-updater Lambda queue
  index.failed    → dead letter exchange → alerting
```

Each queue is durable with a DLQ and configurable retry TTL.

---

## gRPC Contracts

```protobuf
// pkg/proto/retrieval.proto
service RetrievalService {
  rpc Search(SearchRequest) returns (stream SearchResult);
  rpc FilteredSearch(FilteredSearchRequest) returns (stream SearchResult);
}

// pkg/proto/metadata.proto
service MetadataService {
  rpc GetBook(BookRequest) returns (Book);
  rpc ListBooks(ListBooksRequest) returns (stream Book);
  rpc UpdateIndexStatus(IndexStatusRequest) returns (StatusResponse);
  rpc FilterBooks(FilterRequest) returns (stream Book);
}
```

Regenerate stubs: `make proto`

---

## Query Response Format

Every query returns a structured payload:

```json
{
  "query": "Where is memory management in Go described in depth?",
  "results": [
    {
      "book": {
        "title": "The Go Programming Language",
        "author": "Donovan & Kernighan",
        "year": 2015,
        "tags": ["go", "systems", "memory"]
      },
      "chapter": "Chapter 12 — Reflection",
      "pages": [231, 232, 233],
      "passage": "Go's garbage collector tracks...",
      "score": 0.94
    }
  ]
}
```

---

## Getting Started

### Prerequisites

- Go 1.22+
- Docker + Docker Compose
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`
- AWS CLI (for Lambda deployments)

### Local Development

```bash
# Clone with UI submodule
git clone --recurse-submodules https://github.com/yourname/raglibrarian
cd raglibrarian

# Copy environment config
cp .env.example .env

# Start infrastructure (Postgres, Qdrant, RabbitMQ) + services
docker-compose up -d

# Run database migrations
make migrate-up

# Start all long-running services
make run-all
```

Services will be available at:
- Query API: `http://localhost:8080`
- Swagger UI: `http://localhost:8080/swagger`
- RabbitMQ Management: `http://localhost:15672`
- Qdrant Dashboard: `http://localhost:6333/dashboard`
- Prometheus metrics: `http://localhost:9090/metrics`

### Add a Book

```bash
curl -X POST http://localhost:8080/books \
  -F "file=@/path/to/book.pdf" \
  -F "title=The Go Programming Language" \
  -F "author=Donovan & Kernighan" \
  -F "year=2015" \
  -F 'tags=["go","systems","concurrency"]'
```

### Query the Library

```bash
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -d '{
    "question": "Which book explains goroutine scheduling in depth?",
    "filters": { "tags": ["go"] }
  }'
```

---

## UI Setup

The `ui/` directory is a Git submodule:

```bash
# If you cloned without --recurse-submodules
git submodule update --init --remote

# Start the UI dev server
cd ui && npm install && npm run dev
```

The UI runs on `http://localhost:5173` and proxies API calls to `:8080`.

To update the UI submodule to latest:

```bash
git submodule update --remote ui
git add ui && git commit -m "chore: update ui submodule"
```

---

## Makefile Commands

```bash
make run-all        # Start all long-running services
make proto          # Regenerate gRPC stubs from .proto files
make migrate-up     # Apply pending DB migrations
make migrate-down   # Roll back last migration
make test           # Run unit + integration tests
make test-e2e       # Run end-to-end tests (requires Docker)
make lint           # golangci-lint
make build-lambda   # Build all Lambda binaries (linux/amd64)
make deploy-lambda  # Deploy Lambdas via Terraform
make swagger        # Regenerate OpenAPI docs
```

---

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `ANTHROPIC_API_KEY` | Anthropic API key for embeddings + LLM | required |
| `POSTGRES_DSN` | Postgres connection string | `postgres://...` |
| `QDRANT_HOST` | Qdrant gRPC host | `localhost:6334` |
| `RABBITMQ_URL` | RabbitMQ AMQP URL | `amqp://guest:guest@localhost:5672/` |
| `S3_BUCKET` | Object storage bucket for PDFs + chunks | required |
| `LOG_LEVEL` | Zap log level | `info` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OpenTelemetry collector endpoint | optional |

---

## Architecture Decisions

Key decisions are documented as ADRs in [`docs/adr/`](docs/adr/):

| # | Decision |
|---|---|
| ADR-001 | Qdrant over pgvector — native payload filtering maps directly to metadata filter queries |
| ADR-002 | RabbitMQ over SQS — runs locally in Docker, richer routing, no vendor lock-in |
| ADR-003 | Lambda only for ingestion — bursty workload, async acceptable; gRPC services stay warm for query path |
| ADR-004 | gRPC between long-running services — typed contracts, streaming support, binary efficiency |
| ADR-005 | Monorepo with UI submodule — shared proto/pkg without multi-repo coordination overhead |

---

## Project Roadmap

- [x] PDF ingestion pipeline (parse → chunk → embed → index)
- [x] gRPC retrieval + metadata services
- [x] RabbitMQ event-driven ingestion
- [x] REST query API with structured responses
- [x] Metadata filtering (author, year, tags)
- [ ] Re-index scheduler Lambda
- [ ] Book removal + vector cleanup
- [ ] Web UI (submodule)
- [ ] Multi-model embedding support (local Ollama fallback)
- [ ] Streaming LLM responses (SSE)
- [ ] OpenTelemetry distributed tracing across Lambda + services

---

## License

MIT — see [LICENSE](LICENSE).