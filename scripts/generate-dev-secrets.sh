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
  identity_migration_pgpass identity_migration_dsn identity_runtime_dsn identity_signing_key
  edge_verify_key edge.env identity_email_outbox_key
  identity_email_fingerprint_key identity_smtp_password
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
smtp_password=$(openssl rand -hex 32)
key_output=$(cd pkg/auth && go run ./cmd/keygen/)
signing_key=$(printf '%s\n' "$key_output" | sed -n 's/^IDENTITY_SIGNING_KEY=//p')
verify_key=$(printf '%s\n' "$key_output" | sed -n 's/^EDGE_VERIFY_KEY=//p')
test -n "$signing_key" && test -n "$verify_key" || { echo "key generation failed" >&2; exit 1; }

printf '%s\n' "$postgres_password" > "$dir/postgres_password"
printf 'postgres:5432:*:raglibrarian_bootstrap:%s\n' "$postgres_password" > "$dir/postgres_pgpass"
printf '%s\n' "$migration_password" > "$dir/identity_migration_password"
printf '%s\n' "$runtime_password" > "$dir/identity_runtime_password"
printf 'postgres:5432:identity:identity_migrator:%s\n' "$migration_password" > "$dir/identity_migration_pgpass"
printf 'postgres://identity_migrator:%s@postgres:5432/identity?sslmode=disable\n' "$migration_password" > "$dir/identity_migration_dsn"
printf 'postgres://identity_runtime:%s@postgres:5432/identity?sslmode=disable\n' "$runtime_password" > "$dir/identity_runtime_dsn"
printf '%s\n' "$signing_key" > "$dir/identity_signing_key"
printf '%s\n' "$verify_key" > "$dir/edge_verify_key"
printf 'EDGE_VERIFY_KEY=%s\n' "$verify_key" > "$dir/edge.env"
openssl rand -hex 32 > "$dir/identity_email_outbox_key"
openssl rand -hex 32 > "$dir/identity_email_fingerprint_key"
printf '%s\n' "$smtp_password" > "$dir/identity_smtp_password"

chmod 400 "$dir"/*
unset postgres_password migration_password runtime_password smtp_password key_output signing_key verify_key
echo "Generated file-backed development credentials in $dir"
