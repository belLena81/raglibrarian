#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }
command -v go >/dev/null || { echo "go is required" >&2; exit 1; }
export GOCACHE="${GOCACHE:-/tmp/raglibrarian-go-cache}"

dir="${1:-.dev/secrets}"
mkdir -p "$dir"
chmod 700 "$dir"

files=(
  postgres_password postgres_pgpass identity_migration_password identity_runtime_password
	 catalog_migration_password catalog_runtime_password catalog_migration_pgpass catalog_runtime_dsn
	 ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password ingestion_e2e_password ingestion_migration_pgpass ingestion_runtime_dsn ingestion_e2e_dsn ingestion_e2e_container_dsn ingestion_cleanup_dsn
  identity_migration_pgpass identity_migration_dsn identity_runtime_dsn identity_signing_key
  edge_verify_key edge.env identity_email_outbox_key
	identity_email_fingerprint_key identity_password_reset_hmac_key identity_smtp_password
)
for file in "${files[@]}"; do
  if [[ -e "$dir/$file" ]]; then
    echo "refusing to overwrite existing development secret: $dir/$file" >&2
    exit 1
  fi
done

postgres_password=$(openssl rand -hex 32)
migration_password=$(openssl rand -hex 32)
runtime_password=$(openssl rand -hex 32)
catalog_migration_password=$(openssl rand -hex 32)
catalog_runtime_password=$(openssl rand -hex 32)
ingestion_migration_password=$(openssl rand -hex 32)
ingestion_runtime_password=$(openssl rand -hex 32)
ingestion_cleanup_password=$(openssl rand -hex 32)
ingestion_e2e_password=$(openssl rand -hex 32)
smtp_password=$(openssl rand -hex 32)
key_output=$(cd pkg/auth && go run ./cmd/keygen/)
signing_key=$(printf '%s\n' "$key_output" | sed -n 's/^IDENTITY_SIGNING_KEY=//p')
verify_key=$(printf '%s\n' "$key_output" | sed -n 's/^EDGE_VERIFY_KEY=//p')
test -n "$signing_key" && test -n "$verify_key" || { echo "key generation failed" >&2; exit 1; }

printf '%s\n' "$postgres_password" > "$dir/postgres_password"
printf 'postgres:5432:*:raglibrarian_bootstrap:%s\n' "$postgres_password" > "$dir/postgres_pgpass"
printf '%s\n' "$migration_password" > "$dir/identity_migration_password"
printf '%s\n' "$runtime_password" > "$dir/identity_runtime_password"
printf '%s\n' "$catalog_migration_password" > "$dir/catalog_migration_password"
printf '%s\n' "$catalog_runtime_password" > "$dir/catalog_runtime_password"
printf 'postgres:5432:identity:identity_migrator:%s\n' "$migration_password" > "$dir/identity_migration_pgpass"
printf 'postgres://identity_migrator:%s@postgres:5432/identity?sslmode=disable\n' "$migration_password" > "$dir/identity_migration_dsn"
printf 'postgres://identity_runtime:%s@postgres:5432/identity?sslmode=disable\n' "$runtime_password" > "$dir/identity_runtime_dsn"
printf 'postgres:5432:catalog:catalog_migrator:%s\n' "$catalog_migration_password" > "$dir/catalog_migration_pgpass"
printf 'postgres://catalog_runtime:%s@postgres:5432/catalog?sslmode=disable\n' "$catalog_runtime_password" > "$dir/catalog_runtime_dsn"
printf '%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_password"
printf '%s\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_password"
printf '%s\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_password"
printf '%s\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_password"
printf 'postgres:5432:ingestion:ingestion_migrator:%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_pgpass"
printf 'postgres://ingestion_runtime:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_dsn"
printf 'postgres://ingestion_e2e:%s@127.0.0.1:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_dsn"
printf 'postgres://ingestion_e2e:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_container_dsn"
printf 'postgres://ingestion_cleanup:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_dsn"
printf '%s\n' "$signing_key" > "$dir/identity_signing_key"
printf '%s\n' "$verify_key" > "$dir/edge_verify_key"
printf 'EDGE_VERIFY_KEY=%s\n' "$verify_key" > "$dir/edge.env"
openssl rand -hex 32 > "$dir/identity_email_outbox_key"
openssl rand -hex 32 > "$dir/identity_email_fingerprint_key"
openssl rand -hex 32 > "$dir/identity_password_reset_hmac_key"
printf '%s\n' "$smtp_password" > "$dir/identity_smtp_password"

chmod 400 "$dir"/*
unset postgres_password migration_password runtime_password catalog_migration_password catalog_runtime_password ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password ingestion_e2e_password smtp_password key_output signing_key verify_key
bash ./scripts/generate-catalog-dev-secrets.sh "$dir"
echo "Generated file-backed development credentials in $dir"
