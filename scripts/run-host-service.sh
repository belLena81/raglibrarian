#!/usr/bin/env bash
set -euo pipefail

service_name="${1:?service name is required}"
log_file="${2:?log file is required}"
work_dir="${3:?work directory is required}"
shift 3

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
env_file="${HOST_ENV_FILE:-$root_dir/.dev/host-services.env}"
[[ -r "$env_file" ]] || { echo "Host env file is not readable: $env_file" >&2; exit 1; }

mkdir -p "$(dirname "$log_file")"

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a

exec >>"$log_file" 2>&1
echo "[$(date -Is)] starting $service_name"
cd "$work_dir"
exec "$@"
