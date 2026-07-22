#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
source_dir="${1:-}"
secret_dir="${2:-$repo_root/deploy/cloud-test/runtime/secrets}"

[[ -n "$source_dir" && -d "$source_dir" && ! -L "$source_dir" ]] || {
  echo "usage: $0 PROVIDER_SECRET_DIRECTORY [TARGET_SECRET_DIRECTORY]" >&2
  exit 1
}
[[ -d "$secret_dir" && ! -L "$secret_dir" ]] || {
  echo "target secret directory must already exist and contain generated internal secrets" >&2
  exit 1
}

private_directory() {
  local path="$1"
  local mode
  [[ "$(stat -c '%u' "$path")" == "$(id -u)" ]] || { echo "directory must be owned by the deployment user: $path" >&2; exit 1; }
  mode="$(stat -c '%a' "$path")"
  (( (8#$mode & 8#077) == 0 )) || { echo "directory must be owner-only: $path" >&2; exit 1; }
}
private_directory "$source_dir"
private_directory "$secret_dir"

require_secret() {
  local name="$1"
  local path="$source_dir/$name"
  [[ -f "$path" && ! -L "$path" ]] || { echo "missing provider secret file: $name" >&2; exit 1; }
  local mode
  mode="$(stat -c '%a' "$path")"
  (( (8#$mode & 8#077) == 0 )) || { echo "provider secret must be owner-only: $name" >&2; exit 1; }
  [[ -s "$path" && "$(wc -c < "$path")" -le 4096 ]] || { echo "provider secret is empty or oversized: $name" >&2; exit 1; }
}

install_secret() {
  local source_name="$1"
  local target_name="$2"
  install -m 0400 "$source_dir/$source_name" "$staging_dir/new.$target_name"
}

require_secret rabbitmq_uri
rabbitmq_uri="$(tr -d '\n' < "$source_dir/rabbitmq_uri")"
case "$rabbitmq_uri" in
  amqps://*/*) ;;
  *) echo "rabbitmq_uri must contain an authenticated amqps URL and vhost" >&2; exit 1 ;;
esac
unset rabbitmq_uri

provider_sources=(rabbitmq_uri smtp_password groq_api_key s3_catalog_access_key s3_catalog_secret_key s3_ingestion_access_key s3_ingestion_secret_key s3_retrieval_access_key s3_retrieval_secret_key)
for provider_source in "${provider_sources[@]}"; do
  require_secret "$provider_source"
done

staging_dir="$(mktemp -d "$secret_dir/.provider-stage.XXXXXX")"
chmod 700 "$staging_dir"
targets=()
cleanup() {
  local status=$?
  if (( status != 0 )); then
    for target_name in "${targets[@]}"; do
      if [[ -e "$staging_dir/old.$target_name" ]]; then
        mv -f "$staging_dir/old.$target_name" "$secret_dir/$target_name"
      else
        rm -f "$secret_dir/$target_name"
      fi
    done
  fi
  rm -rf "$staging_dir"
  exit "$status"
}
trap cleanup EXIT

install_secret rabbitmq_uri rabbitmq_provider_uri

for rabbit_secret in \
  catalog_rabbitmq_uri catalog_ingestion_rabbitmq_uri catalog_retrieval_rabbitmq_uri \
  ingestion_rabbitmq_uri retrieval_consumer_rabbitmq_uri retrieval_publisher_rabbitmq_uri \
  edge_status_rabbitmq_uri_1 edge_status_rabbitmq_uri_2; do
  install_secret rabbitmq_uri "$rabbit_secret"
done

install_secret smtp_password identity_smtp_password
install_secret groq_api_key answer_llm_api_key
install_secret s3_catalog_access_key catalog_minio_access_key
install_secret s3_catalog_secret_key catalog_minio_secret_key
install_secret s3_ingestion_access_key ingestion_minio_access_key
install_secret s3_ingestion_secret_key ingestion_minio_secret_key
install_secret s3_retrieval_access_key retrieval_minio_access_key
install_secret s3_retrieval_secret_key retrieval_minio_secret_key

for staged_file in "$staging_dir"/new.*; do
  target_name="${staged_file##*/new.}"
  if [[ -e "$secret_dir/$target_name" ]]; then
    cp -p "$secret_dir/$target_name" "$staging_dir/old.$target_name"
  fi
  mv -f "$staged_file" "$secret_dir/$target_name"
  targets+=("$target_name")
done

trap - EXIT
rm -rf "$staging_dir"

echo "Installed provider credentials as owner-only runtime secret files"
