#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

# shellcheck source=scripts/configure-ci-host-clients.sh
source ./scripts/configure-ci-host-clients.sh

test_root="$(mktemp -d /tmp/raglibrarian-ci-host-clients.XXXXXX)"
trap 'rm -rf "$test_root"' EXIT

secret_dir="$test_root/secrets"
mkdir -p "$secret_dir"
chmod 700 "$secret_dir"

write_secret() {
  local name="$1"
  local value="$2"

  printf '%s\n' "$value" > "$secret_dir/$name"
  chmod 400 "$secret_dir/$name"
}

write_secret retrieval_runtime_host_dsn "postgres://retrieval_runtime:runtime-password@127.0.0.1:5432/retrieval?sslmode=disable"
write_secret retrieval_planner_host_dsn "postgres://retrieval_planner:planner-password@127.0.0.1:5432/retrieval?sslmode=disable"
write_secret retrieval_cleanup_host_dsn "postgres://retrieval_cleanup:cleanup-password@127.0.0.1:5432/retrieval?sslmode=disable"

rewrite_postgres_host_dsns "$secret_dir" "172.26.0.2"

assert_rewritten() {
  local name="$1"
  local role="$2"

  if [[ "$(stat -c '%a' "$secret_dir/$name")" != 400 ]]; then
    echo "rewritten secret permissions are not 0400: $name" >&2
    exit 1
  fi
  if ! grep -Eq "^postgres://${role}:[^@]+@172\\.26\\.0\\.2:5432/retrieval\\?sslmode=disable$" "$secret_dir/$name"; then
    echo "CI host client DSN was not rewritten for $role" >&2
    exit 1
  fi
}

assert_rewritten retrieval_runtime_host_dsn retrieval_runtime
assert_rewritten retrieval_planner_host_dsn retrieval_planner
assert_rewritten retrieval_cleanup_host_dsn retrieval_cleanup

echo "CI host-client DSN rewrite regressions passed"
