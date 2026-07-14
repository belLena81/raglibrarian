# 📚 RAG Tech Books Semantic Search

A **microservice platform for semantic search and Retrieval Augmented
Generation (RAG)** over technical books.

This system ingests technical books (PDF/EPUB), processes them into
semantic chunks, generates embeddings, and enables natural language
search and AI‑generated answers with citations.

The project is designed as an **additive microservice architecture**: each
service is independently deployed from its first release. New capabilities are
introduced by adding a service or event consumer, not by putting the feature in
a shared API process and extracting it later. See
[architecture-decision-record.md](architecture-decision-record.md).

The DOCX plans in this directory are retained as historical planning artifacts.
Where they conflict with this document or the architecture decision record,
this document and the decision record take precedence.

------------------------------------------------------------------------

# 🚀 Features

-   🔎 **Semantic Search** across technical books
-   🤖 **RAG Answer Generation** using LLMs
-   📚 **Structured Retrieval** (Book → Chapter → Fragment)
-   🔐 **Role Based Access Control**
-   📥 **Book Ingestion Pipeline**
-   🧠 **Vector Search with Qdrant**
-   ⚡ **gRPC Microservice Architecture**
-   📊 **Admin statistics & system monitoring**

------------------------------------------------------------------------

# 🏗 Architecture Overview

The system is composed of independent services communicating via
**gRPC**.

                    +-------------+
                    | API Gateway |
                    +-------------+
                           |
         ------------------------------------------------
         |            |            |           |         |
      Auth        Library        Search      RAG       Admin
         |            |            |           |
         |            |            |           |
      Postgres     Postgres      Qdrant      LLM
                        |
                    Ingestion
                        |
                    Workers

## Core Services

### Edge API / Gateway

Entry point for all clients.

Responsibilities:

-   Public HTTP API and request routing
-   PASETO validation at the perimeter
-   Rate limiting (future)
-   Observability entrypoint

The gateway owns no business database and does not contain identity, catalog,
retrieval, or answer use cases.

------------------------------------------------------------------------

### Identity Service

Handles authentication and authorization.

Features:

-   User registration
-   Login
-   PASETO token generation
-   Role validation

Roles:

-   Reader
-   Librarian
-   Admin

------------------------------------------------------------------------

### Catalog Service

Responsible for managing books.

Features:

-   Upload books (PDF / EPUB)
-   Store book metadata
-   List and retrieve books

Storage:

-   Metadata → PostgreSQL
-   Book files → S3 / MinIO

------------------------------------------------------------------------

### Ingestion Service

Processes uploaded books into searchable content.

Pipeline:

    Upload
     ↓
    Parse
     ↓
    Chunk
     ↓
    Embedding
     ↓
    Vector Index

------------------------------------------------------------------------

### Retrieval Service

Provides semantic search functionality.

Process:

    Query
     ↓
    Generate embedding
     ↓
    Vector search (Qdrant)
     ↓
    Return top chunks

------------------------------------------------------------------------

### Answer Service

Generates answers using retrieved context.

    User Question
     ↓
    Retrieve context
     ↓
    Call LLM
     ↓
    Generate answer with citations

------------------------------------------------------------------------

### Admin Service

System management tools.

Features:

-   Librarian approvals
-   System statistics
-   Usage metrics

------------------------------------------------------------------------

# 🗄 Infrastructure

Core infrastructure components:

  Component    Purpose
  ------------ --------------------
  PostgreSQL   Metadata & RBAC
  Qdrant       Vector database
  MinIO / S3   Book storage
  RabbitMQ     Ingestion pipeline
  LLM API      Answer generation

------------------------------------------------------------------------

# 🔄 System Data Flow

## Book Ingestion

    Librarian Upload
          ↓
    Library Service
          ↓
    Ingestion Service
          ↓
    Parse → Chunk → Embed
          ↓
    Qdrant Index

------------------------------------------------------------------------

## Query Pipeline

    User Query
       ↓
    Search Service
       ↓
    Query Embedding
       ↓
    Qdrant Vector Search
       ↓
    Top Chunks
       ↓
    RAG Service
       ↓
    LLM Answer

------------------------------------------------------------------------

# 🧑‍💻 Solo Developer Development Strategy

The platform is built **incrementally**, introducing microservices one
by one while keeping the system operational.

### Service Build Order

    1 Edge API + Identity Service + Catalog Service (separate processes)
    2 Object storage and upload event contract
    3 Ingestion and indexing consumers
    4 Retrieval Service
    5 Answer Service
    6 Admin read/API capability

Each milestone delivers **a fully working system**.

------------------------------------------------------------------------

# 🗺 Development Milestones

## Milestone 1 --- Service Boundaries

Services:

-   Edge API / Gateway
-   Identity Service
-   Catalog Service (health-only stub is acceptable)

Deliverable:

-   Register and login through the gateway
-   Authenticated API
-   Independent health checks and gRPC contract tests for all three processes

------------------------------------------------------------------------

## Milestone 2 --- Catalog and Upload

Add:

-   Catalog Service implementation
-   MinIO

Features:

-   Upload books
-   List books
-   Retrieve metadata

------------------------------------------------------------------------

## Milestone 3 --- Event-Driven Ingestion

Add:

-   RabbitMQ
-   Ingestion consumer

Features:

-   Transactional outbox and `book.uploaded.v1`
-   Idempotent parse/chunk processing with retries and DLQ

------------------------------------------------------------------------

## Milestone 4 --- Retrieval

Add:

-   Qdrant
-   Retrieval Service

Deliverable:

-   Vector similarity search

------------------------------------------------------------------------

## Milestone 5 --- Grounded Answers

Add:

-   Answer Service
-   LLM integration

Deliverable:

-   Natural language answers with citations

------------------------------------------------------------------------

## Milestone 6 --- Async Processing

Add:

-   RabbitMQ
-   Worker services

Workers:

-   parser-worker
-   chunk-worker
-   embedding-worker
-   index-worker

------------------------------------------------------------------------

## Milestone 7 --- Admin Tools

Add:

-   Admin Service

Features:

-   Librarian approvals
-   Usage metrics
-   System monitoring

------------------------------------------------------------------------

# 📁 Repository Structure

    rag-tech-books/

    services/
      api-gateway
      auth-service
      library-service
      ingestion-service
      search-service
      rag-service
      admin-service

    workers/
      parser-worker
      chunk-worker
      embedding-worker
      index-worker

    proto/
      auth.proto
      library.proto
      search.proto
      rag.proto
      admin.proto

    infra/
      docker-compose

------------------------------------------------------------------------

# ⚙️ Local Development

Start infrastructure:

    docker compose up

Services connect to:

-   PostgreSQL
-   Qdrant
-   MinIO
-   RabbitMQ

------------------------------------------------------------------------

# 📊 Performance Targets

  Metric                  Target
  ----------------------- -------------
  Vector search latency   \<100ms
  RAG response time       \<3s
  Book ingestion time     \<2 minutes
  System uptime           \>99.5%

------------------------------------------------------------------------

# 🔮 Future Improvements

-   Hybrid search (BM25 + vector)
-   Reranking models
-   Query history
-   Multi‑tenant libraries
-   Improved chunking strategies
-   UI dashboard

------------------------------------------------------------------------

# 🤝 Contributing

Contributions are welcome!

Please open issues for:

-   architecture improvements
-   ingestion pipeline enhancements
-   search quality improvements

------------------------------------------------------------------------

# 📄 License

MIT License
