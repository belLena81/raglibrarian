#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
mkdir -p "$dir"
chmod 700 "$dir"

files=(
  catalog_migration_password
  catalog_runtime_password
  catalog_migration_pgpass
  catalog_runtime_dsn
)
for file in "${files[@]}"; do
  if [[ -e "$dir/$file" ]]; then
    echo "refusing to overwrite existing development secret: $dir/$file" >&2
    exit 1
  fi
done

catalog_migration_password=$(openssl rand -hex 32)
catalog_runtime_password=$(openssl rand -hex 32)

printf '%s\n' "$catalog_migration_password" > "$dir/catalog_migration_password"
printf '%s\n' "$catalog_runtime_password" > "$dir/catalog_runtime_password"
printf 'postgres:5432:catalog:catalog_migrator:%s\n' "$catalog_migration_password" > "$dir/catalog_migration_pgpass"
printf 'postgres://catalog_runtime:%s@postgres:5432/catalog?sslmode=disable\n' "$catalog_runtime_password" > "$dir/catalog_runtime_dsn"

chmod 400 "${files[@]/#/$dir/}"
unset catalog_migration_password catalog_runtime_password

echo "Generated Catalog database development credentials in $dir"
