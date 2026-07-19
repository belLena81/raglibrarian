#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"

if [[ ! -d "$dir" || -L "$dir" || "$(stat -c '%a' "$dir")" != 700 ]]; then
  echo "M4 development secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
fi

files=(
  ingestion_migration_password
  ingestion_runtime_password
  ingestion_cleanup_password
  ingestion_e2e_password
  ingestion_migration_pgpass
  ingestion_runtime_dsn
  ingestion_e2e_dsn
  ingestion_e2e_container_dsn
  ingestion_cleanup_dsn
  ingestion_minio_access_key
  ingestion_minio_secret_key
  ingestion_cleanup_minio_access_key
  ingestion_cleanup_minio_secret_key
  ingestion_e2e_minio_access_key
  ingestion_e2e_minio_secret_key
  catalog_ingestion_rabbitmq_uri
  ingestion_rabbitmq_uri
  ingestion_e2e_rabbitmq_uri
  ingestion_e2e_rabbitmq_container_uri
  edge_status_rabbitmq_uri_1
  edge_status_rabbitmq_uri_2
  rabbitmq_definitions.json
  rabbitmq.conf
)

for file in "${files[@]}"; do
  path="$dir/$file"
  if [[ ! -f "$path" || -L "$path" || ! -r "$path" || "$(stat -c '%a' "$path")" != 400 ]]; then
    echo "M4 development secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  fi
done
