#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

screen_session_exists() {
  local session="$1"
  cleanup_dead_screen_sessions >/dev/null
  screen -ls 2>/dev/null | grep -Eq "\\.${session}[[:space:]]+\\([^)]*\\)[[:space:]]+\\((Detached|Attached)\\)"
}

cleanup_dead_screen_sessions() {
  screen -wipe 2>/dev/null || true
}

start_screen_service() {
  local session="$1"
  local service="$2"
  local work_dir="$3"
  shift 3
  local log_file="$root_dir/_logs/$service/service.log"

  if screen_session_exists "$session"; then
    echo "Screen session already running: $session"
    return
  fi

  mkdir -p "$(dirname "$log_file")"
  screen -dmS "$session" bash ./scripts/run-host-service.sh "$service" "$log_file" "$work_dir" "$@"
}

main() {
  cd "$root_dir"

  for command in screen docker curl npm go; do
    command -v "$command" >/dev/null 2>&1 || {
      echo "$command is required for host services mode" >&2
      exit 1
    }
  done

  bash ./scripts/stop-local-host-services.sh
  bash ./scripts/ensure-local-host-assets.sh
  bash ./scripts/run-local-infra.sh
  bash ./scripts/render-local-host-env.sh

  set -a
  # shellcheck disable=SC1091
  source .dev/host-services.env
  set +a

  cd services/retrieval-service
  RETRIEVAL_QDRANT_API_KEY_FILE="$root_dir/.dev/secrets/retrieval_qdrant_api_key" \
  RETRIEVAL_POSTGRES_DSN_FILE="$root_dir/.dev/host-secrets/retrieval_runtime_dsn" \
  go run ./cmd/qdrant_init
  cd "$root_dir"

  mkdir -p .dev
  if [[ ! -d ui/node_modules ]]; then
    npm --prefix ui ci
  fi

  ui_pid_file=.dev/ui.pid
  ui_log_file=.dev/ui.log
  if [[ -r "$ui_pid_file" ]] && kill -0 "$(cat "$ui_pid_file")" 2>/dev/null; then
    echo "UI already running (PID $(cat "$ui_pid_file"))."
  else
    rm -f "$ui_pid_file"
    (
      cd ui
      nohup npm run dev -- --host 127.0.0.1 >"$root_dir/$ui_log_file" 2>&1 &
      echo "$!" >"$root_dir/$ui_pid_file"
    )
  fi

  start_screen_service raglibrarian-identity identity-service "$root_dir/services/identity-service" go run ./cmd/main.go
  start_screen_service raglibrarian-catalog catalog-service "$root_dir/services/catalog-service" go run ./cmd/main.go
  start_screen_service raglibrarian-ingestion ingestion-service "$root_dir/services/ingestion-service" go run ./cmd/worker
  start_screen_service raglibrarian-layout-worker layout-worker "$root_dir/services/ingestion-service" env \
    INGESTION_MAX_EXTRACTED_BYTES=134217728 \
    go run ./cmd/layout_worker
  start_screen_service raglibrarian-retrieval retrieval-service "$root_dir/services/retrieval-service" go run ./cmd/server
  start_screen_service raglibrarian-retrieval-worker retrieval-worker "$root_dir/services/retrieval-service" env \
    RETRIEVAL_POSTGRES_DSN_FILE="$root_dir/.dev/host-secrets/retrieval_runtime_dsn" \
    RETRIEVAL_QDRANT_API_KEY_FILE="$root_dir/.dev/secrets/retrieval_qdrant_api_key" \
    go run ./cmd/worker
  start_screen_service raglibrarian-answer answer-service "$root_dir/services/answer-service" go run ./cmd/server
  start_screen_service raglibrarian-edge edge-api "$root_dir/services/edge-api" env \
    IDENTITY_GRPC_ADDR="$IDENTITY_GRPC_ADDR_CLIENT" \
    CATALOG_GRPC_ADDR="$CATALOG_GRPC_ADDR_CLIENT" \
    RETRIEVAL_GRPC_ADDR="$RETRIEVAL_GRPC_ADDR_CLIENT" \
    ANSWER_GRPC_ADDR="$ANSWER_GRPC_ADDR_CLIENT" \
    go run ./cmd/main.go

  for attempt in $(seq 1 "${M5_BACKEND_READINESS_TIMEOUT:-120}"); do
    if curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
      break
    fi
    if (( attempt % 10 == 0 )); then
      echo "Waiting for host backend readiness: ${attempt}s/${M5_BACKEND_READINESS_TIMEOUT:-120}s"
    fi
    sleep 1
  done

  if ! curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
    echo "Backend /readyz is not healthy yet. Inspect screen sessions with: screen -ls" >&2
    exit 1
  fi

  echo "Host services ready:"
  echo "  Backend     http://127.0.0.1:8080"
  echo "  UI          http://127.0.0.1:5173"
  echo "  Mailpit     http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
  echo "  Logs        $root_dir/_logs/{edge-api,identity-service,catalog-service,ingestion-service,layout-worker,retrieval-service,retrieval-worker,answer-service}/service.log"
  echo "  Sessions    screen -ls | grep raglibrarian-"
  echo "Stop with: bash ./scripts/stop-local.sh"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
