# ── Workspace layout ──────────────────────────────────────────────────────────
# This is a Go workspace (go.work). Each directory listed in go.work has its
# own go.mod. There is NO go.mod at the repo root.
#
# Rule: ALL make targets must be run from the REPO ROOT (where go.work lives).
#
.PHONY: test test-race lint fmt fmt-check vet vuln arch-check proto-check proto-breaking proto-generate build run-edge-api run-identity run-catalog run-ingestion run-retrieval dev local-run local-stop tidy e2e m4-fixtures m4-contract-test m4-integration-test m4-m5-integration-test m4-worker-recovery-test m4-e2e m4-performance-smoke m4-sse-load m4-soak m5-contract-test m5-integration-test m5-search-quality-test m5-worker-recovery-test m5-e2e m5-performance-smoke contract-test minio-runtime-test migrate-identity-up migrate-identity-down migrate-catalog-up migrate-catalog-down migrate-ingestion-up migrate-ingestion-down migrate-retrieval-up migrate-retrieval-down infra-up infra-down stack-up keygen proto dev-certs dev-secrets dev-secrets-catalog-db dev-secrets-m3 dev-secrets-m4 dev-secrets-m5 dev-secrets-test m5-model-bootstrap m5-model-bootstrap-test bootstrap-verifier compose-config m5-mode-policy sam-validate sam-package-check sam-m5-validate sam-m5-package-check ui-check ui-audit secret-scan dockerfile-lint image-build image-scan security-check full-gates integration-gates smtp-url

GITLEAKS_IMAGE := ghcr.io/gitleaks/gitleaks:v8.30.1
HADOLINT_IMAGE := hadolint/hadolint:2.12.0-alpine
# 0.69.2 is the vendor-designated unaffected Trivy release after the 2026
# publishing incident. Do not move this pin without reviewing the advisory.
TRIVY_IMAGE := aquasec/trivy:0.69.2
SERVICE_IMAGES := raglibrarian-identity-service:local raglibrarian-catalog-service:local raglibrarian-edge-api:local raglibrarian-ingestion-service:local raglibrarian-ingestion-lambda:local raglibrarian-ingestion-dispatcher-lambda:local raglibrarian-ingestion-cleanup-lambda:local raglibrarian-retrieval-service:local raglibrarian-retrieval-worker:local raglibrarian-retrieval-qdrant-init:local raglibrarian-retrieval-planner-lambda:local raglibrarian-retrieval-index-lambda:local raglibrarian-retrieval-dispatcher-lambda:local raglibrarian-retrieval-cleanup-lambda:local
QDRANT_IMAGE := qdrant/qdrant:v1.18.3
QDRANT_TRIVY_IGNORE_FILE := security/trivy/qdrant-v1.18.3.ignore.yaml
M5_TEI_IMAGE := ghcr.io/huggingface/text-embeddings-inference@sha256:cb570aabbfa016b86684f576b5bd72d1ee96cc0b7a00b0ad221b298762b32157
M5_TEI_TRIVY_IGNORE_FILE := security/trivy/text-embeddings-inference-cpu-latest.ignore.yaml
M5_PROVIDER_IMAGES := $(QDRANT_IMAGE) $(M5_TEI_IMAGE)
M4_E2E_INGESTION_POSTGRES_DSN_FILE ?= $(CURDIR)/.dev/secrets/ingestion_e2e_dsn
M4_E2E_MINIO_ENDPOINT ?= 127.0.0.1:9000
M4_E2E_MINIO_INSECURE ?= true
M4_E2E_MINIO_CA_FILE ?=
M4_E2E_MINIO_ACCESS_KEY_FILE ?= $(CURDIR)/.dev/secrets/ingestion_e2e_minio_access_key
M4_E2E_MINIO_SECRET_KEY_FILE ?= $(CURDIR)/.dev/secrets/ingestion_e2e_minio_secret_key
M4_E2E_MINIO_ARTIFACT_BUCKET ?= ingestion-artifacts
M4_E2E_RABBITMQ_URI_FILE ?= $(CURDIR)/.dev/secrets/ingestion_e2e_rabbitmq_uri
M4_E2E_FIXTURE_DIR ?= /tmp/raglibrarian-m4-fixtures
M4_E2E_EDGE_BASE_URLS ?= http://127.0.0.1:8080,http://127.0.0.1:8081
M4_E2E_PUBLIC_ORIGIN ?= http://localhost:5173
E2E_PUBLIC_ORIGIN ?= $(M4_E2E_PUBLIC_ORIGIN)

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
	services/ingestion-service \
	services/retrieval-service \
	services/edge-api \
	tests/e2e \
	tools/healthcheck

# Go packages import generated protobuf bindings. Generate them before any
# target that compiles or analyzes those packages.
GO_PROTO_TARGETS := test test-race lint fmt fmt-check vet vuln build run-edge-api run-identity run-catalog run-ingestion run-retrieval tidy e2e
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
	cd services/ingestion-service && go build -o ../../bin/ingestion-service ./cmd/worker
	cd services/retrieval-service && go build -o ../../bin/retrieval-service ./cmd/server

# ── Run ───────────────────────────────────────────────────────────────────────
# run-edge-api: requires AUTH_SECRET_KEY, POSTGRES_DSN, and IDENTITY_GRPC_ADDR.
# Use `make dev` to load .env automatically.
run-edge-api: _require_root
	cd services/edge-api && go run ./cmd/main.go

run-identity: _require_root
	cd services/identity-service && go run ./cmd/main.go

run-catalog: _require_root
	cd services/catalog-service && go run ./cmd/main.go

run-ingestion: _require_root
	cd services/ingestion-service && go run ./cmd/worker

run-retrieval: _require_root
	cd services/retrieval-service && go run ./cmd/server

# stack-up starts the complete local stack, including the migration job.
stack-up: _require_root
	@test -f .env || { \
		echo ""; \
		echo "  !! .env not found. Run: cp .env.example .env && make dev-secrets bootstrap-verifier dev-certs m5-model-bootstrap !!"; \
		echo ""; \
		exit 1; \
	}
	@test -r "$${SECRET_DIR:-.dev/secrets}/identity_runtime_dsn" || { echo "development secrets are missing; run make dev-secrets"; exit 1; }
	@test -r "$${SECRET_DIR:-.dev/secrets}/catalog_migration_password" && test -r "$${SECRET_DIR:-.dev/secrets}/catalog_runtime_password" && test -r "$${SECRET_DIR:-.dev/secrets}/catalog_migration_pgpass" && test -r "$${SECRET_DIR:-.dev/secrets}/catalog_runtime_dsn" || { echo "Catalog database development secrets are missing; run make dev-secrets-catalog-db"; exit 1; }
	@test -r "$${SECRET_DIR:-.dev/secrets}/catalog_minio_access_key" || { echo "MinIO/RabbitMQ development secrets are missing; run make dev-secrets-m3"; exit 1; }
	@bash ./scripts/check-m4-dev-secrets.sh "$${SECRET_DIR:-.dev/secrets}" || { echo "M4 ingestion development secrets are incomplete; run make dev-secrets for a fresh checkout or scripts/run-local.sh for an additive upgrade"; exit 1; }
	@bash ./scripts/check-m5-dev-secrets.sh "$${SECRET_DIR:-.dev/secrets}" || { echo "M5 Retrieval development secrets are incomplete; run make dev-secrets for a fresh checkout or make dev-secrets-m5 for an additive upgrade"; exit 1; }
	@test -r "$${M5_MODEL_DIR:-.dev/models/m5-jina-code-v1}/.revision" || { echo "M5 model cache is missing; run make m5-model-bootstrap"; exit 1; }
	@test -r "$${CERT_DIR:-.dev/certs}/retrieval-service.crt" && test -r "$${CERT_DIR:-.dev/certs}/retrieval-service.key" || { echo "M5 Retrieval certificate is missing; run bash scripts/ensure-m5-dev-cert.sh"; exit 1; }
	@test -r "$${SECRET_DIR:-.dev/secrets}/identity_bootstrap_verifier" || { echo "bootstrap verifier is missing; run make bootstrap-verifier"; exit 1; }
	@EDGE_RETRIEVAL_READINESS_REQUIRED=true docker compose --profile m5 up -d --build

# dev is retained as a convenient alias for the full Compose workflow.
dev: stack-up

local-run: _require_root
	bash ./scripts/run-local.sh

local-stop: _require_root
	bash ./scripts/stop-local.sh

# ── Tidy ──────────────────────────────────────────────────────────────────────
tidy: _require_root
	@for mod in $(MODULES); do \
		echo "Tidying $$mod..."; \
		(cd $$mod && GOWORK=off go mod tidy); \
	done
	go work sync

# ── E2e ───────────────────────────────────────────────────────────────────────
# Requires the local service stack. Start with:
#   make infra-up && make migrate-identity-up
#
# Override target URL:
#   E2E_BASE_URL=http://staging:8080 make e2e
# The complete M2 lifecycle additionally needs E2E_BOOTSTRAP_CODE and a local
# Mailpit latest-message URL such as http://127.0.0.1:8025/view/latest.txt.
e2e: _require_root
	cd tests/e2e && E2E_PUBLIC_ORIGIN="$(E2E_PUBLIC_ORIGIN)" go test -v -tags e2e ./...

# M4 suites are deliberately separate because their files require both the
# e2e and m4 build constraints. All targets expect a running local stack.
m4-contract-test: contract-test

m5-contract-test: contract-test
	@project=raglibrarian-m5-contract-test; \
	trap 'MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 QDRANT_HTTP_PORT=0 QDRANT_GRPC_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile m5-contract-test down -v --remove-orphans' EXIT; \
	MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 QDRANT_HTTP_PORT=0 QDRANT_GRPC_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile m5-contract-test build retrieval-qdrant-init retrieval-service retrieval-contract-tests && \
	MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 QDRANT_HTTP_PORT=0 QDRANT_GRPC_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile m5-contract-test run --rm retrieval-contract-tests

m5-integration-test: m5-search-quality-test m5-contract-test m5-e2e m5-worker-recovery-test

m5-search-quality-test: _require_root
	cd services/retrieval-service && go test -count=1 -run '^TestSearchQualityBenchmark$$' ./internal/application

m5-worker-recovery-test: _require_root
	cd services/retrieval-service && RETRIEVAL_POSTGRES_INTEGRATION=true RETRIEVAL_POSTGRES_DSN_FILE=../../.dev/secrets/retrieval_runtime_host_dsn go test -count=1 -v -tags=integration -run 'Replay|Recovery|TerminalFailure|Visibility|Manifest|FailBatch|CompleteBatch' ./internal/repository

m5-e2e: m4-fixtures
	cd tests/e2e && M5_E2E_FIXTURE_DIR="$(M4_E2E_FIXTURE_DIR)" go test -count=1 -v -tags 'e2e m5' ./...

m5-performance-smoke: _require_root
	cd tests/e2e && go test -count=1 -v -timeout 20m -tags 'e2e m5' -run '^TestM5PerformanceSearchesIndexedEvidenceWithinBudget$$' ./...

# Preserve the complete M2 lifecycle gate, then pass its short-lived sessions
# to the separate M4 process through owner-only files. Token values are never
# placed in command arguments or output.
m4-integration-test: _require_root
	@set -eu; \
	token_dir="$$(mktemp -d /tmp/raglibrarian-m4-tokens.XXXXXX)"; \
	chmod 700 "$$token_dir"; \
	trap 'rm -f "$$token_dir/access" "$$token_dir/revocable"; rmdir "$$token_dir"' EXIT; \
	E2E_M4_ACCESS_TOKEN_OUT="$$token_dir/access" E2E_M4_REVOCABLE_TOKEN_OUT="$$token_dir/revocable" $(MAKE) e2e; \
	M4_E2E_ACCESS_TOKEN_FILE="$$token_dir/access" M4_E2E_REVOCABLE_ACCESS_TOKEN_FILE="$$token_dir/revocable" $(MAKE) m4-e2e; \
	M4_E2E_ACCESS_TOKEN_FILE="$$token_dir/access" $(MAKE) m4-worker-recovery-test

# The combined gate bootstraps Identity once, keeps all session material in one
# owner-only temporary directory, and exercises both M4 production and M5
# preparation/query paths before securely removing the credentials.
m4-m5-integration-test: _require_root
	@set -eu; \
	token_dir="$$(mktemp -d /tmp/raglibrarian-m4-m5-tokens.XXXXXX)"; \
	chmod 700 "$$token_dir"; \
	trap 'rm -f "$$token_dir/access" "$$token_dir/revocable" "$$token_dir/reader" "$$token_dir/librarian" "$$token_dir/admin"; rmdir "$$token_dir"' EXIT; \
	E2E_M4_ACCESS_TOKEN_OUT="$$token_dir/access" E2E_M4_REVOCABLE_TOKEN_OUT="$$token_dir/revocable" \
	E2E_M5_READER_TOKEN_OUT="$$token_dir/reader" E2E_M5_LIBRARIAN_TOKEN_OUT="$$token_dir/librarian" E2E_M5_ADMIN_TOKEN_OUT="$$token_dir/admin" $(MAKE) e2e; \
	M4_E2E_ACCESS_TOKEN_FILE="$$token_dir/access" M4_E2E_REVOCABLE_ACCESS_TOKEN_FILE="$$token_dir/revocable" $(MAKE) m4-e2e; \
	M4_E2E_ACCESS_TOKEN_FILE="$$token_dir/access" $(MAKE) m4-worker-recovery-test; \
	M5_E2E_READER_TOKEN_FILE="$$token_dir/reader" M5_E2E_LIBRARIAN_TOKEN_FILE="$$token_dir/librarian" M5_E2E_ADMIN_TOKEN_FILE="$$token_dir/admin" $(MAKE) m5-e2e

# This gate deliberately controls only the local Compose worker. The E2E test
# owns upload/status assertions and coordinates through two owner-only markers;
# no production control endpoint or shell-command injection seam is introduced.
m4-worker-recovery-test: m4-fixtures
	@set -eu; \
	control_dir="$$(mktemp -d /tmp/raglibrarian-m4-recovery.XXXXXX)"; \
	chmod 700 "$$control_dir"; \
	test ! -L "$$control_dir"; \
	test "$$(stat -c '%a' "$$control_dir")" = 700; \
	test "$$(stat -c '%u' "$$control_dir")" = "$$(id -u)"; \
	test_pid=''; \
	cleanup() { \
		if test -n "$$test_pid" && kill -0 "$$test_pid" 2>/dev/null; then kill "$$test_pid" 2>/dev/null || true; wait "$$test_pid" 2>/dev/null || true; fi; \
		docker compose up -d --no-deps --wait --wait-timeout 120 ingestion-service >/dev/null 2>&1 || true; \
		rm -f "$$control_dir/upload-accepted" "$$control_dir/worker-restarted" "$$control_dir"/.worker-restarted.*; \
		rmdir "$$control_dir" 2>/dev/null || true; \
	}; \
	trap cleanup EXIT INT TERM; \
	docker compose stop --timeout 30 ingestion-service; \
	(cd tests/e2e && \
		M4_E2E_RECOVERY_CONTROL_DIR="$$control_dir" \
		M4_E2E_FIXTURE_DIR="$(M4_E2E_FIXTURE_DIR)" \
		M4_E2E_EDGE_BASE_URLS="$(M4_E2E_EDGE_BASE_URLS)" \
		M4_E2E_PUBLIC_ORIGIN="$(M4_E2E_PUBLIC_ORIGIN)" \
		M4_E2E_INGESTION_POSTGRES_DSN_FILE="$(M4_E2E_INGESTION_POSTGRES_DSN_FILE)" \
		M4_E2E_MINIO_ENDPOINT="$(M4_E2E_MINIO_ENDPOINT)" \
		M4_E2E_MINIO_INSECURE=true \
		M4_E2E_MINIO_ACCESS_KEY_FILE="$(M4_E2E_MINIO_ACCESS_KEY_FILE)" \
		M4_E2E_MINIO_SECRET_KEY_FILE="$(M4_E2E_MINIO_SECRET_KEY_FILE)" \
		M4_E2E_MINIO_ARTIFACT_BUCKET="$(M4_E2E_MINIO_ARTIFACT_BUCKET)" \
		go test -count=1 -v -timeout 10m -tags 'e2e m4' -run '^TestM4WorkerDownRecovery$$' ./...) & \
	test_pid=$$!; \
	found=false; \
	for attempt in $$(seq 1 120); do \
		if test -f "$$control_dir/upload-accepted"; then found=true; break; fi; \
		if ! kill -0 "$$test_pid" 2>/dev/null; then wait "$$test_pid"; exit 1; fi; \
		sleep 1; \
	done; \
	test "$$found" = true || { echo 'worker recovery test did not signal an accepted upload' >&2; exit 1; }; \
	test ! -L "$$control_dir"; \
	test "$$(stat -c '%a' "$$control_dir")" = 700; \
	test "$$(stat -c '%u' "$$control_dir")" = "$$(id -u)"; \
	test ! -L "$$control_dir/upload-accepted"; \
	test "$$(stat -c '%a' "$$control_dir/upload-accepted")" = 600; \
	test "$$(stat -c '%u' "$$control_dir/upload-accepted")" = "$$(id -u)"; \
	test "$$(wc -l < "$$control_dir/upload-accepted")" -eq 1; \
	grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$$' "$$control_dir/upload-accepted"; \
	docker compose up -d --no-deps --wait --wait-timeout 120 ingestion-service; \
	restarted_tmp="$$(mktemp "$$control_dir/.worker-restarted.XXXXXX")"; \
	chmod 600 "$$restarted_tmp"; \
	mv "$$restarted_tmp" "$$control_dir/worker-restarted"; \
	wait "$$test_pid"; \
	test_pid=''

m4-fixtures: _require_root
	go run ./tests/fixtures/ingestion/generate.go -out "$(M4_E2E_FIXTURE_DIR)"

m4-e2e: m4-fixtures
	cd tests/e2e && M4_E2E_FIXTURE_DIR="$(M4_E2E_FIXTURE_DIR)" M4_E2E_EDGE_BASE_URLS="$(M4_E2E_EDGE_BASE_URLS)" M4_E2E_PUBLIC_ORIGIN="$(M4_E2E_PUBLIC_ORIGIN)" M4_E2E_INGESTION_POSTGRES_DSN_FILE="$(M4_E2E_INGESTION_POSTGRES_DSN_FILE)" M4_E2E_MINIO_ENDPOINT="$(M4_E2E_MINIO_ENDPOINT)" M4_E2E_MINIO_INSECURE="$(M4_E2E_MINIO_INSECURE)" M4_E2E_MINIO_CA_FILE="$(M4_E2E_MINIO_CA_FILE)" M4_E2E_MINIO_ACCESS_KEY_FILE="$(M4_E2E_MINIO_ACCESS_KEY_FILE)" M4_E2E_MINIO_SECRET_KEY_FILE="$(M4_E2E_MINIO_SECRET_KEY_FILE)" M4_E2E_MINIO_ARTIFACT_BUCKET="$(M4_E2E_MINIO_ARTIFACT_BUCKET)" M4_E2E_RABBITMQ_URI_FILE="$(M4_E2E_RABBITMQ_URI_FILE)" go test -count=1 -v -tags 'e2e m4' ./...

m4-performance-smoke: m4-fixtures
	cd tests/e2e && M4_E2E_FIXTURE_DIR="$(M4_E2E_FIXTURE_DIR)" M4_E2E_EDGE_BASE_URLS="$(M4_E2E_EDGE_BASE_URLS)" M4_E2E_PUBLIC_ORIGIN="$(M4_E2E_PUBLIC_ORIGIN)" M4_E2E_INGESTION_POSTGRES_DSN_FILE="$(M4_E2E_INGESTION_POSTGRES_DSN_FILE)" M4_E2E_MINIO_ENDPOINT="$(M4_E2E_MINIO_ENDPOINT)" M4_E2E_MINIO_INSECURE="$(M4_E2E_MINIO_INSECURE)" M4_E2E_MINIO_CA_FILE="$(M4_E2E_MINIO_CA_FILE)" M4_E2E_MINIO_ACCESS_KEY_FILE="$(M4_E2E_MINIO_ACCESS_KEY_FILE)" M4_E2E_MINIO_SECRET_KEY_FILE="$(M4_E2E_MINIO_SECRET_KEY_FILE)" M4_E2E_MINIO_ARTIFACT_BUCKET="$(M4_E2E_MINIO_ARTIFACT_BUCKET)" M4_PERFORMANCE_PROFILE="$${M4_PERFORMANCE_PROFILE:-m4-slo-v1}" go test -count=1 -v -timeout "$${M4_PERFORMANCE_TIMEOUT:-20m}" -tags 'e2e m4' -run '^TestM4Performance' ./...

m4-sse-load: _require_root
	cd tests/e2e && go test -count=1 -v -timeout "$${M4_SSE_LOAD_TIMEOUT:-20m}" -tags 'e2e m4 m4_load' -run '^TestM4SSEConnectionCapIsEnforced$$' ./...

m4-soak: m4-fixtures
	@set -eu; \
	cd tests/e2e; \
	soak_tests="$$(go test -count=1 -tags 'e2e m4 m4_soak' -list '^TestM4SoakRepeatedIngestion$$' ./...)"; \
	printf '%s\n' "$$soak_tests" | grep -qx 'TestM4SoakRepeatedIngestion' || { echo 'M4 soak test was not discovered' >&2; exit 1; }; \
	M4_E2E_FIXTURE_DIR="$(M4_E2E_FIXTURE_DIR)" M4_E2E_EDGE_BASE_URLS="$(M4_E2E_EDGE_BASE_URLS)" M4_E2E_PUBLIC_ORIGIN="$(M4_E2E_PUBLIC_ORIGIN)" M4_E2E_INGESTION_POSTGRES_DSN_FILE="$(M4_E2E_INGESTION_POSTGRES_DSN_FILE)" M4_E2E_MINIO_ENDPOINT="$(M4_E2E_MINIO_ENDPOINT)" M4_E2E_MINIO_INSECURE=true M4_E2E_MINIO_ACCESS_KEY_FILE="$(M4_E2E_MINIO_ACCESS_KEY_FILE)" M4_E2E_MINIO_SECRET_KEY_FILE="$(M4_E2E_MINIO_SECRET_KEY_FILE)" M4_E2E_MINIO_ARTIFACT_BUCKET="$(M4_E2E_MINIO_ARTIFACT_BUCKET)" M4_E2E_REFRESH_COOKIE_FILE="$${M4_E2E_REFRESH_COOKIE_FILE:-}" M4_SOAK_DURATION="$${M4_SOAK_DURATION:-30m}" M4_E2E_SOAK_ITERATIONS="$${M4_E2E_SOAK_ITERATIONS:-10}" go test -count=1 -v -timeout "$${M4_SOAK_TIMEOUT:-45m}" -tags 'e2e m4 m4_soak' -run '^TestM4SoakRepeatedIngestion$$' ./...

.PHONY: contract-test
contract-test: _require_root
	@project=raglibrarian-contract-test; \
	trap 'MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile test down -v --remove-orphans' EXIT; \
	MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile test build identity-service catalog-service ingestion-service contract-tests && \
	MAILPIT_UI_PORT=0 POSTGRES_PORT=0 MINIO_API_PORT=0 RABBITMQ_AMQP_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile test run --rm contract-tests

minio-runtime-test: _require_root
	@project=raglibrarian-minio-runtime-test; \
	trap 'MINIO_API_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile minio-runtime-test down -v --remove-orphans' EXIT; \
	MINIO_API_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile minio-runtime-test build catalog-minio-runtime-tests && \
	MINIO_API_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile minio-runtime-test up -d --wait minio && \
	MINIO_API_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile minio-runtime-test run --rm minio-bootstrap && \
	MINIO_API_PORT=0 COMPOSE_PROJECT_NAME=$$project docker compose --profile minio-runtime-test run --rm catalog-minio-runtime-tests

# ── Database ──────────────────────────────────────────────────────────────────
# Uses psql directly — no migrate CLI dependency.
# Identity migrations are applied in lexicographic order (001_, 002_, ...).
migrate-identity-up: _require_root
	docker compose run --rm identity-migrate

migrate-identity-down: _require_root
	docker compose run --rm -e MIGRATION_DIRECTION=down identity-migrate

migrate-catalog-up: _require_root
	docker compose run --rm catalog-migrate

migrate-catalog-down: _require_root
	docker compose run --rm -e MIGRATION_DIRECTION=down catalog-migrate

migrate-ingestion-up: _require_root
	docker compose run --rm ingestion-migrate

migrate-ingestion-down: _require_root
	docker compose run --rm -e MIGRATION_DIRECTION=down ingestion-migrate

migrate-retrieval-up: _require_root
	docker compose run --rm retrieval-migrate

migrate-retrieval-down: _require_root
	docker compose run --rm -e MIGRATION_DIRECTION=down retrieval-migrate

# ── Infrastructure ────────────────────────────────────────────────────────────
infra-up: stack-up

infra-down:
	docker compose down

# ── Keygen ────────────────────────────────────────────────────────────────────
# Prints a new AUTH_SECRET_KEY line ready to paste into .env.
keygen: _require_root
	cd pkg/auth && go run ./cmd/keygen/

dev-secrets: _require_root
	bash ./scripts/generate-dev-secrets.sh

dev-secrets-catalog-db: _require_root
	@echo "Generating Catalog database development credentials..."
	bash ./scripts/generate-catalog-database-dev-secrets.sh

dev-secrets-m3: _require_root
	@echo "Generating MinIO and RabbitMQ development credentials..."
	bash ./scripts/generate-catalog-dev-secrets.sh

dev-secrets-m4: _require_root
	@echo "Generating additive Ingestion, broker, storage, and database credentials..."
	bash ./scripts/generate-m4-dev-secrets.sh

dev-secrets-m5: _require_root
	@echo "Generating additive Retrieval, broker, storage, and database credentials..."
	bash ./scripts/generate-m5-dev-secrets.sh

m5-model-bootstrap: _require_root
	bash ./scripts/bootstrap-m5-model.sh

m5-model-bootstrap-test: _require_root
	bash ./scripts/test-bootstrap-m5-model.sh

dev-secrets-test: _require_root
	bash ./scripts/test-dev-secret-upgrades.sh

bootstrap-verifier: _require_root
	@secret_dir="$${SECRET_DIR:-$(CURDIR)/.dev/secrets}"; \
	case "$$secret_dir" in /*) ;; *) secret_dir="$(CURDIR)/$$secret_dir" ;; esac; \
	test ! -e "$$secret_dir/identity_bootstrap_verifier" || { \
		echo "refusing to overwrite an existing verifier; remove that one file intentionally first"; \
		exit 1; \
	}; \
	cd services/identity-service && GOCACHE="$${GOCACHE:-/tmp/raglibrarian-go-cache}" go run ./cmd/bootstrap-verifier --out "$$secret_dir/identity_bootstrap_verifier"

proto: _require_root
	$(MAKE) proto-check
	$(MAKE) proto-generate

proto-generate: _require_root
	PATH="$$HOME/go/bin:$$PATH" protoc --experimental_allow_proto3_optional -I api/proto --go_out=paths=source_relative:pkg/proto --go-grpc_out=paths=source_relative:pkg/proto api/proto/identity/v1/identity.proto api/proto/catalog/v1/catalog.proto api/proto/ingestion/v1/ingestion.proto api/proto/retrieval/v1/retrieval.proto

proto-check: _require_root
	XDG_CACHE_HOME=/tmp/raglibrarian-cache buf lint api/proto

proto-breaking: _require_root
	XDG_CACHE_HOME=/tmp/raglibrarian-cache buf breaking api/proto --against '.git#branch=main,subdir=api/proto'

dev-certs: _require_root
	bash ./scripts/generate-dev-certs.sh

# ── UI and security gates ────────────────────────────────────────────────────
compose-config: _require_root
	docker compose config --quiet

m5-mode-policy: _require_root
	bash ./scripts/check-m5-processing-policy.sh

sam-validate: _require_root
	bash ./scripts/check-m4-processing-policy.sh
	@command -v sam >/dev/null || { echo "AWS SAM CLI is required"; exit 1; }
	sam validate --lint --template-file infra/aws/m4/template.yaml

sam-m5-validate: _require_root
	bash ./scripts/check-m5-processing-policy.sh
	@command -v sam >/dev/null || { echo "AWS SAM CLI is required"; exit 1; }
	sam validate --lint --template-file infra/aws/m5/template.yaml

# Packaging is intentionally local-only: it validates image references and
# CloudFormation translation without publishing images or changing AWS state.
sam-package-check: sam-validate
	@command -v aws >/dev/null || { echo "AWS CLI is required"; exit 1; }
	@test -n "$${M4_SAM_ARTIFACT_BUCKET:-}" || { echo "M4_SAM_ARTIFACT_BUCKET is required"; exit 1; }
	sam package --template-file infra/aws/m4/template.yaml --s3-bucket "$${M4_SAM_ARTIFACT_BUCKET}" --output-template-file /tmp/raglibrarian-m4-packaged.yaml

sam-m5-package-check: sam-m5-validate
	@command -v aws >/dev/null || { echo "AWS CLI is required"; exit 1; }
	@test -n "$${M5_SAM_ARTIFACT_BUCKET:-}" || { echo "M5_SAM_ARTIFACT_BUCKET is required"; exit 1; }
	sam package --template-file infra/aws/m5/template.yaml --s3-bucket "$${M5_SAM_ARTIFACT_BUCKET}" --output-template-file /tmp/raglibrarian-m5-packaged.yaml

ui-check: _require_root
	npm --prefix ui ci
	npm --prefix ui test
	npm --prefix ui run lint
	npm --prefix ui run type-check
	npm --prefix ui run build

ui-audit: _require_root
	npm --prefix ui audit --audit-level=high

secret-scan: _require_root
	docker run --rm -v "$(CURDIR):/repo:ro" -w /repo $(GITLEAKS_IMAGE) git --redact --no-banner

dockerfile-lint: _require_root
	docker run --rm -i $(HADOLINT_IMAGE) hadolint - < Dockerfile

image-build: _require_root
	docker build --build-arg SERVICE=identity-service -t raglibrarian-identity-service:local .
	docker build --build-arg SERVICE=catalog-service -t raglibrarian-catalog-service:local .
	docker build --build-arg SERVICE=edge-api -t raglibrarian-edge-api:local .
	docker build --target ingestion-runtime --build-arg SERVICE=ingestion-service --build-arg SERVICE_COMMAND=cmd/worker -t raglibrarian-ingestion-service:local .
	docker build --target ingestion-lambda-runtime --build-arg SERVICE=ingestion-service --build-arg SERVICE_COMMAND=cmd/lambda -t raglibrarian-ingestion-lambda:local .
	docker build --target ingestion-lambda-runtime --build-arg SERVICE=ingestion-service --build-arg SERVICE_COMMAND=cmd/dispatcher_lambda -t raglibrarian-ingestion-dispatcher-lambda:local .
	docker build --target ingestion-lambda-runtime --build-arg SERVICE=ingestion-service --build-arg SERVICE_COMMAND=cmd/cleanup_lambda -t raglibrarian-ingestion-cleanup-lambda:local .
	docker build --target retrieval-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/server -t raglibrarian-retrieval-service:local .
	docker build --target retrieval-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/worker -t raglibrarian-retrieval-worker:local .
	docker build --target retrieval-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/qdrant_init -t raglibrarian-retrieval-qdrant-init:local .
	docker build --target retrieval-lambda-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/planner_lambda -t raglibrarian-retrieval-planner-lambda:local .
	docker build --target retrieval-lambda-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/index_lambda -t raglibrarian-retrieval-index-lambda:local .
	docker build --target retrieval-lambda-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/dispatcher_lambda -t raglibrarian-retrieval-dispatcher-lambda:local .
	docker build --target retrieval-lambda-runtime --build-arg SERVICE=retrieval-service --build-arg SERVICE_COMMAND=cmd/cleanup_lambda -t raglibrarian-retrieval-cleanup-lambda:local .

image-scan: image-build
	@for image in $(SERVICE_IMAGES) $(M5_PROVIDER_IMAGES); do \
		ignorefile=""; \
		if [ "$$image" = "$(QDRANT_IMAGE)" ]; then ignorefile="--ignorefile /trivyignore/qdrant-v1.18.3.ignore.yaml"; fi; \
		if [ "$$image" = "$(M5_TEI_IMAGE)" ]; then ignorefile="--ignorefile /trivyignore/text-embeddings-inference-cpu-latest.ignore.yaml"; fi; \
		docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
			-v "$(CURDIR)/$(QDRANT_TRIVY_IGNORE_FILE):/trivyignore/qdrant-v1.18.3.ignore.yaml:ro" \
			-v "$(CURDIR)/$(M5_TEI_TRIVY_IGNORE_FILE):/trivyignore/text-embeddings-inference-cpu-latest.ignore.yaml:ro" \
			$(TRIVY_IMAGE) image --exit-code 1 --ignore-unfixed --severity HIGH,CRITICAL $$ignorefile "$$image" || exit 1; \
	done

security-check: secret-scan dockerfile-lint image-scan ui-audit

full-gates: fmt-check vet lint test test-race arch-check vuln proto-check proto-breaking dev-secrets-test compose-config m5-mode-policy sam-m5-validate ui-check security-check

integration-gates: compose-config
	docker compose --profile m4-ha up -d --build --wait --wait-timeout 180
	$(MAKE) contract-test minio-runtime-test m4-integration-test

smtp-url:
	@echo "Mailpit is available only on http://127.0.0.1:$${MAILPIT_UI_PORT:-8025}"
