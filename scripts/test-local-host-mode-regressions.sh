#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

source "$root_dir/scripts/render-local-host-env.sh"
source "$root_dir/scripts/ensure-local-host-secrets.sh"
source "$root_dir/scripts/run-local-host-services.sh"

RUN_AS_UID=65532
RUN_AS_GID=65532
unset HOST_RUN_AS_UID HOST_RUN_AS_GID

if [[ "$(resolve_host_run_as_uid)" != "$(id -u)" ]]; then
  echo "host env render reused container RUN_AS_UID instead of local uid" >&2
  exit 1
fi
if [[ "$(resolve_host_run_as_gid)" != "$(id -g)" ]]; then
  echo "host env render reused container RUN_AS_GID instead of local gid" >&2
  exit 1
fi

HOST_RUN_AS_UID=123
HOST_RUN_AS_GID=456
if [[ "$(resolve_host_run_as_uid)" != "123" ]]; then
  echo "host uid override was not applied" >&2
  exit 1
fi
if [[ "$(resolve_host_run_as_gid)" != "456" ]]; then
  echo "host gid override was not applied" >&2
  exit 1
fi

if [[ "$(resolve_host_path ".dev/secrets/answer_llm_api_key")" != "$root_dir/.dev/secrets/answer_llm_api_key" ]]; then
  echo "relative host paths were not resolved against repo root" >&2
  exit 1
fi
if [[ "$(resolve_host_path "/tmp/provider-key")" != "/tmp/provider-key" ]]; then
  echo "absolute host paths were unexpectedly rewritten" >&2
  exit 1
fi

curl() {
  local url="${!#}"
  case "$url" in
    http://127.0.0.1:6333/collections)
      return 7
      ;;
    http://172.22.0.3:6333/collections)
      return 0
      ;;
    http://127.0.0.1:8082/health)
      return 7
      ;;
    http://172.22.0.2:8080/health)
      return 0
      ;;
    *)
      return 7
      ;;
  esac
}

docker() {
  if [[ "$*" == "compose --profile raglibrarian ps -q qdrant" ]]; then
    printf 'qdrant-container\n'
    return 0
  fi
  if [[ "$*" == "compose --profile raglibrarian ps -q text-embeddings-inference" ]]; then
    printf 'tei-container\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "qdrant-container" ]]; then
    printf '172.22.0.3\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "tei-container" ]]; then
    printf '172.22.0.2\n'
    return 0
  fi
  return 1
}

if [[ "$(resolve_service_host qdrant 6333 6333 /collections -H "api-key: test-key")" != "172.22.0.3:6333" ]]; then
  echo "qdrant host fallback did not use the container bridge IP" >&2
  exit 1
fi
if [[ "$(resolve_service_host text-embeddings-inference 8082 8080 /health)" != "172.22.0.2:8080" ]]; then
  echo "tei host fallback did not use the container bridge IP" >&2
  exit 1
fi
tcp_endpoint_reachable() {
  local host="$1"
  local port="$2"
  [[ "$host:$port" == "172.21.0.2:9000" || "$host:$port" == "172.18.0.2:1025" ]]
}
docker() {
  if [[ "$*" == "compose --profile raglibrarian ps -q qdrant" ]]; then
    printf 'qdrant-container\n'
    return 0
  fi
  if [[ "$*" == "compose --profile raglibrarian ps -q text-embeddings-inference" ]]; then
    printf 'tei-container\n'
    return 0
  fi
  if [[ "$*" == "compose --profile raglibrarian ps -q minio" ]]; then
    printf 'minio-container\n'
    return 0
  fi
  if [[ "$*" == "compose --profile raglibrarian ps -q mailpit" ]]; then
    printf 'mailpit-container\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "qdrant-container" ]]; then
    printf '172.22.0.3\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "tei-container" ]]; then
    printf '172.22.0.2\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "minio-container" ]]; then
    printf '172.21.0.2\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "mailpit-container" ]]; then
    printf '172.18.0.2\n'
    return 0
  fi
  return 1
}
if [[ "$(resolve_service_tcp_host minio 9000 9000)" != "172.21.0.2:9000" ]]; then
  echo "minio host fallback did not use the container bridge IP" >&2
  exit 1
fi
if [[ "$(resolve_service_tcp_host mailpit 1025 1025)" != "172.18.0.2:1025" ]]; then
  echo "mailpit smtp fallback did not use the container bridge IP" >&2
  exit 1
fi

source "$root_dir/scripts/run-local-infra.sh"

curl() {
  local url="${!#}"
  case "$url" in
    http://127.0.0.1:6333/collections)
      return 7
      ;;
    http://172.22.0.3:6333/collections)
      return 0
      ;;
    *)
      return 7
      ;;
  esac
}

docker() {
  if [[ "$*" == "compose --profile raglibrarian ps -q qdrant" ]]; then
    printf 'qdrant-container\n'
    return 0
  fi
  if [[ "$1" == "inspect" && "$2" == "qdrant-container" ]]; then
    printf '172.22.0.3\n'
    return 0
  fi
  return 1
}

if [[ "$(resolve_service_http_base_url qdrant 6333 6333 /collections -H "api-key: test-key")" != "http://172.22.0.3:6333" ]]; then
  echo "qdrant infra fallback did not resolve the container bridge base URL" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

host_dir="$tmp_dir/host-secrets"
mkdir -p "$host_dir"
chmod 700 "$host_dir"
printf 'stale-value\n' > "$host_dir/identity_runtime_dsn"
chmod 400 "$host_dir/identity_runtime_dsn"

ensure_file identity_runtime_dsn "fresh-value"

if [[ "$(cat "$host_dir/identity_runtime_dsn")" != "fresh-value" ]]; then
  echo "host secret overlay was not refreshed when stale contents were present" >&2
  exit 1
fi

host_asset_dir=".dev/host-assets"
if [[ "$root_dir/$host_asset_dir/bin/parser-sandbox" != "$root_dir/.dev/host-assets/bin/parser-sandbox" ]]; then
  echo "host asset parser sandbox path assumption changed unexpectedly" >&2
  exit 1
fi

if ! grep -Fq 'export PARSER_SANDBOX_PATH="$parser_sandbox_path"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not export the parser sandbox path" >&2
  exit 1
fi
if ! grep -Fq 'export PARSER_SANDBOX_EPUB_PARSER_PATH="$ingestion_epub_parser_path"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not export the EPUB parser allowlist path" >&2
  exit 1
fi
if ! grep -Fq 'retrieval_summary_provider_url="${RETRIEVAL_SUMMARY_LLM_BASE_URL:-$answer_provider_url}"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not default retrieval summary URL from answer provider config" >&2
  exit 1
fi
if ! grep -Fq 'retrieval_summary_provider_model="${RETRIEVAL_SUMMARY_LLM_MODEL:-$answer_provider_model}"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not default retrieval summary model from answer provider config" >&2
  exit 1
fi
if ! grep -Fq 'retrieval_summary_provider_max_output_tokens="${RETRIEVAL_SUMMARY_LLM_MAX_OUTPUT_TOKENS:-64}"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not default retrieval summary max output tokens" >&2
  exit 1
fi
if ! grep -Fq 'export RETRIEVAL_SUMMARY_LLM_MAX_CALLS="${RETRIEVAL_SUMMARY_LLM_MAX_CALLS:-100}"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not export retrieval summary max calls" >&2
  exit 1
fi
if ! grep -Fq 'export EDGE_CATALOG_PREVIEW_DEADLINE="${EDGE_CATALOG_PREVIEW_DEADLINE:-5s}"' "$root_dir/scripts/render-local-host-env.sh"; then
  echo "host env renderer does not export the catalog preview deadline" >&2
  exit 1
fi
if ! grep -Fq 'bash ./scripts/stop-local-host-services.sh' "$root_dir/scripts/run-local-host-services.sh"; then
  echo "host services launcher does not stop stale screen sessions before restart" >&2
  exit 1
fi

screen() {
  if [[ "${1:-}" == "-wipe" ]]; then
    return 0
  fi
  if [[ "${1:-}" == "-ls" ]]; then
    cat <<'EOF'
There are screens on:
	1214504.raglibrarian-retrieval-worker	(Dead ???)
	1193454.raglibrarian-retrieval	(Detached)
	1193600.raglibrarian-edge	(Attached)
3 Sockets in /run/screen/S-test.
EOF
    return 0
  fi
  return 1
}

if screen_session_exists raglibrarian-retrieval-worker; then
  echo "dead screen socket was treated as an active session" >&2
  exit 1
fi
if ! screen_session_exists raglibrarian-retrieval; then
  echo "detached screen session was not treated as active" >&2
  exit 1
fi
if ! screen_session_exists raglibrarian-edge; then
  echo "attached screen session was not treated as active" >&2
  exit 1
fi

echo "local host-mode regressions passed"
