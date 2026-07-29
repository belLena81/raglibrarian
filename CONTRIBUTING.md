# Development workflow

## Repository structure

This is a Go workspace (`go.work`) with no root `go.mod`. Each shared package,
service, test suite, and tool is its own module:

```text
go.work
pkg/
  auth/          go.mod — PASETO access-token contract
  contracts/     go.mod — shared contract helpers
  grpcauth/      go.mod — verified peer-SAN authorization
  indexprofile/  go.mod — versioned Retrieval/Catalog profile constants
  internaltls/   go.mod — TLS 1.3 mTLS credential loading
  logger/        go.mod — zap constructor
  process/       go.mod — privilege-drop primitive
  proto/         go.mod — generated gRPC contracts only
  providerhttp/  go.mod — provider-neutral HTTP, TLS, and secret helpers
  rabbitmqconn/  go.mod — RabbitMQ connection helpers
  retrydelay/    go.mod — shared retry-delay policy helpers
services/
  edge-api/          go.mod — public HTTP API and route composition
  identity-service/  go.mod — credentials, users, roles, sessions, migrations
  catalog-service/   go.mod — book metadata, original objects, lifecycle status
  ingestion-service/ go.mod — extraction, chunking, workers, Lambda adapters
  retrieval-service/ go.mod — indexing, evidence projection, search, Qdrant
  answer-service/    go.mod — grounded synthesis and citation validation
tests/e2e/                go.mod — black-box HTTP workflows
tools/healthcheck/        go.mod — operational HTTP/gRPC probe
tools/rabbitmq-topology/  go.mod — RabbitMQ topology verifier
```

The `go.work` file stitches modules together so cross-module imports resolve to
local disk instead of stale published versions from the module proxy. Generated
Go protobuf bindings under `pkg/proto/` are intentionally not committed; Make
generates them before Go compile/analyze targets.

The `ui/` directory is a nested React repository. Use root Make targets such as
`make ui-check` and `make ci-ui` unless you are intentionally working inside
the nested UI checkout.

## Always run Make from the workspace root

```bash
cd "$(git rev-parse --show-toplevel)"

make test
make lint
make vet
make fmt-check
make build
make tidy
```

`make lint`, `make vet`, `make test`, and race checks iterate over the
workspace modules. In restricted environments, set `GOCACHE` and
`GOLANGCI_LINT_CACHE` to writable paths under `/tmp`.

## First-time setup

Go 1.26.5 or newer is required. The workspace toolchain directive, CI, and
Docker builder are intentionally kept on the same patched release.

```bash
git clone <repo>
cd raglibrarian

cp .env.example .env
make dev-secrets       # file-backed local runtime secrets
make test-secrets      # isolated e2e/CI credentials
make bootstrap-verifier
make dev-certs
make app-bootstrap     # standard full-stack local startup
make app-test          # current full local static/security gate
```

`make app-bootstrap` is the app-level alias for the current local startup path.
The runtime uses the single Compose profile `raglibrarian`; test containers use
the separate `tests` profile. `make app-test` maps to `make full-gates`; live
contract and integration suites remain separate targets because they start or
depend on a running stack.

## Linting

golangci-lint does not support `go.work` workspace mode. `make lint` works
around this by running golangci-lint per module with `GOWORK=off`:

```bash
make lint
# equivalent to:
# cd pkg/auth && GOWORK=off golangci-lint run ./...
# ... and so on for every module in Makefile MODULES
```

If you want to lint a single module while iterating:

```bash
cd services/retrieval-service && GOWORK=off golangci-lint run ./...
```

## Adding a dependency to a module

```bash
cd services/edge-api
go get github.com/some/pkg@latest
go mod tidy
cd ../..
go work sync
```

Keep dependencies in the module that owns the use case or adapter. Do not add
transport, database, queue, cloud SDK, or provider dependencies to domain or
application packages.

## Adding a new module

Add a module only when its owning bounded context, public contract, storage
ownership, deployment shape, and acceptance test are recorded in
[docs/README.md](docs/README.md). A Lambda and a portable worker for one use
case belong to one module; they are adapters, not duplicate services.

1. Create the directory and run
   `go mod init github.com/belLena81/raglibrarian/<name>`.
2. Keep domain/application packages independent of transport, persistence, and
   cloud SDKs; put Lambda, worker, gRPC, storage, provider, and runtime
   integrations in adapters.
3. Add the path to the `use()` block in `go.work`.
4. Add the path to `MODULES` in the `Makefile` and applicable CI scans.
5. Run `go work sync`, architecture checks, and the milestone's contract/E2E
   acceptance command.
