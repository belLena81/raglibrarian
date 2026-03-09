# 📚 raglibrarian

> A production-grade Retrieval-Augmented Generation (RAG) system for technical books — built in Go with a microservices architecture, gRPC inter-service communication, and Qdrant vector search.

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

---

## What is raglibrarian?

`raglibrarian` ingests technical PDF books and answers structured questions about their content using LLM-powered semantic search. Instead of asking "what does this book say?", you can ask:

- *"Which book is better for beginners learning memory management in Go?"*
- *"On which pages is BDD compared with TDD? Give me the book and chapter."*
- *"List all sections across my library where concurrency patterns are discussed."*

Every answer will return a structured response: 📘 book title · 📑 chapter · 📄 page numbers · ✂️ extracted passage.

---

## Current State

The project is being built iteratively. This is what exists today:

**Iteration 2 complete** — authentication layer is live and all e2e tests pass.

| Layer | Status         | Notes |
|---|----------------|---|
| Domain model | ✅              | `User`, `Book`, `Chunk`, `Query`, `SearchResult` value objects |
| Auth tokens | ✅              | PASETO v4 local (symmetric, XChaCha20-Poly1305 + BLAKE2b) |
| Password hashing | ✅              | bcrypt |
| HTTP API | ✅              | chi router, graceful shutdown, structured logging |
| Auth endpoints | ✅              | register, login, `/me`, logout |
| Query endpoint | ✅              | stub — returns no results yet |
| DB migrations | ✅              | `users` table |
| e2e test suite | ✅              | 18 tests, all passing |
| gRPC metadata service | 🔜 Iteration 3 | metadata service split |
| Vector search | 🔜 Iteration 5 | Qdrant integration |
| PDF ingestion | 🔜 Iteration 6 | chunking + embedding pipeline |
| Token revocation | 🔜 Iteration 4 | Redis blocklist in metadata service |

---

## Architecture (Target)

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
        │Retrieval Service │         │  Metadata Service    │
        │ Qdrant gRPC SDK  │         │  pgx + Postgres      │
        └──────────────────┘         └─────────────────────┘

━━━━━━━━━━━━━━━━━━━━━━━━━━━ Async ingestion pipeline (future) ━━━━━━━

  pdf.uploaded → ingest → embed → index → metadata update
```

**Today:** a single `query` service handles HTTP, auth, and a stub query handler. The `metadata` package (user repository + auth use case) is wired directly into the query service binary. The gRPC split happens in Iteration 3.

---

## Repository Structure

```
raglibrarian/
│
├── go.work                  # Go workspace — no go.mod at root
│
├── pkg/
│   ├── domain/              # Value objects: User, Book, Chunk, Query, SearchResult
│   ├── auth/                # PASETO v4 tokens, bcrypt password hashing
│   │   └── cmd/keygen/      # Operator CLI: print a new AUTH_SECRET_KEY
│   ├── config/              # Env-var loading, fail-fast validation
│   └── logger/              # Zap constructor
│
├── services/
│   ├── query/               # HTTP API — the only running service today
│   │   ├── cmd/main.go      # Entry point, wiring, graceful shutdown
│   │   ├── server.go        # chi router and route registration
│   │   ├── handler/         # auth_handler, query_handler
│   │   ├── middleware/       # Authenticator (PASETO), RequestLogger
│   │   ├── usecase/         # QueryService (stub)
│   │   ├── repository/      # StubQueryRepository
│   │   └── Dockerfile       # Multi-stage build → distroless/static
│   │
│   └── metadata/            # Auth domain logic (wired into query for now)
│       ├── usecase/         # AuthService: Register, Login
│       └── repository/      # PostgresUserRepository
│
├── migrations/
│   └── 001_create_users.*   # users table
│
├── tests/
│   └── e2e/                 # Black-box HTTP tests (go:build e2e)
│
├── docker-compose.yml       # Postgres for local dev
├── Makefile
├── .env.example
└── CONTRIBUTING.md
```

---

## API Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/healthz` | — | Health check |
| `POST` | `/auth/register` | — | Create account, returns token |
| `POST` | `/auth/login` | — | Returns token |
| `GET` | `/auth/me` | ✅ Bearer | Returns identity from token |
| `POST` | `/auth/logout` | ✅ Bearer | Client-side logout (returns 200) |
| `POST` | `/query/` | ✅ Bearer | Semantic query — stub, returns empty results |

### Register

```bash
curl -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email": "alice@example.com", "password": "secret", "role": "reader"}'
```

```json
{"token": "v4.local...", "role": "reader"}
```

`role` is `reader` (default) or `admin`.

### Login

```bash
curl -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "alice@example.com", "password": "secret"}'
```

### Me

```bash
curl http://localhost:8080/auth/me \
  -H "Authorization: Bearer <token>"
```

```json
{"user_id": "...", "email": "alice@example.com", "role": "reader"}
```

Token claims are read directly — no database call on this endpoint.

### Logout

```bash
curl -X POST http://localhost:8080/auth/logout \
  -H "Authorization: Bearer <token>"
```

Returns `{"message": "logged out"}`. The token remains technically valid until it expires — the client must discard it. Server-side revocation is planned for Iteration 4.

---

## Getting Started

### Prerequisites

- Go 1.26+
- Docker + Docker Compose
- `psql` (for `make migrate-up`)

### Local Development

```bash
git clone https://github.com/belLena81/raglibrarian
cd raglibrarian

# Copy and fill in env config
cp .env.example .env

# Generate the secret key and add it to .env
make keygen >> .env

# Start Postgres
make infra-up

# Apply DB migrations
make migrate-up

# Start the service (loads .env automatically)
make dev
```

The service starts on `:8080` by default.

### Run with Docker Compose

Requires `AUTH_SECRET_KEY` in your environment (or a `.env` file):

```bash
export AUTH_SECRET_KEY=$(make keygen | cut -d= -f2)
docker-compose up
```

---

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `AUTH_SECRET_KEY` | ✅ | — | 32-byte key, hex-encoded. Generate with `make keygen` |
| `POSTGRES_DSN` | ✅ | — | `postgres://user:pass@host:port/db?sslmode=disable` |
| `QUERY_ADDR` | no | `:8080` | HTTP listen address |
| `TOKEN_TTL` | no | `24h` | PASETO token lifetime |
| `LOG_ENV` | no | `development` | `development` or `production` |
| `LOG_LEVEL` | no | `debug` | `debug`, `info`, `warn`, `error` |

---

## Makefile Commands

```bash
make dev           # Load .env and start the query service
make build         # Compile binary to bin/query
make test          # Unit tests across all modules
make test-race     # Unit tests with -race
make lint          # golangci-lint per module (GOWORK=off)
make fmt           # goimports (falls back to gofmt)
make tidy          # go mod tidy + go work sync
make e2e           # End-to-end tests (requires service running on :8080)
make migrate-up    # Apply migrations from migrations/*.up.sql
make migrate-down  # Revert migrations in reverse order
make infra-up      # docker-compose up -d postgres
make infra-down    # docker-compose down
make keygen        # Print a new AUTH_SECRET_KEY= line ready for .env
```

All targets must be run from the **repo root** (where `go.work` lives).

---

## Auth Design Notes

**Tokens** — PASETO v4 local (symmetric). The payload is encrypted with XChaCha20-Poly1305 and authenticated with BLAKE2b. Clients cannot read or tamper with their own token claims. No algorithm negotiation — the algorithm is fixed by the `v4.local.` prefix, so the JWT `alg:none` class of attacks cannot exist.

**Passwords** — bcrypt. Cost factor is Go's `bcrypt.DefaultCost` (10).

**`/auth/me` is DB-free** — identity (user ID, email, role) is embedded in the token. Validating the token is sufficient to serve the endpoint.

**Logout** — currently client-side only. The server returns 200 and the client discards the token. A server-side blocklist (Redis set keyed by token expiry) is planned for Iteration 4 when the metadata service has its own infrastructure.

---

## Testing

```bash
# Unit + middleware tests (no infrastructure required)
make test

# End-to-end tests (Postgres + running service required)
make infra-up && make migrate-up
make dev &          # in background, or separate terminal
make e2e
```

The e2e suite (`tests/e2e/`) is tagged `//go:build e2e` so it is never included in `make test`. It runs 18 tests covering all auth and query paths including error cases.

WARNs in the service logs during `make e2e` are expected — the request logger emits WARN on every 4xx response, and roughly half the e2e tests deliberately send bad requests to verify rejections.

---

## Development Guide

See [CONTRIBUTING.md](CONTRIBUTING.md) for:
- Go workspace layout and the reason there is no `go.mod` at the root
- How to add a dependency to a specific module
- How to add a new module to the workspace
- Why `make lint` uses `GOWORK=off` and how to lint a single module

---

## Roadmap

- [x] Domain model (User, Book, Chunk, Query, SearchResult)
- [x] PASETO v4 auth (register, login, `/me`, logout)
- [x] chi HTTP server with structured logging and graceful shutdown
- [x] Postgres user repository
- [x] e2e test suite
- [x] Dockerfile (multi-stage, distroless runtime)
- [ ] **Iteration 3** — gRPC metadata service split; query service calls metadata over gRPC
- [ ] **Iteration 4** — short-lived access tokens, refresh tokens, server-side revocation
- [ ] **Iteration 5** — Qdrant vector search integration
- [ ] **Iteration 6** — PDF ingestion pipeline (parse → chunk → embed → index)
- [ ] **Iteration 7** — Event-driven ingestion via RabbitMQ
- [ ] **Iteration 8** — OpenTelemetry distributed tracing

---

## License

MIT — see [LICENSE](LICENSE).