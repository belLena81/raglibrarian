# Development workflow

## Repository structure

This is a **Go workspace** (`go.work`). Each package is its own module with
its own `go.mod`:

```
go.work
pkg/
  domain/     go.mod   — domain model, no dependencies
  auth/       go.mod   — PASETO + bcrypt
  logger/     go.mod   — zap constructor
  config/     go.mod   — env-var loading
services/
  metadata/   go.mod   — user repository + auth use case
  query/      go.mod   — HTTP API, handlers, middleware
migrations/            — SQL migration files
cmd/keygen/            — operator tool: print a new AUTH_SECRET_KEY
```

The `go.work` file stitches the modules together so cross-module imports
(`pkg/auth` → `pkg/domain`, etc.) resolve to local disk instead of fetching
stale published versions from the module proxy.

## ⚠️  Always run make from the workspace root

```bash
# Find the root from anywhere inside the repo
cd $(git rev-parse --show-toplevel)

make test
make lint
make build
make tidy
```

## First-time setup

```bash
git clone <repo>
cd raglibrarian

cp .env.example .env
# Generate the secret key and paste it into .env:
make keygen

make infra-up       # start Postgres
make migrate-up     # create tables
make test           # run all tests
make run-query      # start the HTTP service
```

## Linting

golangci-lint does not support `go.work` workspace mode. `make lint` works
around this by running golangci-lint **per module** with `GOWORK=off`:

```bash
make lint
# equivalent to:
# cd pkg/domain    && GOWORK=off golangci-lint run ./...
# cd pkg/auth      && GOWORK=off golangci-lint run ./...
# ... and so on for every module
```

If you want to lint a single module while iterating:

```bash
cd pkg/auth && GOWORK=off golangci-lint run ./...
```

## Adding a dependency to a module

```bash
cd services/query        # go into the specific module
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
