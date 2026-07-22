#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
env_file="${1:-$repo_root/deploy/cloud-test/.env.cloud}"

[[ -r "$env_file" ]] || { echo "missing cloud environment file: $env_file" >&2; exit 1; }
[[ -f "$env_file" && ! -L "$env_file" && "$(stat -c '%u' "$env_file")" == "$(id -u)" ]] || { echo "cloud environment file is unsafe" >&2; exit 1; }
env_mode="$(stat -c '%a' "$env_file")"
(( (8#$env_mode & 8#022) == 0 )) || { echo "cloud environment file must not be group/world writable" >&2; exit 1; }
command -v tailscale >/dev/null || { echo "tailscale is required on the host" >&2; exit 1; }

read_config() {
  local name="$1"
  sed -n "s/^${name}=//p" "$env_file" | tail -n 1
}

secret_dir="${SECRET_DIR:-$(read_config SECRET_DIR)}"
public_origin="${PUBLIC_ORIGIN:-$(read_config PUBLIC_ORIGIN)}"
management_base_url="${RABBITMQ_MANAGEMENT_BASE_URL:-$(read_config RABBITMQ_MANAGEMENT_BASE_URL)}"
[[ -n "$secret_dir" && -n "$public_origin" ]] || { echo "SECRET_DIR and PUBLIC_ORIGIN are required" >&2; exit 1; }
case "$secret_dir" in /*) ;; *) secret_dir="$repo_root/${secret_dir#./}" ;; esac

bash "$repo_root/deploy/cloud-test/scripts/validate.sh" "$env_file"
cd "$repo_root"
topology_args=(
  --uri-file "$secret_dir/rabbitmq_provider_uri"
  --definitions "$secret_dir/rabbitmq_definitions.json"
)
if [[ -n "$management_base_url" ]]; then
  topology_args+=(--management-base-url "$management_base_url")
fi
go run ./tools/rabbitmq-topology "${topology_args[@]}"

docker compose \
  --env-file "$env_file" \
  -f docker-compose.yml \
  -f deploy/cloud-test/compose.yaml \
  --profile m5 \
  --profile m6 \
  up -d --build --wait --wait-timeout 600

tailscale serve --bg --https=443 http://127.0.0.1:8088
echo "RAGLibrarian is available at $public_origin"
