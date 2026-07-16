#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-.dev/secrets}"
mkdir -p "$dir"
chmod 700 "$dir"
for file in catalog_migration_password catalog_runtime_password catalog_migration_pgpass catalog_runtime_dsn; do
  if [[ -e "$dir/$file" ]]; then
    echo "refusing to overwrite existing development secret: $dir/$file" >&2
    exit 1
  fi
done

migration_password=$(openssl rand -hex 32)
runtime_password=$(openssl rand -hex 32)
printf '%s\n' "$migration_password" > "$dir/catalog_migration_password"
printf '%s\n' "$runtime_password" > "$dir/catalog_runtime_password"
printf 'postgres:5432:catalog:catalog_migrator:%s\n' "$migration_password" > "$dir/catalog_migration_pgpass"
printf 'postgres://catalog_runtime:%s@postgres:5432/catalog?sslmode=disable\n' "$runtime_password" > "$dir/catalog_runtime_dsn"
chmod 400 "$dir"/catalog_*
unset migration_password runtime_password
echo "Generated Catalog development credentials in $dir"
