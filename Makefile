# ── Workspace layout ──────────────────────────────────────────────────────────
# Go workspace (go.work). Every target must be run from the REPO ROOT.
# Dependency chain (rightmost runs first):
#
#   tidy → proto → fmt → lint → test → build / run / dev
#
# Each target declares its prerequisite explicitly so the chain is enforced
# by make itself — no need to repeat the chain in every recipe.
#
.PHONY: \
	proto fmt lint test test-race build build-metadata \
	run-query run-metadata dev dev-metadata \
	tidy e2e test-integration \
	infra-up infra-down migrate-up migrate-down \
	keygen _require_root

# ── Modules ───────────────────────────────────────────────────────────────────
# All modules touched by fmt, lint, tidy, and test.
# pkg/proto is included because it has a test file and generated stubs to lint.
MODULES := \
	pkg/domain \
	pkg/auth \
	pkg/logger \
	pkg/config \
	pkg/proto \
	services/metadata \
	services/query

PROTO_DIR := pkg/proto

# ── Root guard ────────────────────────────────────────────────────────────────
_require_root:
	@test -f go.work || { \
		echo ""; \
		echo "  !! Run make from the repo root (where go.work lives) !!"; \
		echo ""; \
		exit 1; \
	}

# ── Proto ─────────────────────────────────────────────────────────────────────
# Regenerates Go code from .proto sources.
# Run this when retrieval.proto or metadata.proto change.
# Install tools once with: go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5
#                          go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.70.0
PHONY: proto
proto: ## Regenerate Go code from all .proto sources under $(PROTO_DIR) (subdirs included)
	@which protoc             > /dev/null || (echo "protoc not found — install protobuf-compiler"; exit 1)
	@which protoc-gen-go      > /dev/null || (echo "protoc-gen-go not found — run: go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5"; exit 1)
	@which protoc-gen-go-grpc > /dev/null || (echo "protoc-gen-go-grpc not found — run: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.70.0"; exit 1)
	@echo "Generating Go code from proto files in $(PROTO_DIR)..."
	@find $(PROTO_DIR) -name '*.proto' | sort | while read f; do \
		echo "  protoc $$f"; \
		protoc \
			--proto_path=$(PROTO_DIR) \
			--go_out=$(PROTO_DIR) \
			--go_opt=paths=source_relative \
			--go-grpc_out=$(PROTO_DIR) \
			--go-grpc_opt=paths=source_relative \
			"$$f" || exit 1; \
	done
	@echo "Done."

# ── Tidy ──────────────────────────────────────────────────────────────────────
# go mod tidy on every module, then sync the workspace.
# Run before fmt so generated/updated imports are correct before formatting.
tidy: _require_root
	@for mod in $(MODULES) tests/e2e; do \
		echo "Tidying $$mod..."; \
		(cd $$mod && go mod tidy); \
	done
	go work sync

# ── Fmt ───────────────────────────────────────────────────────────────────────
# Uses goimports when available (sorts imports + formats); falls back to gofmt.
# tidy is NOT a prerequisite of fmt — tidy is slow and optional before fmt.
# The chain tidy → proto → fmt is invoked explicitly when needed (e.g. make build).
fmt: _require_root
	@if command -v goimports > /dev/null 2>&1; then \
		echo "Running goimports across all modules..."; \
		for mod in $(MODULES); do \
			find $$mod -name '*.go' -not -path '*/vendor/*' | xargs goimports -w; \
		done; \
	else \
		echo "goimports not found, falling back to gofmt..."; \
		for mod in $(MODULES); do \
			gofmt -w $$mod; \
		done; \
	fi

# ── Lint ──────────────────────────────────────────────────────────────────────
# fmt runs first so linting always sees formatted code.
# GOWORK=off prevents golangci-lint from trying to resolve workspace-relative
# replace directives that it does not natively understand.
lint: fmt
	@echo "Running golangci-lint across all modules..."
	@fail=0; \
	for mod in $(MODULES); do \
		echo ""; \
		echo "── $$mod ──"; \
		(cd $$mod && GOWORK=off golangci-lint run ./...) || fail=1; \
	done; \
	exit $$fail

# ── Test ──────────────────────────────────────────────────────────────────────
# lint (and therefore fmt) runs first.
test: lint
	@fail=0; \
	for mod in $(MODULES); do \
		echo "Testing $$mod..."; \
		(cd $$mod && go test ./...) || fail=1; \
	done; \
	exit $$fail

# test-race: same chain as test but with -race detector.
test-race: lint
	@fail=0; \
	for mod in $(MODULES); do \
		echo "Testing (race) $$mod..."; \
		(cd $$mod && go test -race ./...) || fail=1; \
	done; \
	exit $$fail

# test-integration: runs //go:build integration tests (requires Docker for testcontainers).
# Skips lint — integration tests are run in a dedicated CI job after unit tests pass.
test-integration: _require_root
	@fail=0; \
	for mod in $(MODULES); do \
		echo "Integration testing $$mod..."; \
		(cd $$mod && go test -tags integration ./...) || fail=1; \
	done; \
	exit $$fail

# ── Build ─────────────────────────────────────────────────────────────────────
# Full chain: fmt → lint → test → build.
# Use `make build-fast` if you want to skip test (e.g. in a pre-push hook).
build: test
	@mkdir -p bin
	cd services/query && go build -trimpath -ldflags="-s -w" -o ../../bin/query ./cmd/main.go
	@echo "Built bin/query"

build-metadata: test
	@mkdir -p bin
	cd services/metadata && go build -trimpath -ldflags="-s -w" -o ../../bin/metadata ./cmd/main.go
	@echo "Built bin/metadata"

# build-fast: fmt + lint only, no test. For rapid iteration during development.
build-fast: lint
	@mkdir -p bin
	cd services/query    && go build -trimpath -ldflags="-s -w" -o ../../bin/query    ./cmd/main.go
	cd services/metadata && go build -trimpath -ldflags="-s -w" -o ../../bin/metadata ./cmd/main.go
	@echo "Built bin/query bin/metadata"

# ── Run ───────────────────────────────────────────────────────────────────────
# fmt → lint → test before running in production-like mode.
# Requires AUTH_SECRET_KEY, POSTGRES_DSN, AMQP_URL already exported.
run-query: test
	cd services/query && go run ./cmd/main.go

run-metadata: test
	cd services/metadata && go run ./cmd/main.go

# ── Dev ───────────────────────────────────────────────────────────────────────
# fmt → lint before starting (skips test for fast iteration).
# Loads .env automatically. `make infra-up` first.
dev: lint
	@test -f .env || { \
		echo ""; \
		echo "  !! .env not found. Run: cp .env.example .env && make keygen >> .env !!"; \
		echo ""; \
		exit 1; \
	}
	@set -a && . ./.env && set +a && \
		cd services/query && go run ./cmd/main.go

dev-metadata: lint
	@test -f .env || { \
		echo ""; \
		echo "  !! .env not found. Run: cp .env.example .env && make keygen >> .env !!"; \
		echo ""; \
		exit 1; \
	}
	@set -a && . ./.env && set +a && \
		cd services/metadata && go run ./cmd/main.go

# ── E2e ───────────────────────────────────────────────────────────────────────
# Hits a live service. Start with:
#   make infra-up && make migrate-up
#   make dev           (terminal 1 — query)
#   make dev-metadata  (terminal 2 — metadata)
#
# Override targets:
#   E2E_BASE_URL=http://staging:8080 E2E_AMQP_URL=amqp://staging:5672/ make e2e
e2e: _require_root
	cd tests/e2e && go test -v -tags e2e ./...

# ── Database ──────────────────────────────────────────────────────────────────
migrate-up: _require_root
	@set -a && . ./.env && set +a && \
		for f in $(CURDIR)/migrations/*.up.sql; do \
			echo "Applying $$f..."; \
			psql "$$POSTGRES_DSN" -f "$$f" || exit 1; \
		done

migrate-down: _require_root
	@set -a && . ./.env && set +a && \
		for f in $$(ls -r $(CURDIR)/migrations/*.down.sql); do \
			echo "Reverting $$f..."; \
			psql "$$POSTGRES_DSN" -f "$$f" || exit 1; \
		done

# ── Infrastructure ────────────────────────────────────────────────────────────
# infra-up starts only the backing services (postgres + rabbitmq), not the
# application containers — those are started via `make dev` / `make dev-metadata`.
infra-up:
	docker-compose up -d postgres rabbitmq
	@echo "Waiting for services to be healthy..."
	@docker-compose ps

infra-down:
	docker-compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
keygen: _require_root
	cd pkg/auth && go run ./cmd/keygen/