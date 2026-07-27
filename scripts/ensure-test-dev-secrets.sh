#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"

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

present_files=0
for file in "${files[@]}"; do
  [[ -e "$dir/$file" ]] && ((present_files += 1))
done

if (( present_files == 0 )); then
  bash ./scripts/generate-test-secrets.sh "$dir"
  exit 0
fi

if (( present_files == ${#files[@]} )); then
  bash ./scripts/check-test-dev-secrets.sh "$dir"
  echo "Using existing test-only development credentials in $dir"
  exit 0
fi

echo "incomplete test development secrets in $dir; remove them and rerun the local bootstrap" >&2
exit 1
