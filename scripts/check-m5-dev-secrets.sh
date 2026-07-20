#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
[[ -d "$dir" && ! -L "$dir" && "$(stat -c '%a' "$dir")" == 700 ]] || {
  echo "M5 secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
}

files=(
  retrieval_migration_password retrieval_runtime_password retrieval_search_password retrieval_planner_password retrieval_indexer_password retrieval_dispatcher_password retrieval_cleanup_password retrieval_e2e_password
  retrieval_migration_pgpass retrieval_runtime_dsn retrieval_runtime_host_dsn retrieval_search_dsn retrieval_cleanup_dsn retrieval_e2e_dsn retrieval_e2e_container_dsn
  retrieval_minio_access_key retrieval_minio_secret_key retrieval_consumer_rabbitmq_uri
  retrieval_publisher_rabbitmq_uri catalog_retrieval_rabbitmq_uri retrieval_e2e_rabbitmq_uri
  retrieval_e2e_rabbitmq_container_uri retrieval_qdrant_api_key retrieval_qdrant_read_api_key rabbitmq_definitions.json rabbitmq.conf
)
for file in "${files[@]}"; do
  path="$dir/$file"
  [[ -f "$path" && ! -L "$path" && -r "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
    echo "M5 secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  }
done
