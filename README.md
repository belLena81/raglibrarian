# raglibrarian

`raglibrarian` is a Go-based RAG system for a private technical-book library.
The eventual product will ingest books, retrieve evidence, and return answers
with traceable book, chapter, page, and passage citations.

The repository is currently at **Milestone 1**: a secure, runnable service
foundation. It does not yet ingest files, query Qdrant, call an LLM, or return
real retrieval results.

## Architecture decision

The architecture is additive: deploy a service boundary before its capability
grows. New product features are added as a service or event consumer, rather
than first being placed in the public API and extracted later.

```text
client -- HTTPS/HTTP --> edge-api -- mTLS gRPC --> identity-service --> Postgres
                         |
                         +-- mTLS gRPC --> catalog-service (health scaffold)
```

- **edge-api** owns public HTTP, request validation, token verification, route
  composition, and query orchestration. It owns no business database.
- **identity-service** owns credentials, users, roles, and its `identity`
  Postgres schema. It is the only service that signs access tokens.
- **catalog-service** is an independently deployable mTLS gRPC boundary. It
  currently exposes health only; book metadata is its future responsibility.
- Internal gRPC ports and Postgres are private in Compose. Service-to-service
  calls use TLS 1.3 with client certificates.
- Future ingestion, indexing, retrieval, and answer generation will be added
  as separate services/consumers. Their contracts will be versioned and
  additive. See the local architecture decision record in `docs/`.

## Current implementation state

| Capability | State | Notes |
|---|---|---|
| Edge, Identity, Catalog processes | Implemented | Compose builds and starts all three services. |
| Public auth API | Implemented | Register, login, `/me`, and client-side logout. Public registration creates readers only. |
| Access tokens | Implemented | PASETO v4 public, Ed25519 signed by Identity and verified by Edge; 15-minute lifetime and `edge-api` audience. |
| Password storage | Implemented | bcrypt at cost 12; plaintext is never persisted. |
| Identity persistence | Implemented | Identity-owned users migration and Postgres repository. |
| HTTP hardening | Implemented | Strict, bounded JSON, request/header timeouts, security headers, sanitized errors, and request IDs. |
| Real query/retrieval | Not implemented | `/query/` is authenticated but uses a deterministic stub. |
| Sessions, refresh tokens, revocation | Not implemented | Logout instructs the client to discard the access token; a valid token remains usable until expiry. |
| Rate limiting / Redis | Not implemented | Required before an Internet-facing deployment. |
| File upload, ingestion, vectors, LLM | Not implemented | Future additive services. |

## Security model

Run `make keygen` to produce two values:

- `IDENTITY_SIGNING_KEY`: a private Ed25519 key. Configure it only in
  `identity-service`.
- `EDGE_VERIFY_KEY`: the corresponding public key. Configure it only in
  `edge-api`.

Never commit either value, local certificates, connection strings, tokens, or
book content. The signing key must never be present in Edge configuration.

## Public API

| Method | Path | Authentication | Current behaviour |
|---|---|---|---|
| `GET` | `/healthz` | None | Edge process health. |
| `POST` | `/auth/register` | None | Creates a `reader` and returns an access token. |
| `POST` | `/auth/login` | None | Validates credentials and returns an access token. |
| `GET` | `/auth/me` | Bearer token | Returns verified token claims without a database call. |
| `POST` | `/auth/logout` | Bearer token | Client-side logout; token revocation is future work. |
| `POST` | `/query/` | Bearer token | Validates the request and returns stub results. |

Request JSON is strict. Client-supplied `role` and `user_id` fields are
rejected; identity comes only from verified token claims.

## Repository layout

```text
pkg/                 Stable platform packages and generated protobuf clients
services/edge-api/   Public HTTP boundary and query stub
services/identity-service/
                     Identity domain, gRPC adapter, Postgres repository, migration
services/catalog-service/
                     Independent catalog gRPC scaffold
api/proto/           Versioned gRPC source contracts
tests/e2e/           Black-box HTTP tests
```

`go.work` defines the Go workspace; there is intentionally no root `go.mod`.

## Local development

Prerequisites: Go 1.26+, Docker Compose, `psql`, OpenSSL, and `protoc` for
contract generation.

```bash
cp .env.example .env
make keygen >> .env
make dev-certs
make infra-up
make migrate-identity-up
make dev
```

`make dev` starts Edge on `:8080`. For the Compose stack, ensure `.env`
contains both generated key values before `docker compose up`.

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
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for workspace and module rules.
