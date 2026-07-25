#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

regenerate_bootstrap_code=false
case "${1:-}" in
  "")
    ;;
  --regenerate-bootstrap-code)
    regenerate_bootstrap_code=true
    ;;
  *)
    echo "usage: $0 [--regenerate-bootstrap-code]" >&2
    exit 1
    ;;
esac

for command in docker npm curl; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for a local run" >&2
    exit 1
  }
done
docker compose version >/dev/null

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "Created .env from .env.example. Review loopback ports if needed."
fi

secret_dir="${SECRET_DIR:-.dev/secrets}"
cert_dir="${CERT_DIR:-.dev/certs}"
model_dir="${M5_MODEL_DIR:-.dev/models/m5-jina-code-v1}"
bootstrap_verifier_file="$secret_dir/identity_bootstrap_verifier"
bootstrap_status="unknown"
fallback_root="${TMPDIR:-/tmp}/raglibrarian-model-cache-$(id -u)"
fallback_dir="$fallback_root/m5-jina-code-v1"

is_valid_m5_model_cache() {
  local path="$1"

  [[ -f "$path/.revision" ]] || return 1
  [[ -f "$path/config.json" ]] || return 1
  [[ -f "$path/tokenizer.json" ]] || return 1
  [[ -f "$path/onnx/model.onnx" ]] || return 1
}

if [[ -z "${M5_MODEL_DIR:-}" || ! -d "${model_dir}" || ! -w "$(dirname "$model_dir")" ]]; then
  model_parent="$(dirname "$model_dir")"
  if [[ ! -d "$model_parent" ]]; then
    if ! mkdir -p "$model_parent"; then
      rm -rf "$fallback_root"
      mkdir -p "$fallback_root"
      chmod 700 "$fallback_root"
      model_dir="$fallback_dir"
      echo "Unable to create default model cache parent $model_parent. Using fallback model dir: $model_dir"
    fi
  fi
  if [[ -d "$model_parent" && ! -w "$model_parent" ]]; then
    rm -rf "$fallback_root"
    mkdir -p "$fallback_root"
    chmod 700 "$fallback_root"
    model_dir="$fallback_dir"
    echo "Default model cache parent $model_parent is not writable. Using fallback model dir: $model_dir"
  fi
fi

if ! is_valid_m5_model_cache "$model_dir" && is_valid_m5_model_cache "$fallback_dir"; then
  model_dir="$fallback_dir"
  echo "Resolved model cache directory with required files: $model_dir"
fi

export M5_MODEL_DIR="$model_dir"

if ! is_valid_m5_model_cache "$M5_MODEL_DIR"; then
  echo "Model cache is incomplete at $M5_MODEL_DIR. It will be repaired by bootstrap-m5-model.sh."
fi

if [[ ! -r "$secret_dir/identity_runtime_dsn" ]]; then
  if [[ -d "$secret_dir" ]] && [[ -n "$(find "$secret_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Incomplete local secrets in $secret_dir; do not overwrite them automatically." >&2
    echo "Remove the directory only if you intend a full local reset, then rerun this script." >&2
    exit 1
  fi
  make dev-secrets
elif [[ ! -r "$secret_dir/catalog_minio_access_key" ]]; then
  make dev-secrets-m3
fi

# Additive development secrets must not force a destructive local reset. This
# key is independent of existing credentials and is created only when absent.
if [[ ! -r "$secret_dir/identity_password_reset_hmac_key" ]]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is required to create the password-reset development secret" >&2
    exit 1
  }
  umask 077
  openssl rand -hex 32 > "$secret_dir/identity_password_reset_hmac_key"
  chmod 400 "$secret_dir/identity_password_reset_hmac_key"
  echo "Created missing password-reset development secret."
fi

bash ./scripts/ensure-m4-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m5-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m6-answer-provider-key.sh "$secret_dir"
export M5_TEI_MEMORY_LIMIT="${M5_TEI_MEMORY_LIMIT:-16g}"
export M5_TEI_CPU_LIMIT="${M5_TEI_CPU_LIMIT:-4.0}"
export M5_TEI_MAX_CLIENT_BATCH_SIZE="${M5_TEI_MAX_CLIENT_BATCH_SIZE:-16}"
export M5_COMPOSE_WAIT_TIMEOUT="${M5_COMPOSE_WAIT_TIMEOUT:-300}"
export M5_BACKEND_READINESS_TIMEOUT="${M5_BACKEND_READINESS_TIMEOUT:-900}"
export M5_SERVICE_STARTUP_DELAY="${M5_SERVICE_STARTUP_DELAY:-5}"

if ! [[ "${M5_SERVICE_STARTUP_DELAY}" =~ ^[0-9]+$ ]]; then
  echo "Invalid M5_SERVICE_STARTUP_DELAY value: ${M5_SERVICE_STARTUP_DELAY}. It must be a non-negative integer."
  exit 1
fi

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
  echo "Creating a local admin bootstrap verifier (interactive)."
  echo "The one-time bootstrap code is printed below; store it now."
  make bootstrap-verifier
  bootstrap_status="created"
  echo "Use the code only with /setup/admin, then remove the verifier after setup."
elif [[ "$regenerate_bootstrap_code" == false ]]; then
  bootstrap_status="existing"
  echo "Admin bootstrap verifier already exists in $bootstrap_verifier_file (no new code printed)."
elif [[ "$regenerate_bootstrap_code" == true ]]; then
  bootstrap_status="regenerate"
  echo "Admin bootstrap verifier will be replaced after setup is confirmed available."
fi

if [[ ! -r "$cert_dir/ca.crt" ]]; then
	if [[ -d "$cert_dir" ]] && [[ -n "$(find "$cert_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Incomplete local certificates in $cert_dir; do not overwrite them automatically." >&2
    echo "Remove the directory only if you intend a full local reset, then rerun this script." >&2
    exit 1
  fi
  make dev-certs
fi
bash ./scripts/ensure-m6-dev-cert.sh "$cert_dir"

echo "Using M5 model directory: $M5_MODEL_DIR"
# Model acquisition is explicit and revision-pinned. TEI runs offline and will
# not fetch weights on its serving path.
# Allow one-time recovery from incomplete historical model caches by default.
if [[ ! -d "$model_dir" ]]; then
  mkdir -p "$(dirname "$model_dir")"
fi
M5_REPAIR_MODEL_CACHE="${M5_REPAIR_MODEL_CACHE:-true}" \
	bash ./scripts/bootstrap-m5-model.sh

M5_MODEL_DIR="$M5_MODEL_DIR" make compose-config
compose_exit=0
compose_wait_timeout="${M5_COMPOSE_WAIT_TIMEOUT:-300}"
compose_wait_mode="${M5_COMPOSE_WAIT_MODE:-manual}"
compose_wait_mode="${compose_wait_mode,,}"
compose_wait_mode="${compose_wait_mode//[[:space:]]/}"
case "$compose_wait_mode" in
  manual|compose)
    :
    ;;
  "")
    compose_wait_mode="manual"
    ;;
  *)
    echo "Unknown M5_COMPOSE_WAIT_MODE=${compose_wait_mode}; defaulting to manual."
    compose_wait_mode="manual"
    ;;
esac
echo "Using compose wait mode: $compose_wait_mode"

print_tei_debug() {
  local tei_container_id
  tei_container_id="$(docker compose --profile m5 --profile m6 ps -q text-embeddings-inference || true)"
  if [[ -z "$tei_container_id" ]]; then
    echo "No text-embeddings-inference container was found yet."
    return
  fi
  echo "---- text-embeddings-inference runtime state ----"
  docker inspect --format 'id={{.Id}} state={{.State.Status}} exit={{.State.ExitCode}} oom_killed={{.State.OOMKilled}} error={{.State.Error}} started={{.State.StartedAt}} finished={{.State.FinishedAt}}' "$tei_container_id" || true
  tei_health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$tei_container_id" 2>/dev/null || true)"
  echo "TEI health status: ${tei_health:-unknown}"
  echo "---- recent TEI health output ----"
  docker inspect --format '{{with .State.Health}}{{range .Log}}{{printf "%s %s\n" .End .Output}}{{end}}{{end}}' "$tei_container_id" | tail -n 8 || true
}

report_dependency_status() {
  echo "---- compose status ----"
  docker compose --profile m5 --profile m6 ps || true
  echo "---- tei state ----"
  docker compose --profile m5 --profile m6 ps text-embeddings-inference || true
  echo "---- edge-api state ----"
  docker compose --profile m5 --profile m6 ps edge-api || true
  if docker compose --profile m5 --profile m6 ps text-embeddings-inference | tail -n +3 | grep -q "health: starting"; then
    echo "text-embeddings-inference is still warming up."
  fi
  if docker compose --profile m5 --profile m6 ps text-embeddings-inference | tail -n +3 | grep -Eq "Restarting|Restarted|Exit|Exited|unhealthy|starting"; then
    echo "Text-embeddings-inference appears unhealthy/restarting. Last logs:"
    docker compose --profile m5 --profile m6 logs --no-color --tail 160 text-embeddings-inference 2>/dev/null || true
    print_tei_debug
  fi
}

check_tei_failure_exit() {
  local tei_container_id tei_status tei_exit_code

  tei_container_id="$(docker compose --profile m5 --profile m6 ps -q text-embeddings-inference || true)"
  if [[ -z "$tei_container_id" ]]; then
    return 1
  fi

  tei_status="$(docker inspect --format '{{.State.Status}}' "$tei_container_id" 2>/dev/null || true)"
  tei_exit_code="$(docker inspect --format '{{.State.ExitCode}}' "$tei_container_id" 2>/dev/null || true)"
  if [[ "$tei_status" == "exited" && "${tei_exit_code:-0}" != "0" ]]; then
    echo "text-embeddings-inference exited during startup (exit code ${tei_exit_code})."
    echo "Recent logs:"
    docker compose --profile m5 --profile m6 logs --no-color --tail 200 text-embeddings-inference 2>/dev/null || true
    return 0
  fi
  return 1
}

set +e
if [[ "$compose_wait_mode" == "compose" ]]; then
  M5_MODEL_DIR="$M5_MODEL_DIR" docker compose --profile m5 --profile m6 up -d --build --wait --wait-timeout "$compose_wait_timeout"
  compose_exit=$?
else
  startup_services=(
    text-embeddings-inference
    qdrant
    rabbitmq
    minio
    mailpit
    postgres
    identity-db-bootstrap
    identity-migrate
    catalog-migrate
    ingestion-migrate
    retrieval-migrate
    minio-bootstrap
    catalog-service
    identity-service
    ingestion-service
    retrieval-qdrant-init
    retrieval-service
    retrieval-worker
    answer-service
    edge-api
  )

  M5_MODEL_DIR="$M5_MODEL_DIR" docker compose --profile m5 --profile m6 build
  compose_exit=0
  for startup_service in "${startup_services[@]}"; do
    compose_args=(up -d)
    if [[ "$startup_service" == "answer-service" ]]; then
      compose_args+=(--no-deps)
    fi
    compose_args+=("$startup_service")
    M5_MODEL_DIR="$M5_MODEL_DIR" docker compose --profile m5 --profile m6 "${compose_args[@]}"
    if [[ $? -ne 0 ]]; then
      compose_exit=1
      break
    fi
    if (( M5_SERVICE_STARTUP_DELAY > 0 )); then
      sleep "$M5_SERVICE_STARTUP_DELAY"
    fi
  done
fi
set -e

if (( compose_exit != 0 )); then
  echo "Compose startup failed with exit code $compose_exit."
  echo "Service status:"
  docker compose --profile m5 --profile m6 ps
  echo "Recent logs from text-embeddings-inference (if running):"
  docker compose --profile m5 --profile m6 logs --no-color --tail 200 text-embeddings-inference 2>/dev/null || true
  print_tei_debug
  echo "Recent logs from retrieval-service (if running):"
  docker compose --profile m5 --profile m6 logs --no-color --tail 200 retrieval-service 2>/dev/null || true
  echo "Recent logs from edge-api (if running):"
  docker compose --profile m5 --profile m6 logs --no-color --tail 120 edge-api 2>/dev/null || true
  if [[ "${M5_STRICT_COMPOSE_WAIT:-false}" == "true" ]]; then
    exit 1
  fi
  echo "Compose startup finished with warning state; continuing with readiness checks."
fi

max_wait="${M5_BACKEND_READINESS_TIMEOUT}"
print_readyz_snapshot() {
  if ! readiness_body="$(curl --silent --show-error http://127.0.0.1:8080/readyz || true)"; then
    return 0
  fi
  if [[ -n "${readiness_body:-}" ]]; then
    echo "Latest /readyz response: $readiness_body"
  fi
}

for attempt in $(seq 1 "$max_wait"); do
  if curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
    break
  fi
  if check_tei_failure_exit; then
    compose_exit=1
    break
  fi
  if (( attempt == 1 || attempt % 10 == 0 )); then
    echo "Waiting for backend readiness: ${attempt}s/${max_wait}s"
    report_dependency_status
    print_tei_debug
    print_readyz_snapshot
  fi
  sleep 1
  if (( attempt == max_wait )); then
    echo "Backend readiness timed out after ${max_wait}s."
    print_readyz_snapshot
    echo "Service status:"
    docker compose --profile m5 --profile m6 ps
    echo "Recent logs from text-embeddings-inference:"
    docker compose --profile m5 --profile m6 logs --no-color --tail 120 text-embeddings-inference 2>/dev/null || true
    echo "Recent logs from edge-api:"
    docker compose --profile m5 --profile m6 logs --no-color --tail 120 edge-api 2>/dev/null || true
    echo "Recent logs from retrieval-service:"
    docker compose --profile m5 --profile m6 logs --no-color --tail 120 retrieval-service 2>/dev/null || true
    break
  fi
done

if ! curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
  echo "Backend /readyz is not healthy yet."
  print_readyz_snapshot
  print_tei_debug
  compose_exit=1
else
  compose_exit=0
fi

if (( compose_exit != 0 )); then
  echo "---- failed/restarting services ----"
  docker compose --profile m5 --profile m6 ps | awk 'NR>2 && $0 !~ /(Up|Exit 0|Created|running)/ {print}'
  echo "Service status:"
  docker compose --profile m5 --profile m6 ps
  echo "Recent logs from text-embeddings-inference (if running):"
  docker compose --profile m5 --profile m6 logs --no-color --tail 120 text-embeddings-inference 2>/dev/null || true
  echo "Recent logs from edge-api (if running):"
  docker compose --profile m5 --profile m6 logs --no-color --tail 120 edge-api 2>/dev/null || true
  echo "Continuing; check logs for failing dependency and fix before retry."
else
  echo "Backend ready: http://127.0.0.1:8080"
fi

log_pid_dir=.dev/log-pids
mkdir -p "$log_pid_dir"
for service in edge-api identity-service catalog-service ingestion-service retrieval-service retrieval-worker answer-service; do
  service_log_dir="_logs/$service"
  service_log_file="$service_log_dir/service.log"
  service_pid_file="$log_pid_dir/$service.pid"
  mkdir -p "$service_log_dir"

  if [[ -r "$service_pid_file" ]] && kill -0 "$(cat "$service_pid_file")" 2>/dev/null; then
    continue
  fi

  rm -f "$service_pid_file"
  nohup docker compose --profile m5 --profile m6 logs --no-color --follow "$service" >>"$service_log_file" 2>&1 &
  echo "$!" >"$service_pid_file"
done

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

wait_for_backend() {
  for _ in {1..30}; do
    if curl --fail --silent --show-error http://127.0.0.1:8080/readyz >/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

if [[ "$regenerate_bootstrap_code" == true ]]; then
  setup_status="$(curl --fail --silent --show-error http://127.0.0.1:8080/setup/status)"
  if [[ "$setup_status" != '{"required":true}' ]]; then
    echo "Bootstrap is unavailable because an administrator is already configured." >&2
    exit 1
  fi
  rm -f "$secret_dir/identity_bootstrap_verifier"
  echo "Creating a replacement local admin bootstrap verifier (interactive)."
  echo "The one-time bootstrap code is printed below; store it now."
  make bootstrap-verifier
  docker compose up -d --force-recreate identity-service
  wait_for_backend
  bootstrap_status="created"
fi

if (( compose_exit != 0 )); then
  echo "Backend is not ready yet. If this persists, run:"
  echo "  docker compose --profile m5 --profile m6 ps text-embeddings-inference"
  echo "  docker compose --profile m5 --profile m6 logs -f --tail 200 text-embeddings-inference"
  echo "You can also run: curl -i http://127.0.0.1:8080/readyz"
else
  echo "Backend ready: http://127.0.0.1:8080"
fi
echo "UI:            http://127.0.0.1:5173"
echo "Mailpit:       http://127.0.0.1:${MAILPIT_UI_PORT:-8025}"
echo "Setup URL:     http://127.0.0.1:5173/setup"
echo "Setup API status endpoint:"
echo "  GET  http://127.0.0.1:8080/setup/status"
echo "  POST http://127.0.0.1:8080/setup/admin"
if [[ "$bootstrap_status" == "created" ]]; then
  echo "Bootstrap verifier file: $bootstrap_verifier_file"
fi
if [[ "$bootstrap_status" == "existing" ]]; then
  echo "Bootstrap verifier file (existing): $bootstrap_verifier_file"
fi
echo "Backend logs:  $root_dir/_logs/{edge-api,identity-service,catalog-service,ingestion-service,retrieval-service,retrieval-worker,answer-service}/service.log"
echo "Stop backend with: docker compose down --profile m5 --profile m6"
echo "Stop local stack:  bash ./scripts/stop-local.sh"
