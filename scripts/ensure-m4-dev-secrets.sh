#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
non_database_files=(
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
)

present=0
for file in "${non_database_files[@]}"; do
  [[ ! -e "$dir/$file" ]] || present=$((present + 1))
done

if ((present == 0)); then
  bash ./scripts/generate-m4-dev-secrets.sh "$dir"
elif ((present != ${#non_database_files[@]})); then
  echo "refusing to modify a partial M4 non-database secret set in $dir" >&2
  exit 1
fi

database_files=(
  catalog_migration_password
  catalog_runtime_password
  catalog_migration_pgpass
  catalog_runtime_dsn
  ingestion_migration_password
  ingestion_runtime_password
  ingestion_cleanup_password
  ingestion_e2e_password
  ingestion_migration_pgpass
  ingestion_runtime_dsn
  ingestion_e2e_dsn
  ingestion_e2e_container_dsn
  ingestion_cleanup_dsn
)
database_complete=true
for file in "${database_files[@]}"; do
  [[ -r "$dir/$file" ]] || database_complete=false
done
if [[ "$database_complete" == false ]]; then
  bash ./scripts/generate-catalog-database-dev-secrets.sh "$dir"
fi

bash ./scripts/check-m4-dev-secrets.sh "$dir"
