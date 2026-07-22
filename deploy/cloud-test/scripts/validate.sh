#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
env_file="${1:-$repo_root/deploy/cloud-test/.env.cloud}"

[[ -r "$env_file" ]] || { echo "missing cloud environment file: $env_file" >&2; exit 1; }
[[ -f "$env_file" && ! -L "$env_file" && "$(stat -c '%u' "$env_file")" == "$(id -u)" ]] || { echo "cloud environment file is unsafe" >&2; exit 1; }
if [[ "${ALLOW_EXAMPLE_CONFIG:-false}" != "true" ]]; then
  env_mode="$(stat -c '%a' "$env_file")"
  (( (8#$env_mode & 8#022) == 0 )) || { echo "cloud environment file must not be group/world writable" >&2; exit 1; }
fi
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker compose version >/dev/null

if [[ "${ALLOW_EXAMPLE_CONFIG:-false}" != "true" ]]; then
  if grep -Eq '(^|[.=])(REPLACE_ME|ACCOUNT_ID)|example-tailnet|verified-sender@example.com' "$env_file"; then
    echo "cloud environment still contains example placeholders" >&2
    exit 1
  fi
fi

cd "$repo_root"
docker compose \
  --env-file "$env_file" \
  -f docker-compose.yml \
  -f deploy/cloud-test/compose.yaml \
  --profile m5 \
  --profile m6 \
  config --quiet

GOCACHE="${GOCACHE:-/tmp/raglibrarian-go-cache}" go test ./tools/rabbitmq-topology

echo "Cloud deployment configuration is valid"
