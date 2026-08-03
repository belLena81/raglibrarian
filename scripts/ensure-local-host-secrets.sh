#!/usr/bin/env bash
set -euo pipefail
umask 077

service_container_ips() {
  local service="$1"
  local container_id

  container_id="$(docker compose --profile raglibrarian ps -q "$service" 2>/dev/null || true)"
  [[ -n "$container_id" ]] || return 1
  docker inspect "$container_id" --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}'
}

tcp_endpoint_reachable() {
  local host="$1"
  local port="$2"

  : >/dev/tcp/"$host"/"$port"
}

resolve_service_tcp_host() {
  local service="$1"
  local host_port="$2"
  local container_port="$3"
  local container_ip

  if tcp_endpoint_reachable 127.0.0.1 "$host_port" 2>/dev/null; then
    printf '127.0.0.1:%s' "$host_port"
    return 0
  fi

  while IFS= read -r container_ip; do
    [[ -n "$container_ip" ]] || continue
    if tcp_endpoint_reachable "$container_ip" "$container_port" 2>/dev/null; then
      printf '%s:%s' "$container_ip" "$container_port"
      return 0
    fi
  done < <(service_container_ips "$service")

  return 1
}

read_value() {
  local file="$1"
  local path="$secret_dir/$file"
  [[ -r "$path" ]] || { echo "Missing readable secret: $path" >&2; exit 1; }
  IFS= read -r value < "$path"
  [[ -n "$value" ]] || { echo "Secret is empty: $path" >&2; exit 1; }
  printf '%s' "$value"
}

ensure_file() {
  local file="$1"
  local value="$2"
  local path="$host_dir/$file"
  local tmp_file

  if [[ -e "$path" ]]; then
    [[ -f "$path" && ! -L "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
      echo "Host secret file has unsafe metadata: $path" >&2
      exit 1
    }
    IFS= read -r existing < "$path"
    [[ "$existing" == "$value" ]] && return
  fi
  tmp_file="$(mktemp "$host_dir/.secret.XXXXXX")"
  printf '%s\n' "$value" > "$tmp_file"
  chmod 400 "$tmp_file"
  mv -f "$tmp_file" "$path"
}

ensure_secret_file() {
  local file="$1"
  local source_path="$secret_dir/$file"
  local path="$host_dir/$file"
  local tmp_file

  [[ -f "$source_path" && ! -L "$source_path" && -r "$source_path" ]] || {
    echo "Missing safe readable secret: $source_path" >&2
    exit 1
  }
  if [[ -e "$path" ]]; then
    [[ -f "$path" && ! -L "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
      echo "Host secret file has unsafe metadata: $path" >&2
      exit 1
    }
    cmp -s "$source_path" "$path" && return
  fi
  tmp_file="$(mktemp "$host_dir/.secret.XXXXXX")"
  cp "$source_path" "$tmp_file"
  chmod 400 "$tmp_file"
  mv -f "$tmp_file" "$path"
}

main() {
  secret_dir="${1:-${SECRET_DIR:-.dev/secrets}}"
  host_dir="${2:-${HOST_SECRET_DIR:-.dev/host-secrets}}"

  [[ -d "$secret_dir" ]] || { echo "Secret directory not found: $secret_dir" >&2; exit 1; }
  mkdir -p "$host_dir"
  chmod 700 "$host_dir"

  postgres_port="${POSTGRES_PORT:-5432}"
  rabbit_port="${RABBITMQ_AMQP_PORT:-5672}"

  identity_runtime_password="$(read_value identity_runtime_password)"
  catalog_runtime_password="$(read_value catalog_runtime_password)"
  ingestion_runtime_password="$(read_value ingestion_runtime_password)"
  ingestion_cleanup_password="$(read_value ingestion_cleanup_password)"
  retrieval_runtime_password="$(read_value retrieval_runtime_password)"
  retrieval_search_password="$(read_value retrieval_search_password)"
  postgres_host="$(resolve_service_tcp_host postgres "$postgres_port" 5432)"
  rabbit_host="$(resolve_service_tcp_host rabbitmq "$rabbit_port" 5672)"

  ensure_file identity_runtime_dsn "postgres://identity_runtime:${identity_runtime_password}@${postgres_host}/identity?sslmode=disable"
  ensure_file catalog_runtime_dsn "postgres://catalog_runtime:${catalog_runtime_password}@${postgres_host}/catalog?sslmode=disable"
  ensure_file ingestion_runtime_dsn "postgres://ingestion_runtime:${ingestion_runtime_password}@${postgres_host}/ingestion?sslmode=disable"
  ensure_file ingestion_cleanup_dsn "postgres://ingestion_cleanup:${ingestion_cleanup_password}@${postgres_host}/ingestion?sslmode=disable"
  ensure_file retrieval_runtime_dsn "postgres://retrieval_runtime:${retrieval_runtime_password}@${postgres_host}/retrieval?sslmode=disable"
  ensure_file retrieval_search_dsn "postgres://retrieval_search:${retrieval_search_password}@${postgres_host}/retrieval?sslmode=disable"
  ensure_secret_file retrieval_summary_cache_hmac_key

derive_rabbit_host_uri() {
  local file="$1"
  local uri prefix suffix
  uri="$(read_value "$file")"
  prefix="${uri%@*}"
  suffix="${uri#*@}"
  suffix="/${suffix#*/}"
  printf '%s@%s%s' "$prefix" "$rabbit_host" "$suffix"
}

  ensure_file catalog_rabbitmq_uri "$(derive_rabbit_host_uri catalog_rabbitmq_uri)"
  ensure_file catalog_ingestion_rabbitmq_uri "$(derive_rabbit_host_uri catalog_ingestion_rabbitmq_uri)"
  ensure_file catalog_retrieval_rabbitmq_uri "$(derive_rabbit_host_uri catalog_retrieval_rabbitmq_uri)"
  ensure_file ingestion_rabbitmq_uri "$(derive_rabbit_host_uri ingestion_rabbitmq_uri)"
  ensure_file layout_rabbitmq_uri "$(derive_rabbit_host_uri layout_rabbitmq_uri)"
  ensure_file edge_status_rabbitmq_uri_1 "$(derive_rabbit_host_uri edge_status_rabbitmq_uri_1)"
  ensure_file edge_status_rabbitmq_uri_2 "$(derive_rabbit_host_uri edge_status_rabbitmq_uri_2)"
  ensure_file retrieval_consumer_rabbitmq_uri "$(derive_rabbit_host_uri retrieval_consumer_rabbitmq_uri)"
  ensure_file retrieval_publisher_rabbitmq_uri "$(derive_rabbit_host_uri retrieval_publisher_rabbitmq_uri)"

  echo "Host secret overlays ready in $host_dir"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
