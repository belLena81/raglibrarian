#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

compose_files_string="${CI_COMPOSE_FILES:--f docker-compose.yml -f docker-compose.ci.yml}"
read -r -a compose_files <<< "$compose_files_string"

exec docker compose "${compose_files[@]}" --profile raglibrarian "$@"
