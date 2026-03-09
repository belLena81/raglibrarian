# ── Workspace layout ──────────────────────────────────────────────────────────
# This is a Go workspace (go.work) where each package is its own module.
# There is NO go.mod at the root — all go commands must target individual
# modules, not ./... from the root.
#
# Rule: ALL make targets must be run from the REPO ROOT (where go.work lives).
#
.PHONY: test test-race lint fmt build run-query tidy migrate-up migrate-down infra-up infra-down keygen

# Every module listed in go.work — keep in sync with the use() block.
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
# go test ./... at the workspace root fails — no go.mod there.
# We run `go test` per-module instead.
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
# golangci-lint v2 does not support go.work workspace mode.
# Run per-module with GOWORK=off so it reads each module's own go.mod.
# See https://github.com/golangci/golangci-lint/issues/3841
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
# Rewrites all .go files in place. Use goimports when available — it is a
# strict superset of gofmt that also sorts and groups import blocks.
# Install: go install golang.org/x/tools/cmd/goimports@latest
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
	go build -o bin/query ./services/query/cmd/main.go

run-query: _require_root
	go run ./services/query/cmd/main.go

# ── Tidy ──────────────────────────────────────────────────────────────────────
tidy: _require_root
	@for mod in $(MODULES); do \
		echo "Tidying $$mod..."; \
		(cd $$mod && go mod tidy); \
	done
	go work sync

# ── Database ──────────────────────────────────────────────────────────────────
migrate-up: _require_root
	migrate -path migrations -database "$$POSTGRES_DSN" up

migrate-down: _require_root
	migrate -path migrations -database "$$POSTGRES_DSN" down 1

# ── Infrastructure ────────────────────────────────────────────────────────────
infra-up:
	docker-compose up -d postgres

infra-down:
	docker-compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
keygen: _require_root
	go run ./pkg/auth/cmd/keygen
