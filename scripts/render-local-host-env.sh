#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

resolve_host_run_as_uid() {
  printf '%s' "${HOST_RUN_AS_UID:-$(id -u)}"
}

resolve_host_run_as_gid() {
  printf '%s' "${HOST_RUN_AS_GID:-$(id -g)}"
}

resolve_host_path() {
  local path="$1"

  case "$path" in
    /*) printf '%s' "$path" ;;
    *) printf '%s/%s' "$root_dir" "$path" ;;
  esac
}

service_container_ip() {
  local service="$1"
  local container_id

  container_id="$(docker compose --profile raglibrarian ps -q "$service" 2>/dev/null || true)"
  [[ -n "$container_id" ]] || return 1
  docker inspect "$container_id" --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}'
}

http_url_reachable() {
  local url="$1"
  shift
  curl --fail --silent --show-error "$@" "$url" >/dev/null
}

tcp_endpoint_reachable() {
  local host="$1"
  local port="$2"

  : >/dev/tcp/"$host"/"$port"
}

resolve_service_host() {
  local service="$1"
  local host_port="$2"
  local container_port="$3"
  local probe_path="$4"
  shift 4
  local container_ip

  if http_url_reachable "http://127.0.0.1:${host_port}${probe_path}" "$@"; then
    printf '127.0.0.1:%s' "$host_port"
    return 0
  fi

  while IFS= read -r container_ip; do
    [[ -n "$container_ip" ]] || continue
    if http_url_reachable "http://${container_ip}:${container_port}${probe_path}" "$@"; then
      printf '%s:%s' "$container_ip" "$container_port"
      return 0
    fi
  done < <(service_container_ip "$service")

  return 1
}

resolve_service_tcp_host() {
  local service="$1"
  local host_port="$2"
  local container_port="$3"
  local container_ip

  if tcp_endpoint_reachable 127.0.0.1 "$host_port" 2>/dev/null; then
    printf '127.0.0.1:%s' "$host_port"
    return 0
  fi

  while IFS= read -r container_ip; do
    [[ -n "$container_ip" ]] || continue
    if tcp_endpoint_reachable "$container_ip" "$container_port" 2>/dev/null; then
      printf '%s:%s' "$container_ip" "$container_port"
      return 0
    fi
  done < <(service_container_ip "$service")

  return 1
}

main() {
cd "$root_dir"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

secret_dir="${SECRET_DIR:-.dev/secrets}"
cert_dir="${CERT_DIR:-.dev/certs}"
host_secret_dir="${HOST_SECRET_DIR:-.dev/host-secrets}"
host_asset_dir="${HOST_ASSET_DIR:-.dev/host-assets}"
env_file="${HOST_ENV_FILE:-.dev/host-services.env}"

bash ./scripts/ensure-local-host-secrets.sh "$secret_dir" "$host_secret_dir"
bash ./scripts/ensure-local-host-assets.sh

for command in pdfinfo pdftotext; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for host ingestion mode" >&2
    exit 1
  }
done

edge_verify_key="$(sed -n 's/^EDGE_VERIFY_KEY=//p' "$secret_dir/edge.env" | tail -n 1)"
[[ -n "$edge_verify_key" ]] || { echo "EDGE_VERIFY_KEY not found in $secret_dir/edge.env" >&2; exit 1; }

answer_provider_url="${ANSWER_LLM_BASE_URL:-}"
answer_provider_model="${ANSWER_LLM_MODEL:-}"
answer_provider_key="${ANSWER_LLM_API_KEY_PATH:-$secret_dir/answer_llm_api_key}"
if [[ -z "$answer_provider_url" || -z "$answer_provider_model" ]]; then
  echo "Host mode requires ANSWER_LLM_BASE_URL and ANSWER_LLM_MODEL in .env or the shell." >&2
  exit 1
fi
answer_provider_key="$(resolve_host_path "$answer_provider_key")"
[[ -r "$answer_provider_key" ]] || { echo "Answer provider key file is not readable: $answer_provider_key" >&2; exit 1; }
retrieval_summary_provider_url="${RETRIEVAL_SUMMARY_LLM_BASE_URL:-$answer_provider_url}"
retrieval_summary_provider_model="${RETRIEVAL_SUMMARY_LLM_MODEL:-$answer_provider_model}"
retrieval_summary_provider_max_output_tokens="${RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS:-64}"
retrieval_summary_provider_output_mode="${RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE:-json_or_plain}"

host_run_as_uid="$(resolve_host_run_as_uid)"
host_run_as_gid="$(resolve_host_run_as_gid)"
qdrant_api_key="$(tr -d '\r\n' < "$secret_dir/retrieval_qdrant_read_api_key")"
qdrant_host="$(resolve_service_host qdrant "${QDRANT_HTTP_PORT:-6333}" 6333 /collections -H "api-key: $qdrant_api_key")"
tei_host="$(resolve_service_host text-embeddings-inference "${M5_TEI_HTTP_PORT:-8082}" 8080 /health)"
minio_host="$(resolve_service_tcp_host minio "${MINIO_API_PORT:-9000}" 9000)"
mailpit_smtp_host="$(resolve_service_tcp_host mailpit 1025 1025)"
ingestion_epub_parser_path="$root_dir/$host_asset_dir/bin/epub-parser"
ingestion_pdfinfo_path="$(command -v pdfinfo)"
ingestion_pdftotext_path="$(command -v pdftotext)"
parser_sandbox_path="$root_dir/$host_asset_dir/bin/parser-sandbox"

mkdir -p "$(dirname "$env_file")"
cat <<EOF > "$env_file"
export GOCACHE="${GOCACHE:-/tmp/raglibrarian-go-cache}"
export RUN_AS_UID="$host_run_as_uid"
export RUN_AS_GID="$host_run_as_gid"
export INTERNAL_TLS_CA_FILE="$root_dir/$cert_dir/ca.crt"

export IDENTITY_GRPC_ADDR=":50051"
export IDENTITY_POSTGRES_DSN_FILE="$root_dir/$host_secret_dir/identity_runtime_dsn"
export IDENTITY_SIGNING_KEY_FILE="$root_dir/$secret_dir/identity_signing_key"
export IDENTITY_BOOTSTRAP_VERIFIER_FILE="$root_dir/$secret_dir/identity_bootstrap_verifier"
export IDENTITY_EMAIL_OUTBOX_KEY_FILE="$root_dir/$secret_dir/identity_email_outbox_key"
export IDENTITY_EMAIL_OUTBOX_KEY_ID="local-v1"
export IDENTITY_EMAIL_FINGERPRINT_KEY_FILE="$root_dir/$secret_dir/identity_email_fingerprint_key"
export IDENTITY_PASSWORD_RESET_HMAC_KEY_FILE="$root_dir/$secret_dir/identity_password_reset_hmac_key"
export IDENTITY_SMTP_PASSWORD_FILE="$root_dir/$secret_dir/identity_smtp_password"
export IDENTITY_SMTP_ADDR="$mailpit_smtp_host"
export IDENTITY_SMTP_SERVER_NAME="${mailpit_smtp_host%%:*}"
export IDENTITY_SMTP_FROM="no-reply@raglibrarian.local"
export IDENTITY_VERIFY_URL="http://localhost:5173/verify-email"
export IDENTITY_SMTP_STARTTLS="false"
export IDENTITY_TLS_KEY_FILE="$root_dir/$cert_dir/identity-service.key"
export IDENTITY_TLS_CERT_FILE="$root_dir/$cert_dir/identity-service.crt"
export IDENTITY_BCRYPT_CONCURRENCY="${IDENTITY_BCRYPT_CONCURRENCY:-4}"

export CATALOG_GRPC_ADDR=":50052"
export CATALOG_METRICS_ADDR="127.0.0.1:9092"
export CATALOG_POSTGRES_DSN_FILE="$root_dir/$host_secret_dir/catalog_runtime_dsn"
export CATALOG_MINIO_ENDPOINT="$minio_host"
export CATALOG_MINIO_INSECURE="true"
export CATALOG_MINIO_BUCKET="original-books"
export CATALOG_MINIO_ACCESS_KEY_FILE="$root_dir/$secret_dir/catalog_minio_access_key"
export CATALOG_MINIO_SECRET_KEY_FILE="$root_dir/$secret_dir/catalog_minio_secret_key"
export CATALOG_RABBITMQ_URI_FILE="$root_dir/$host_secret_dir/catalog_rabbitmq_uri"
export CATALOG_INGESTION_RABBITMQ_URI_FILE="$root_dir/$host_secret_dir/catalog_ingestion_rabbitmq_uri"
export CATALOG_RETRIEVAL_RABBITMQ_URI_FILE="$root_dir/$host_secret_dir/catalog_retrieval_rabbitmq_uri"
export CATALOG_PREVIEW_TIMEOUT="${CATALOG_PREVIEW_TIMEOUT:-5s}"
export CATALOG_TLS_CERT_FILE="$root_dir/$cert_dir/catalog-service.crt"
export CATALOG_TLS_KEY_FILE="$root_dir/$cert_dir/catalog-service.key"

export INGESTION_POSTGRES_DSN_FILE="$root_dir/$host_secret_dir/ingestion_runtime_dsn"
export INGESTION_RABBITMQ_URI_FILE="$root_dir/$host_secret_dir/ingestion_rabbitmq_uri"
export INGESTION_MINIO_ENDPOINT="$minio_host"
export INGESTION_MINIO_INSECURE="true"
export INGESTION_MINIO_ACCESS_KEY_FILE="$root_dir/$secret_dir/ingestion_minio_access_key"
export INGESTION_MINIO_SECRET_KEY_FILE="$root_dir/$secret_dir/ingestion_minio_secret_key"
export INGESTION_SOURCE_BUCKET="original-books"
export INGESTION_ARTIFACT_BUCKET="ingestion-artifacts"
export INGESTION_TOKENIZER_FILE="$root_dir/$host_asset_dir/cl100k_base.tiktoken"
export INGESTION_EPUB_PARSER_PATH="$ingestion_epub_parser_path"
export INGESTION_PDFINFO_PATH="$ingestion_pdfinfo_path"
export INGESTION_PDFTOTEXT_PATH="$ingestion_pdftotext_path"
export INGESTION_COMMAND_FAILURE_TRACE="1"
export INGESTION_DEBUG_DUMP_PDFTEXT_DIR="${INGESTION_DEBUG_DUMP_PDFTEXT_DIR:-}"
export PARSER_SANDBOX_PATH="$parser_sandbox_path"
export PARSER_SANDBOX_EPUB_PARSER_PATH="$ingestion_epub_parser_path"
export PARSER_SANDBOX_PDFINFO_PATH="$ingestion_pdfinfo_path"
export PARSER_SANDBOX_PDFTOTEXT_PATH="$ingestion_pdftotext_path"
export INGESTION_METRICS_ADDR="127.0.0.1:9093"
export INGESTION_WORK_CONCURRENCY="${INGESTION_WORK_CONCURRENCY:-1}"
export INGESTION_PARSER_SANDBOX_MEMORY_BYTES="${INGESTION_PARSER_SANDBOX_MEMORY_BYTES:-1610612736}"
export INGESTION_MAX_SOURCE_BYTES="26214400"
export INGESTION_MAX_EXTRACTED_BYTES="67108864"
export INGESTION_MAX_PAGES="1000"
export INGESTION_MAX_TEMP_BYTES="805306368"
export INGESTION_MEMORY_LIMIT_BYTES="2147483648"
export INGESTION_PROCESSING_TIMEOUT="12m30s"

export RETRIEVAL_INDEX_PROFILE="m8-bge-v1"
export RETRIEVAL_GRPC_ADDR="127.0.0.1:50054"
export RETRIEVAL_GRPC_ADDRESS="127.0.0.1:50054"
export RETRIEVAL_METRICS_ADDR="127.0.0.1:9094"
export RETRIEVAL_WORKER_METRICS_ADDR="127.0.0.1:9095"
export RETRIEVAL_POSTGRES_DSN_FILE="$root_dir/$host_secret_dir/retrieval_search_dsn"
export RETRIEVAL_QDRANT_URL="http://$qdrant_host"
export RETRIEVAL_QDRANT_API_KEY_FILE="$root_dir/$secret_dir/retrieval_qdrant_read_api_key"
export RETRIEVAL_TEI_URL="http://$tei_host"
export RETRIEVAL_SUMMARY_LLM_BASE_URL="$retrieval_summary_provider_url"
export RETRIEVAL_SUMMARY_LLM_MODEL="$retrieval_summary_provider_model"
export RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS="$retrieval_summary_provider_max_output_tokens"
export RETRIEVAL_SUMMARY_LLM_MAX_CALLS="${RETRIEVAL_SUMMARY_LLM_MAX_CALLS:-100}"
export RETRIEVAL_SUMMARY_LLM_OUTPUT_MODE="$retrieval_summary_provider_output_mode"
export RETRIEVAL_SUMMARY_LLM_API_KEY_FILE="$answer_provider_key"
export RETRIEVAL_SUMMARY_LLM_CA_FILE="$root_dir/$cert_dir/ca.crt"
export RETRIEVAL_SEARCH_TIMEOUT="${RETRIEVAL_SEARCH_TIMEOUT:-4m}"
export RETRIEVAL_DEPENDENCY_TIMEOUT="${RETRIEVAL_DEPENDENCY_TIMEOUT:-4m}"
export RETRIEVAL_SUMMARY_LLM_TIMEOUT="${RETRIEVAL_SUMMARY_LLM_TIMEOUT:-3m}"
export RETRIEVAL_TLS_CA_FILE="$root_dir/$cert_dir/ca.crt"
export RETRIEVAL_TLS_CERT_FILE="$root_dir/$cert_dir/retrieval-service.crt"
export RETRIEVAL_TLS_KEY_FILE="$root_dir/$cert_dir/retrieval-service.key"

export RETRIEVAL_PROCESSING_MODE="worker"
export RETRIEVAL_RABBITMQ_CONSUMER_URI_FILE="$root_dir/$host_secret_dir/retrieval_consumer_rabbitmq_uri"
export RETRIEVAL_RABBITMQ_PUBLISHER_URI_FILE="$root_dir/$host_secret_dir/retrieval_publisher_rabbitmq_uri"
export RETRIEVAL_MINIO_ENDPOINT="$minio_host"
export RETRIEVAL_MINIO_INSECURE="true"
export RETRIEVAL_MINIO_ACCESS_KEY_FILE="$root_dir/$secret_dir/retrieval_minio_access_key"
export RETRIEVAL_MINIO_SECRET_KEY_FILE="$root_dir/$secret_dir/retrieval_minio_secret_key"
export RETRIEVAL_ARTIFACT_BUCKET="ingestion-artifacts"
export RETRIEVAL_SERVERLESS_INVOCATION_TIMEOUT="${RETRIEVAL_SERVERLESS_INVOCATION_TIMEOUT:-10m}"
export RETRIEVAL_WORK_CONCURRENCY="${RETRIEVAL_WORK_CONCURRENCY:-1}"
export RETRIEVAL_TEI_LOG_RAW_RESPONSE="${RETRIEVAL_TEI_LOG_RAW_RESPONSE:-false}"
export RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES="${RETRIEVAL_TEI_LOG_RAW_RESPONSE_MAX_BYTES:-4096}"
export RETRIEVAL_WORKER_POSTGRES_DSN_FILE="$root_dir/$host_secret_dir/retrieval_runtime_dsn"
export RETRIEVAL_WORKER_QDRANT_API_KEY_FILE="$root_dir/$secret_dir/retrieval_qdrant_api_key"

export ANSWER_GRPC_ADDR="127.0.0.1:50055"
export ANSWER_METRICS_ADDR="127.0.0.1:9096"
export ANSWER_RETRIEVAL_GRPC_ADDR="127.0.0.1:50054"
export ANSWER_RETRIEVAL_TLS_SERVER_NAME="retrieval-service"
export ANSWER_LLM_BASE_URL="$answer_provider_url"
export ANSWER_LLM_MODEL="$answer_provider_model"
export ANSWER_LLM_API_KEY_FILE="$answer_provider_key"
export ANSWER_LLM_CA_FILE="${ANSWER_LLM_CA_FILE:-}"
export ANSWER_TLS_CA_FILE="$root_dir/$cert_dir/ca.crt"
export ANSWER_TLS_CERT_FILE="$root_dir/$cert_dir/answer-service.crt"
export ANSWER_TLS_KEY_FILE="$root_dir/$cert_dir/answer-service.key"

export QUERY_ADDR=":8080"
export IDENTITY_GRPC_ADDR_CLIENT="127.0.0.1:50051"
export CATALOG_GRPC_ADDR_CLIENT="127.0.0.1:50052"
export RETRIEVAL_GRPC_ADDR_CLIENT="127.0.0.1:50054"
export ANSWER_GRPC_ADDR_CLIENT="127.0.0.1:50055"
export EDGE_STATUS_RABBITMQ_URI_FILE="$root_dir/$host_secret_dir/edge_status_rabbitmq_uri_1"
export EDGE_STATUS_QUEUE="edge.book-status.local.1"
export EDGE_VERIFY_KEY="$edge_verify_key"
export EDGE_PUBLIC_ORIGIN="${EDGE_PUBLIC_ORIGIN:-http://localhost:5173}"
export EDGE_ENFORCE_BROWSER_ORIGIN="${EDGE_ENFORCE_BROWSER_ORIGIN:-true}"
export EDGE_INSECURE_REFRESH_COOKIE="${EDGE_INSECURE_REFRESH_COOKIE:-false}"
export EDGE_RETRIEVAL_READINESS_REQUIRED="${EDGE_RETRIEVAL_READINESS_REQUIRED:-false}"
export EDGE_ANSWER_DEADLINE="${EDGE_ANSWER_DEADLINE:-5m}"
export EDGE_RETRIEVAL_SEARCH_DEADLINE="${EDGE_RETRIEVAL_SEARCH_DEADLINE:-4m}"
export EDGE_CATALOG_PREVIEW_DEADLINE="${EDGE_CATALOG_PREVIEW_DEADLINE:-6s}"
export EDGE_TRUSTED_PROXY_CIDRS="${EDGE_TRUSTED_PROXY_CIDRS:-}"
export EDGE_TLS_CERT_FILE="$root_dir/$cert_dir/edge-api.crt"
export EDGE_TLS_KEY_FILE="$root_dir/$cert_dir/edge-api.key"
EOF

chmod 600 "$env_file"
echo "Host-mode environment rendered to $env_file"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
