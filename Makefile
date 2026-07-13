# ── Workspace layout ──────────────────────────────────────────────────────────
# This is a Go workspace (go.work). Each directory listed in go.work has its
# own go.mod. There is NO go.mod at the repo root.
#
# Rule: ALL make targets must be run from the REPO ROOT (where go.work lives).
#
.PHONY: test test-race lint fmt build run-edge-api run-identity run-catalog dev tidy e2e migrate-identity-up infra-up infra-down keygen proto

# Service/library modules — looped over by test, lint, tidy, fmt.
MODULES := \
	pkg/domain \
	pkg/auth \
	pkg/logger \
	pkg/config \
	pkg/proto \
	services/identity-service \
	services/catalog-service \
	services/edge-api

# Guard: abort if not run from the workspace root.
_require_root:
	@test -f go.work || { \
		echo ""; \
		echo "  !! Run make from the repo root (where go.work lives) !!"; \
		echo ""; \
		exit 1; \
	}

# ── Test ──────────────────────────────────────────────────────────────────────
test: _require_root
	@fail=0; \
	for mod in $(MODULES); do \
		echo "Testing $$mod..."; \
		(cd $$mod && go test ./...) || fail=1; \
	done; \
	exit $$fail

test-race: _require_root
	@fail=0; \
	for mod in $(MODULES); do \
		echo "Testing (race) $$mod..."; \
		(cd $$mod && go test -race ./...) || fail=1; \
	done; \
	exit $$fail

# ── Lint ──────────────────────────────────────────────────────────────────────
lint: _require_root
	@echo "Running golangci-lint across all modules..."
	@fail=0; \
	for mod in $(MODULES); do \
		echo ""; \
		echo "── $$mod ──"; \
		(cd $$mod && GOWORK=off golangci-lint run ./...) || fail=1; \
	done; \
	exit $$fail

# ── Fmt ───────────────────────────────────────────────────────────────────────
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

# ── Build ─────────────────────────────────────────────────────────────────────
build: _require_root
	cd services/edge-api && go build -o ../../bin/edge-api ./cmd/main.go
	cd services/identity-service && go build -o ../../bin/identity-service ./cmd/main.go
	cd services/catalog-service && go build -o ../../bin/catalog-service ./cmd/main.go

# ── Run ───────────────────────────────────────────────────────────────────────
# run-edge-api: requires AUTH_SECRET_KEY, POSTGRES_DSN, and IDENTITY_GRPC_ADDR.
# Use `make dev` to load .env automatically.
run-edge-api: _require_root
	cd services/edge-api && go run ./cmd/main.go

run-identity: _require_root
	cd services/identity-service && go run ./cmd/main.go

run-catalog: _require_root
	cd services/catalog-service && go run ./cmd/main.go

# dev: loads .env then starts the service — the everyday local workflow.
# Usage: make dev
# Prerequisites: .env file exists (copy from .env.example, fill in values).
dev: _require_root
	@test -f .env || { \
		echo ""; \
		echo "  !! .env not found. Run: cp .env.example .env && make keygen >> .env !!"; \
		echo ""; \
		exit 1; \
	}
	@set -a && . ./.env && set +a && \
		cd services/edge-api && go run ./cmd/main.go

# ── Tidy ──────────────────────────────────────────────────────────────────────
tidy: _require_root
	@for mod in $(MODULES) tests/e2e; do \
		echo "Tidying $$mod..."; \
		(cd $$mod && go mod tidy); \
	done
	go work sync

# ── E2e ───────────────────────────────────────────────────────────────────────
# Requires the local service stack. Start with:
#   make infra-up && make migrate-identity-up
#
# Override target URL:
#   E2E_BASE_URL=http://staging:8080 make e2e
e2e: _require_root
	cd tests/e2e && go test -v -tags e2e ./...

# ── Database ──────────────────────────────────────────────────────────────────
# Uses psql directly — no migrate CLI dependency.
# Identity migrations are applied in lexicographic order (001_, 002_, ...).
migrate-identity-up: _require_root
	@set -a && . ./.env && set +a && \
		for f in $(CURDIR)/services/identity-service/migrations/*.up.sql; do \
			echo "Applying $$f..."; \
			psql "$$IDENTITY_POSTGRES_DSN" -f "$$f" || exit 1; \
		done

migrate-down: _require_root
	@set -a && . ./.env && set +a && \
		for f in $$(ls -r $(CURDIR)/migrations/*.down.sql); do \
			echo "Reverting $$f..."; \
			psql "$$POSTGRES_DSN" -f "$$f" || exit 1; \
		done

# ── Infrastructure ────────────────────────────────────────────────────────────
infra-up:
	docker-compose up -d postgres identity-service catalog-service edge-api

infra-down:
	docker-compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
# Prints a new AUTH_SECRET_KEY line ready to paste into .env.
keygen: _require_root
	cd pkg/auth && go run ./cmd/keygen/

proto: _require_root
	XDG_CACHE_HOME=/tmp/raglibrarian-cache buf lint api/proto
	PATH="$$HOME/go/bin:$$PATH" protoc -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto
