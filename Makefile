# ── Workspace layout ──────────────────────────────────────────────────────────
# This is a Go workspace (go.work). Each directory listed in go.work has its
# own go.mod. There is NO go.mod at the repo root.
#
# Rule: ALL make targets must be run from the REPO ROOT (where go.work lives).
#
.PHONY: test test-race lint fmt build run-query dev tidy e2e migrate-up migrate-down infra-up infra-down keygen proto

# Service/library modules — looped over by test, lint, tidy, fmt.
MODULES := \
	pkg/domain \
	pkg/auth \
	pkg/logger \
	pkg/config \
	services/metadata \
	services/query

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
	cd services/query && go build -o ../../bin/query ./cmd/main.go

# ── Run ───────────────────────────────────────────────────────────────────────
# run-query: requires AUTH_SECRET_KEY and POSTGRES_DSN already exported.
# Use `make dev` to load .env automatically.
run-query: _require_root
	cd services/query && go run ./cmd/main.go

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
		cd services/query && go run ./cmd/main.go

PROTO_DIR := pkg/proto

.PHONY: proto
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
tidy: _require_root
	@for mod in $(MODULES) tests/e2e; do \
		echo "Tidying $$mod..."; \
		(cd $$mod && go mod tidy); \
	done
	go work sync

# ── E2e ───────────────────────────────────────────────────────────────────────
# Requires a running service. Start with:
#   make dev                            (in a separate terminal)
#   make infra-up && make migrate-up    (once)
#
# Override target URL:
#   E2E_BASE_URL=http://staging:8080 make e2e
e2e: _require_root
	cd tests/e2e && go test -v -tags e2e ./...

# ── Database ──────────────────────────────────────────────────────────────────
# Uses psql directly — no migrate CLI dependency.
# Files are applied in lexicographic order (001_, 002_, ...).
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
infra-up:
	docker-compose up -d postgres

infra-down:
	docker-compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
# Prints a new AUTH_SECRET_KEY line ready to paste into .env.
keygen: _require_root
	cd pkg/auth && go run ./cmd/keygen/