#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"

if [[ ! -d "$dir" || -L "$dir" || "$(stat -c '%a' "$dir")" != 700 ]]; then
  echo "test development secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
fi

files=(
  ingestion_e2e_password
  ingestion_e2e_dsn
  ingestion_e2e_container_dsn
  ingestion_e2e_minio_access_key
  ingestion_e2e_minio_secret_key
  ingestion_e2e_rabbitmq_uri
  ingestion_e2e_rabbitmq_container_uri
  retrieval_e2e_password
  retrieval_e2e_dsn
  retrieval_e2e_container_dsn
  retrieval_e2e_rabbitmq_uri
  retrieval_e2e_rabbitmq_container_uri
  answer_llm_test_api_key
)

for file in "${files[@]}"; do
  path="$dir/$file"
  if [[ ! -f "$path" || -L "$path" || ! -r "$path" || "$(stat -c '%a' "$path")" != 400 ]]; then
    echo "test secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  fi
done

definitions="$dir/rabbitmq_definitions.json"
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
[[ -f "$definitions" && ! -L "$definitions" && -r "$definitions" && "$(stat -c '%a' "$definitions")" == 400 ]] || {
  echo "test RabbitMQ definitions must be a readable non-symlink regular file with mode 0400: $definitions" >&2
  exit 1
}

jq -e '
  any(.users[]?; .name == "ingestion_e2e") and
  any(.users[]?; .name == "retrieval_e2e") and
  any(.permissions[]?; .user == "ingestion_e2e" and .vhost == "/" and .configure == "^$" and .write == "^raglibrarian\\.events\\.v1$" and .read == "^ingestion\\.book-uploaded\\.dlq\\.v1$") and
  any(.permissions[]?; .user == "retrieval_e2e" and .vhost == "/" and .configure == "^$" and .write == "^(raglibrarian\\.events\\.v1|raglibrarian\\.ingestion\\.events\\.v1)$" and .read == "^(retrieval\\..*|catalog\\.retrieval-terminal)\\.dlq\\.v1$")
' "$definitions" >/dev/null || {
  echo "test RabbitMQ credentials are missing or out of bounds" >&2
  exit 1
}
