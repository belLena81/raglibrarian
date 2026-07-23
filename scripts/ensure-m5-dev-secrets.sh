#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
if bash ./scripts/check-m5-dev-secrets.sh "$dir" >/dev/null 2>&1; then
  exit 0
fi

existing=$(find "$dir" -maxdepth 1 -type f -name 'retrieval_*' -print -quit 2>/dev/null || true)
if [[ -n "$existing" ]]; then
  bash ./scripts/upgrade-m7-rabbitmq-topology.sh "$dir"
  legacy_files=(
    retrieval_migration_password retrieval_runtime_password retrieval_search_password retrieval_planner_password
    retrieval_indexer_password retrieval_dispatcher_password retrieval_cleanup_password retrieval_e2e_password
    retrieval_migration_pgpass retrieval_runtime_dsn retrieval_runtime_host_dsn retrieval_search_dsn
    retrieval_cleanup_dsn retrieval_e2e_dsn retrieval_e2e_container_dsn retrieval_minio_access_key
    retrieval_minio_secret_key retrieval_consumer_rabbitmq_uri retrieval_publisher_rabbitmq_uri
    catalog_retrieval_rabbitmq_uri retrieval_e2e_rabbitmq_uri retrieval_e2e_rabbitmq_container_uri
    retrieval_qdrant_api_key retrieval_qdrant_read_api_key rabbitmq_definitions.json rabbitmq.conf
  )
  for file in "${legacy_files[@]}"; do
    path="$dir/$file"
    [[ -f "$path" && ! -L "$path" && -r "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
      echo "Incomplete legacy M5 secret set in $dir; refusing an automatic partial overwrite" >&2
      exit 1
    }
  done

  IFS= read -r planner_password < "$dir/retrieval_planner_password"
  IFS= read -r cleanup_password < "$dir/retrieval_cleanup_password"
  [[ -n "$planner_password" && -n "$cleanup_password" ]] || {
    echo "Legacy M5 role passwords are invalid; refusing DSN upgrade" >&2
    exit 1
  }

  ensure_derived_dsn() {
    local file=$1
    local value=$2
    local path="$dir/$file"
    if [[ -e "$path" ]]; then
      [[ -f "$path" && ! -L "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
        echo "Derived M5 DSN has unsafe file metadata: $path" >&2
        exit 1
      }
      local existing_value
      IFS= read -r existing_value < "$path"
      [[ "$existing_value" == "$value" ]] || {
        echo "Derived M5 DSN conflicts with the existing role password: $path" >&2
        exit 1
      }
      return
    fi
    local temporary
    temporary=$(mktemp "$dir/.retrieval-dsn.XXXXXX")
    printf '%s\n' "$value" > "$temporary"
    chmod 400 "$temporary"
    mv "$temporary" "$path"
  }

  ensure_derived_dsn retrieval_planner_dsn "postgres://retrieval_planner:$planner_password@postgres:5432/retrieval?sslmode=disable"
  ensure_derived_dsn retrieval_planner_host_dsn "postgres://retrieval_planner:$planner_password@127.0.0.1:5432/retrieval?sslmode=disable"
  ensure_derived_dsn retrieval_cleanup_dsn "postgres://retrieval_cleanup:$cleanup_password@postgres:5432/retrieval?sslmode=disable"
  ensure_derived_dsn retrieval_cleanup_host_dsn "postgres://retrieval_cleanup:$cleanup_password@127.0.0.1:5432/retrieval?sslmode=disable"
  unset planner_password cleanup_password
  bash ./scripts/check-m5-dev-secrets.sh "$dir" && exit 0
  echo "Incomplete M5 secret set in $dir; refusing an automatic partial overwrite" >&2
  exit 1
fi
bash ./scripts/generate-m5-dev-secrets.sh "$dir"
