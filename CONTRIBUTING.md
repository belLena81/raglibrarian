# Development workflow

## Repository structure

This is a **Go workspace** (`go.work`). Each package is its own module with
its own `go.mod`:

```
go.work
pkg/
  auth/       go.mod   — PASETO access-token contract
  grpcauth/   go.mod   — verified peer-SAN authorization
  internaltls/go.mod   — TLS 1.3 mTLS credential loading
  logger/     go.mod   — zap constructor
  process/    go.mod   — privilege-drop primitive
  proto/      go.mod   — generated gRPC contracts only
services/
  identity-service/ go.mod — credentials, users, sessions, migrations
  catalog-service/  go.mod — catalog gRPC boundary
  edge-api/         go.mod — public HTTP API and middleware
tests/e2e/           go.mod — HTTP E2E and live mTLS contracts
tools/healthcheck/   go.mod — operational HTTP/gRPC probe
```

The `go.work` file stitches the modules together so cross-module imports
(`services/edge-api` → `pkg/auth`, etc.) resolve to local disk instead of fetching
stale published versions from the module proxy.

## ⚠️  Always run make from the workspace root

```bash
# Find the root from anywhere inside the repo
cd $(git rev-parse --show-toplevel)

make test
make lint
make vet
make fmt-check
make build
make tidy
```

## First-time setup

Go 1.26.5 or newer is required. The workspace toolchain directive, CI, and
Docker builder are intentionally kept on the same patched release.

```bash
git clone <repo>
cd raglibrarian

cp .env.example .env
# Generate the asymmetric key pair and put each value in the owning service
# configuration. Never put IDENTITY_SIGNING_KEY in Edge configuration.
make keygen
make dev-certs
make stack-up       # start and health-check the complete local stack
make e2e            # run black-box HTTP workflows
make contract-test  # run live mTLS and database adapter contracts
```

## Linting

golangci-lint does not support `go.work` workspace mode. `make lint` works
around this by running golangci-lint **per module** with `GOWORK=off`:

```bash
make lint
# equivalent to:
# cd pkg/auth      && GOWORK=off golangci-lint run ./...
# ... and so on for every module
```

If you want to lint a single module while iterating:

```bash
cd pkg/auth && GOWORK=off golangci-lint run ./...
```

## Adding a dependency to a module

```bash
cd services/edge-api     # go into the specific module
go get github.com/some/pkg@latest
go mod tidy
cd ../..
go work sync             # update go.work.sum
```

## Adding a new module

1. Create the directory and run `go mod init github.com/belLena81/raglibrarian/<name>`
2. Add the path to the `use()` block in `go.work`
3. Add the path to `MODULES` in the `Makefile`
4. Run `go work sync`
