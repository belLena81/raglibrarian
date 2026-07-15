# ── Workspace layout ──────────────────────────────────────────────────────────
# This is a Go workspace (go.work). Each directory listed in go.work has its
# own go.mod. There is NO go.mod at the repo root.
#
# Rule: ALL make targets must be run from the REPO ROOT (where go.work lives).
#
.PHONY: test test-race lint fmt fmt-check vet vuln arch-check proto-check proto-generate build run-edge-api run-identity run-catalog dev tidy e2e migrate-identity-up migrate-identity-down infra-up infra-down stack-up keygen proto dev-certs

# Service/library modules — looped over by test, lint, tidy, fmt.
MODULES := \
	pkg/auth \
	pkg/grpcauth \
	pkg/internaltls \
	pkg/logger \
	pkg/process \
	pkg/proto \
	services/identity-service \
	services/catalog-service \
	services/edge-api \
	tests/e2e \
	tools/healthcheck

# Go packages import generated protobuf bindings. Generate them before any
# target that compiles or analyzes those packages.
GO_PROTO_TARGETS := test test-race lint fmt fmt-check vet vuln build run-edge-api run-identity run-catalog tidy
$(GO_PROTO_TARGETS): proto-generate

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

arch-check: _require_root
	@! rg -n 'github.com/belLena81/raglibrarian/pkg/(domain|config)' --glob '*.go' --glob 'go.mod' .
	@! test -f pkg/proto/cmd/healthcheck/main.go

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
			find $$mod -name '*.go' -not -path '*/vendor/*' -print0 | xargs -0 gofmt -w; \
		done; \
	fi

fmt-check: _require_root
	@test -z "$$(find $(MODULES) -name '*.go' -not -path '*/vendor/*' -print0 | xargs -0 gofmt -d)" || { echo "Go files are not gofmt formatted"; exit 1; }

vet: _require_root
	@fail=0; for mod in $(MODULES); do (cd $$mod && GOWORK=off go vet ./...) || fail=1; done; exit $$fail

vuln: _require_root
	@command -v govulncheck >/dev/null || { echo "govulncheck is required"; exit 1; }
	@fail=0; for mod in $(MODULES); do (cd $$mod && GOWORK=off govulncheck ./...) || fail=1; done; exit $$fail

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

# stack-up starts the complete local stack, including the migration job.
stack-up: _require_root
	@test -f .env || { \
		echo ""; \
		echo "  !! .env not found. Run: cp .env.example .env && make keygen >> .env !!"; \
		echo ""; \
		exit 1; \
	}
	@docker compose up -d --build

# dev is retained as a convenient alias for the full Compose workflow.
dev: stack-up

# ── Tidy ──────────────────────────────────────────────────────────────────────
tidy: _require_root
	@for mod in $(MODULES); do \
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

.PHONY: contract-test
contract-test: _require_root
	docker compose --profile test run --rm contract-tests

# ── Database ──────────────────────────────────────────────────────────────────
# Uses psql directly — no migrate CLI dependency.
# Identity migrations are applied in lexicographic order (001_, 002_, ...).
migrate-identity-up: _require_root
	@set -a && . ./.env && set +a && \
		for f in $(CURDIR)/services/identity-service/migrations/*.up.sql; do \
			echo "Applying $$f..."; \
			psql "$$IDENTITY_POSTGRES_DSN" -f "$$f" || exit 1; \
		done

migrate-identity-down: _require_root
	@set -a && . ./.env && set +a && \
		for f in $$(ls -r $(CURDIR)/services/identity-service/migrations/*.down.sql); do \
			echo "Reverting $$f..."; \
			psql "$$IDENTITY_POSTGRES_DSN" -f "$$f" || exit 1; \
		done

# ── Infrastructure ────────────────────────────────────────────────────────────
infra-up: stack-up

infra-down:
	docker-compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
# Prints a new AUTH_SECRET_KEY line ready to paste into .env.
keygen: _require_root
	cd pkg/auth && go run ./cmd/keygen/

proto: _require_root
	$(MAKE) proto-check
	$(MAKE) proto-generate

proto-generate: _require_root
	PATH="$$HOME/go/bin:$$PATH" protoc -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto

proto-check: _require_root
	XDG_CACHE_HOME=/tmp/raglibrarian-cache buf lint api/proto

dev-certs: _require_root
	bash ./scripts/generate-dev-certs.sh
