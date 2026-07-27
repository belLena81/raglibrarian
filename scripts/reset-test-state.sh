#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

compose_cmd=(docker compose)
if [[ -n "${TEST_COMPOSE_FILES:-}" ]]; then
  read -r -a compose_files <<< "${TEST_COMPOSE_FILES}"
  compose_cmd+=("${compose_files[@]}")
fi
compose_cmd+=(--profile raglibrarian)

require_service() {
  local service="$1"
  local container_id

  container_id="$("${compose_cmd[@]}" ps -q "$service" 2>/dev/null || true)"
  [[ -n "$container_id" ]] || {
    echo "test reset requires the $service compose service to be running" >&2
    exit 1
  }
  printf '%s' "$container_id"
}

truncate_database() {
  local database="$1"
  case "$database" in
    identity|catalog|ingestion|retrieval) ;;
    *)
      echo "refusing to reset non-test database: $database" >&2
      exit 1
      ;;
  esac

  "${compose_cmd[@]}" exec -T postgres sh -lc '
    set -euo pipefail
    export PGPASSWORD="$(cat /run/secrets/postgres_password)"
    psql -h 127.0.0.1 -U raglibrarian_bootstrap -d "$1" -v ON_ERROR_STOP=1 >/dev/null
  ' sh "$database" <<SQL
DO \$\$
DECLARE
  truncate_sql text;
BEGIN
  SELECT format(
    'TRUNCATE %s RESTART IDENTITY CASCADE',
    string_agg(format('%I.%I', schemaname, tablename), ', ')
  )
    INTO truncate_sql
  FROM pg_tables
  WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
    AND schemaname NOT LIKE 'pg_toast%%';
  IF truncate_sql IS NOT NULL THEN
    EXECUTE truncate_sql;
  END IF;
END
\$\$;
SQL
}

reset_qdrant_collection() {
  local qdrant_container api_key collection http_status
  qdrant_container="$(require_service qdrant)"
  api_key="$("${compose_cmd[@]}" exec -T qdrant sh -lc 'tr -d "\r\n" < /run/secrets/retrieval_qdrant_api_key')"
  collection="${RETRIEVAL_QDRANT_COLLECTION:-evidence_v2}"
  case "$collection" in
    evidence_v2) ;;
    *)
      echo "refusing to reset non-default Qdrant collection: $collection" >&2
      exit 1
      ;;
  esac

  http_status="$(
    docker run --rm --network "container:${qdrant_container}" \
      curlimages/curl:8.11.0 -sS \
      -H "api-key: ${api_key}" \
      -X DELETE \
      -o /dev/null \
      -w '%{http_code}' \
      "http://127.0.0.1:6333/collections/${collection}"
  )"
  case "$http_status" in
    200|202|204|404) ;;
    *)
      echo "failed to delete test Qdrant collection ${collection}; HTTP ${http_status}" >&2
      exit 1
      ;;
  esac

  "${compose_cmd[@]}" run --rm --no-deps retrieval-qdrant-init >/dev/null
}

main() {
  require_service postgres >/dev/null
  require_service qdrant >/dev/null

  for database in identity catalog ingestion retrieval; do
    truncate_database "$database"
  done

  reset_qdrant_collection
  echo "Test-only PostgreSQL schemas and Qdrant collection reset to an empty state."
}

main "$@"
