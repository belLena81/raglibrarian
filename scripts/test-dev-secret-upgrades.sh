#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

for command in go jq openssl stat; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "$command is required for development secret regression tests" >&2
    exit 1
  }
done

test_root=$(mktemp -d /tmp/raglibrarian-secret-tests.XXXXXX)
trap 'rm -rf "$test_root"' EXIT

m4_files=(
  ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password
  ingestion_e2e_password ingestion_migration_pgpass ingestion_runtime_dsn
  ingestion_e2e_dsn ingestion_e2e_container_dsn ingestion_cleanup_dsn
  ingestion_minio_access_key ingestion_minio_secret_key
  ingestion_cleanup_minio_access_key ingestion_cleanup_minio_secret_key
  ingestion_e2e_minio_access_key ingestion_e2e_minio_secret_key
  catalog_ingestion_rabbitmq_uri ingestion_rabbitmq_uri
  ingestion_e2e_rabbitmq_uri ingestion_e2e_rabbitmq_container_uri
  edge_status_rabbitmq_uri_1 edge_status_rabbitmq_uri_2
  rabbitmq_definitions.json rabbitmq.conf
)

assert_complete_and_private() {
  local dir=$1
  local file
  bash ./scripts/check-m4-dev-secrets.sh "$dir"
  [[ "$(stat -c '%a' "$dir")" == 700 ]] || {
    echo "secret directory permissions are not 0700: $dir" >&2
    exit 1
  }
  for file in "${m4_files[@]}"; do
    [[ "$(stat -c '%a' "$dir/$file")" == 400 ]] || {
      echo "secret permissions are not 0400: $dir/$file" >&2
      exit 1
    }
  done
}

# Fresh checkout: the normal generator must produce the complete M4 set.
fresh_dir="$test_root/fresh"
bash ./scripts/generate-dev-secrets.sh "$fresh_dir" >/dev/null
assert_complete_and_private "$fresh_dir"

# Identity-only upgrade: the M3 generator adds non-database M4 credentials,
# then the database helper must fill the complete database set without clashes.
identity_dir="$test_root/identity-upgrade"
bash ./scripts/generate-catalog-dev-secrets.sh "$identity_dir" >/dev/null
bash ./scripts/ensure-m4-dev-secrets.sh "$identity_dir" >/dev/null
assert_complete_and_private "$identity_dir"

# Legacy M3-only upgrade: M4 adds its non-database and ingestion credentials
# first; the database helper then adds only the absent Catalog group.
m3_dir="$test_root/m3-upgrade"
mkdir -p "$m3_dir"
chmod 700 "$m3_dir"
printf '%s\n' '{"users":[],"permissions":[],"exchanges":[],"queues":[],"bindings":[]}' > "$m3_dir/rabbitmq_definitions.json"
printf '%s\n' 'management.load_definitions = /etc/rabbitmq/definitions.json' > "$m3_dir/rabbitmq.conf"
chmod 400 "$m3_dir/rabbitmq_definitions.json" "$m3_dir/rabbitmq.conf"
bash ./scripts/ensure-m4-dev-secrets.sh "$m3_dir" >/dev/null
assert_complete_and_private "$m3_dir"
for file in catalog_migration_password catalog_runtime_password catalog_migration_pgpass catalog_runtime_dsn; do
  [[ -r "$m3_dir/$file" ]] || { echo "Catalog database secret is missing after M3 upgrade: $file" >&2; exit 1; }
done

# Partial states and complete reruns must fail instead of overwriting values.
partial_dir="$test_root/partial"
mkdir -p "$partial_dir"
chmod 700 "$partial_dir"
printf '%s\n' sentinel > "$partial_dir/ingestion_runtime_password"
chmod 400 "$partial_dir/ingestion_runtime_password"
if bash ./scripts/generate-catalog-database-dev-secrets.sh "$partial_dir" >/dev/null 2>&1; then
  echo "partial database secret set was unexpectedly accepted" >&2
  exit 1
fi
[[ "$(cat "$partial_dir/ingestion_runtime_password")" == sentinel ]] || {
  echo "existing database secret was modified" >&2
  exit 1
}
if bash ./scripts/generate-catalog-database-dev-secrets.sh "$identity_dir" >/dev/null 2>&1; then
  echo "complete database secret set was unexpectedly overwritten" >&2
  exit 1
fi

partial_m4_dir="$test_root/partial-m4"
mkdir -p "$partial_m4_dir"
chmod 700 "$partial_m4_dir"
printf '%s\n' sentinel > "$partial_m4_dir/ingestion_minio_access_key"
chmod 400 "$partial_m4_dir/ingestion_minio_access_key"
if bash ./scripts/ensure-m4-dev-secrets.sh "$partial_m4_dir" >/dev/null 2>&1; then
  echo "partial M4 non-database secret set was unexpectedly accepted" >&2
  exit 1
fi
[[ "$(cat "$partial_m4_dir/ingestion_minio_access_key")" == sentinel ]] || {
  echo "existing M4 non-database secret was modified" >&2
  exit 1
}

# Stack preflight must reject permissive or indirect secret paths even when
# their contents remain readable to the current user.
chmod 750 "$fresh_dir"
if bash ./scripts/check-m4-dev-secrets.sh "$fresh_dir" >/dev/null 2>&1; then
  echo "permissive secret directory mode was unexpectedly accepted" >&2
  exit 1
fi
chmod 700 "$fresh_dir"

chmod 440 "$fresh_dir/ingestion_runtime_password"
if bash ./scripts/check-m4-dev-secrets.sh "$fresh_dir" >/dev/null 2>&1; then
  echo "permissive secret file mode was unexpectedly accepted" >&2
  exit 1
fi
chmod 400 "$fresh_dir/ingestion_runtime_password"

mv "$fresh_dir/ingestion_runtime_password" "$fresh_dir/ingestion_runtime_password.real"
ln -s ingestion_runtime_password.real "$fresh_dir/ingestion_runtime_password"
if bash ./scripts/check-m4-dev-secrets.sh "$fresh_dir" >/dev/null 2>&1; then
  echo "symlinked secret file was unexpectedly accepted" >&2
  exit 1
fi
rm "$fresh_dir/ingestion_runtime_password"
mv "$fresh_dir/ingestion_runtime_password.real" "$fresh_dir/ingestion_runtime_password"
assert_complete_and_private "$fresh_dir"

echo "Development secret generation and upgrade regressions passed"
