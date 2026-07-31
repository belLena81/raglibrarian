#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

load_compose_command() {
  local compose_files_string

  compose_files_string="${CI_COMPOSE_FILES:--f docker-compose.yml -f docker-compose.ci.yml}"
  read -r -a compose_files <<< "$compose_files_string"
  compose_cmd=(docker compose "${compose_files[@]}" --profile raglibrarian)
}

service_container_ip() {
  local service="$1"
  local container_id

  container_id="$("${compose_cmd[@]}" ps -q "$service")"
  [[ -n "$container_id" ]] || return 1
  docker inspect "$container_id" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'
}

require_ipv4() {
  [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]
}

wait_for_tcp_endpoint() {
  local host="$1"
  local port="$2"
  local attempt

  for attempt in $(seq 1 30); do
    if timeout 2 bash -c ":</dev/tcp/$host/$port" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done

  echo "Compose service is not reachable: ${host}:${port}" >&2
  return 1
}

rewrite_endpoint() {
  local file="$1"
  local loopback_port="$2"
  local host="$3"
  local temporary

  temporary="$(mktemp "${file}.XXXXXX")"
  chmod 600 "$temporary"
  sed "s/127\\.0\\.0\\.1:${loopback_port}/${host}:${loopback_port}/g" "$file" > "$temporary"
  mv "$temporary" "$file"
  chmod 400 "$file"
  grep -q "${host}:${loopback_port}" "$file"
}

rewrite_postgres_host_dsns() {
  local secret_dir="$1"
  local postgres_ip="$2"

  rewrite_endpoint "$secret_dir/retrieval_runtime_host_dsn" 5432 "$postgres_ip"
  rewrite_endpoint "$secret_dir/retrieval_migration_host_pgpass" 5432 "$postgres_ip"
  rewrite_endpoint "$secret_dir/retrieval_planner_host_dsn" 5432 "$postgres_ip"
  rewrite_endpoint "$secret_dir/retrieval_cleanup_host_dsn" 5432 "$postgres_ip"
}

append_github_env() {
  local name="$1"
  local value="$2"

  [[ -n "${GITHUB_ENV:-}" ]] || return 0
  echo "${name}=${value}" >> "$GITHUB_ENV"
}

main() {
  local secret_dir
  local postgres_ip
  local rabbitmq_ip
  local minio_ip

  cd "$root_dir"
  load_compose_command

  secret_dir="${SECRET_DIR:-.dev/secrets}"

  postgres_ip="$(service_container_ip postgres)"
  rabbitmq_ip="$(service_container_ip rabbitmq)"
  minio_ip="$(service_container_ip minio)"

  require_ipv4 "$postgres_ip"
  require_ipv4 "$rabbitmq_ip"
  require_ipv4 "$minio_ip"

  wait_for_tcp_endpoint "$postgres_ip" 5432
  wait_for_tcp_endpoint "$rabbitmq_ip" 5672
  wait_for_tcp_endpoint "$minio_ip" 9000

  rewrite_endpoint "$secret_dir/ingestion_e2e_dsn" 5432 "$postgres_ip"
  rewrite_postgres_host_dsns "$secret_dir" "$postgres_ip"
  rewrite_endpoint "$secret_dir/ingestion_e2e_rabbitmq_uri" 5672 "$rabbitmq_ip"
  append_github_env M4_E2E_MINIO_ENDPOINT "${minio_ip}:9000"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
