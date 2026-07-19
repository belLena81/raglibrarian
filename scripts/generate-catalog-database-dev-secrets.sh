#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
mkdir -p "$dir"
chmod 700 "$dir"

catalog_files=(
  catalog_migration_password
  catalog_runtime_password
  catalog_migration_pgpass
  catalog_runtime_dsn
)
ingestion_files=(
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

group_state() {
  local present=0
  local file
  for file in "$@"; do
    [[ ! -e "$dir/$file" ]] || present=$((present + 1))
  done
  if ((present == 0)); then
    printf 'missing\n'
  elif ((present == $#)); then
    printf 'complete\n'
  else
    printf 'partial\n'
  fi
}

catalog_state=$(group_state "${catalog_files[@]}")
ingestion_state=$(group_state "${ingestion_files[@]}")
if [[ "$catalog_state" == partial || "$ingestion_state" == partial ]]; then
  echo "refusing to modify a partial database development secret set in $dir" >&2
  exit 1
fi
if [[ "$catalog_state" == complete && "$ingestion_state" == complete ]]; then
  echo "refusing to overwrite existing database development secrets in $dir" >&2
  exit 1
fi

if [[ "$catalog_state" == missing ]]; then
  catalog_migration_password=$(openssl rand -hex 32)
  catalog_runtime_password=$(openssl rand -hex 32)

  printf '%s\n' "$catalog_migration_password" > "$dir/catalog_migration_password"
  printf '%s\n' "$catalog_runtime_password" > "$dir/catalog_runtime_password"
  printf 'postgres:5432:catalog:catalog_migrator:%s\n' "$catalog_migration_password" > "$dir/catalog_migration_pgpass"
  printf 'postgres://catalog_runtime:%s@postgres:5432/catalog?sslmode=disable\n' "$catalog_runtime_password" > "$dir/catalog_runtime_dsn"
  chmod 400 "${catalog_files[@]/#/$dir/}"
  unset catalog_migration_password catalog_runtime_password
fi

if [[ "$ingestion_state" == missing ]]; then
  ingestion_migration_password=$(openssl rand -hex 32)
  ingestion_runtime_password=$(openssl rand -hex 32)
  ingestion_cleanup_password=$(openssl rand -hex 32)
  ingestion_e2e_password=$(openssl rand -hex 32)

  printf '%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_password"
  printf '%s\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_password"
  printf '%s\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_password"
  printf '%s\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_password"
  printf 'postgres:5432:ingestion:ingestion_migrator:%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_pgpass"
  printf 'postgres://ingestion_runtime:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_dsn"
  printf 'postgres://ingestion_e2e:%s@127.0.0.1:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_dsn"
  printf 'postgres://ingestion_e2e:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_container_dsn"
  printf 'postgres://ingestion_cleanup:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_dsn"
  chmod 400 "${ingestion_files[@]/#/$dir/}"
  unset ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password ingestion_e2e_password
fi

echo "Generated complete service database development credentials in $dir"
