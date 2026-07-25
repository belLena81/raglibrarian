#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

service_container_ip() {
  local service="$1"
  local container_id

  container_id="$(docker compose --profile raglibrarian ps -q "$service" 2>/dev/null || true)"
  [[ -n "$container_id" ]] || return 1
  docker inspect "$container_id" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'
}

http_url_reachable() {
  local url="$1"
  shift
  curl --fail --silent --show-error "$@" "$url" >/dev/null
}

resolve_service_http_base_url() {
  local service="$1"
  local host_port="$2"
  local container_port="$3"
  local probe_path="$4"
  shift 4
  local container_ip

  if http_url_reachable "http://127.0.0.1:${host_port}${probe_path}" "$@"; then
    printf 'http://127.0.0.1:%s' "$host_port"
    return 0
  fi

  container_ip="$(service_container_ip "$service")"
  [[ -n "$container_ip" ]] || return 1
  if http_url_reachable "http://${container_ip}:${container_port}${probe_path}" "$@"; then
    printf 'http://%s:%s' "$container_ip" "$container_port"
    return 0
  fi

  return 1
}

qdrant_collections_ready() {
  local base_url="$1"
  local api_key="$2"

  http_url_reachable "${base_url}/collections" -H "api-key: $api_key"
}

wait_for_qdrant_http_ready() {
  local timeout="${1:-300}"
  local api_key="$2"
  local elapsed

  for elapsed in $(seq 1 "$timeout"); do
    if qdrant_base_url="$(resolve_service_http_base_url qdrant "${QDRANT_HTTP_PORT:-6333}" 6333 /collections -H "api-key: $api_key")"; then
      printf '%s\n' "$qdrant_base_url"
      return 0
    fi
    if (( elapsed % 10 == 0 )); then
      echo "Waiting for Qdrant HTTP readiness: ${elapsed}s/${timeout}s"
    fi
    sleep 1
  done

  echo "Timed out waiting for Qdrant HTTP readiness after ${timeout}s." >&2
  return 1
}

main() {
cd "$root_dir"

for command in docker curl; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for host-mode infra" >&2
    exit 1
  }
done
docker compose version >/dev/null

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "Created .env from .env.example. Review loopback ports if needed."
fi

set -a
# shellcheck disable=SC1091
source .env
set +a

secret_dir="${SECRET_DIR:-.dev/secrets}"
cert_dir="${CERT_DIR:-.dev/certs}"
model_dir="${M5_MODEL_DIR:-.dev/models/m5-jina-code-v1}"
fallback_root="${TMPDIR:-/tmp}/raglibrarian-model-cache-$(id -u)"
fallback_dir="$fallback_root/m5-jina-code-v1"

has_bootstrapped_m5_model_cache() {
  local path="$1"

  [[ -f "$path/.revision" ]] || return 1
  [[ -f "$path/config.json" ]] || return 1
  [[ -f "$path/tokenizer.json" ]] || return 1
  [[ -f "$path/onnx/model.onnx" ]] || return 1
  find "$path" -maxdepth 1 -type f -name '*.safetensors' -print -quit | grep -q . || return 1
}

if [[ -z "${M5_MODEL_DIR:-}" || ! -d "${model_dir}" || ! -w "$(dirname "$model_dir")" ]]; then
  model_parent="$(dirname "$model_dir")"
  if [[ ! -d "$model_parent" ]]; then
    if ! mkdir -p "$model_parent"; then
      model_dir="$fallback_dir"
      mkdir -p "$fallback_root"
      chmod 700 "$fallback_root"
      echo "Unable to create default model cache parent $model_parent. Using fallback model dir: $model_dir"
    fi
  fi
  if [[ -d "$model_parent" && ! -w "$model_parent" ]]; then
    model_dir="$fallback_dir"
    mkdir -p "$fallback_root"
    chmod 700 "$fallback_root"
    echo "Default model cache parent $model_parent is not writable. Using fallback model dir: $model_dir"
  fi
fi

if [[ "$model_dir" != "$fallback_dir" ]] && ! has_bootstrapped_m5_model_cache "$model_dir" && has_bootstrapped_m5_model_cache "$fallback_dir"; then
  model_dir="$fallback_dir"
  echo "Resolved model cache directory with required files: $model_dir"
fi

export M5_MODEL_DIR="$model_dir"

if ! has_bootstrapped_m5_model_cache "$M5_MODEL_DIR"; then
  echo "Model cache is incomplete at $M5_MODEL_DIR. It will be repaired by bootstrap-m5-model.sh."
fi

if [[ ! -r "$secret_dir/identity_runtime_dsn" ]]; then
  if [[ -d "$secret_dir" ]] && [[ -n "$(find "$secret_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Incomplete local secrets in $secret_dir; do not overwrite them automatically." >&2
    exit 1
  fi
  make dev-secrets
elif [[ ! -r "$secret_dir/catalog_minio_access_key" ]]; then
  make dev-secrets-m3
fi

if [[ ! -r "$secret_dir/identity_password_reset_hmac_key" ]]; then
  command -v openssl >/dev/null 2>&1 || {
    echo "openssl is required to create the password-reset development secret" >&2
    exit 1
  }
  openssl rand -hex 32 > "$secret_dir/identity_password_reset_hmac_key"
  chmod 400 "$secret_dir/identity_password_reset_hmac_key"
fi

bash ./scripts/ensure-m4-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m5-dev-secrets.sh "$secret_dir"
bash ./scripts/ensure-m6-answer-provider-key.sh "$secret_dir"

if [[ ! -r "$secret_dir/identity_bootstrap_verifier" ]]; then
  echo "Creating a local admin bootstrap verifier (interactive)."
  echo "The one-time bootstrap code is printed below; store it now."
  make bootstrap-verifier
fi

if [[ ! -r "$cert_dir/ca.crt" ]]; then
  if [[ -d "$cert_dir" ]] && [[ -n "$(find "$cert_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "Incomplete local certificates in $cert_dir; do not overwrite them automatically." >&2
    exit 1
  fi
  make dev-certs
fi
bash ./scripts/ensure-m6-dev-cert.sh "$cert_dir"

mkdir -p "$(dirname "$M5_MODEL_DIR")"
if ! has_bootstrapped_m5_model_cache "$M5_MODEL_DIR"; then
  M5_REPAIR_MODEL_CACHE="${M5_REPAIR_MODEL_CACHE:-true}" \
    bash ./scripts/bootstrap-m5-model.sh
else
  echo "Using existing M5 model cache: $M5_MODEL_DIR"
fi

M5_MODEL_DIR="$M5_MODEL_DIR" make compose-config

. "$root_dir/scripts/run-local-lib.sh"

compose_wait_timeout="${M5_COMPOSE_WAIT_TIMEOUT:-300}"

M5_MODEL_DIR="$M5_MODEL_DIR" compose_cmd up -d \
  postgres \
  db-bootstrap \
  minio \
  minio-bootstrap \
  rabbitmq \
  qdrant \
  text-embeddings-inference \
  mailpit

wait_for_service_completed_successfully db-bootstrap "$compose_wait_timeout" "db-bootstrap"
wait_for_service_healthy minio "$compose_wait_timeout" "minio"
wait_for_service_completed_successfully minio-bootstrap "$compose_wait_timeout" "minio-bootstrap"
wait_for_service_healthy rabbitmq "$compose_wait_timeout" "rabbitmq"
wait_for_service_healthy qdrant "$compose_wait_timeout" "qdrant"
wait_for_service_healthy text-embeddings-inference "$compose_wait_timeout" "text-embeddings-inference"

qdrant_api_key="$(cat "$secret_dir/retrieval_qdrant_api_key")"
qdrant_base_url="$(resolve_service_http_base_url qdrant "${QDRANT_HTTP_PORT:-6333}" 6333 /collections -H "api-key: $qdrant_api_key" || true)"
tei_base_url="$(resolve_service_http_base_url text-embeddings-inference "${M5_TEI_HTTP_PORT:-8082}" 8080 /health || true)"
if [[ -z "$qdrant_base_url" || -z "$tei_base_url" ]]; then
  echo "Refreshing Qdrant/TEI containers to apply host port bindings."
  M5_MODEL_DIR="$M5_MODEL_DIR" compose_cmd up -d --force-recreate --no-deps qdrant text-embeddings-inference
  wait_for_service_healthy qdrant "$compose_wait_timeout" "qdrant"
  wait_for_service_healthy text-embeddings-inference "$compose_wait_timeout" "text-embeddings-inference"
  qdrant_base_url="$(wait_for_qdrant_http_ready "$compose_wait_timeout" "$qdrant_api_key")"
  tei_base_url="$(resolve_service_http_base_url text-embeddings-inference "${M5_TEI_HTTP_PORT:-8082}" 8080 /health)"
fi

echo "Infra ready:"
echo "  PostgreSQL  127.0.0.1:${POSTGRES_PORT:-5432}"
echo "  RabbitMQ    127.0.0.1:${RABBITMQ_AMQP_PORT:-5672}"
echo "  MinIO       127.0.0.1:${MINIO_API_PORT:-9000}"
echo "  Qdrant      ${qdrant_base_url#http://}"
echo "  TEI         ${tei_base_url#http://}"
echo "  Mailpit     127.0.0.1:${MAILPIT_UI_PORT:-8025}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
