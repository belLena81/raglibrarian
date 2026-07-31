#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"

if [[ ! -d "$dir" || -L "$dir" || "$(stat -c '%a' "$dir")" != 700 ]]; then
  echo "M4 development secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
fi

files=(
  ingestion_migration_password
  ingestion_runtime_password
  ingestion_cleanup_password
  ingestion_migration_pgpass
  ingestion_runtime_dsn
  ingestion_cleanup_dsn
  ingestion_minio_access_key
  ingestion_minio_secret_key
  layout_minio_access_key
  layout_minio_secret_key
  ingestion_cleanup_minio_access_key
  ingestion_cleanup_minio_secret_key
  catalog_ingestion_rabbitmq_uri
  ingestion_rabbitmq_uri
  layout_rabbitmq_uri
  edge_status_rabbitmq_uri_1
  edge_status_rabbitmq_uri_2
  rabbitmq_definitions.json
  rabbitmq.conf
)

for file in "${files[@]}"; do
  path="$dir/$file"
  if [[ ! -f "$path" || -L "$path" || ! -r "$path" || "$(stat -c '%a' "$path")" != 400 ]]; then
    echo "M4 development secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  fi
done

[[ "$(<"$dir/layout_minio_access_key")" == layout-parser-worker && -s "$dir/layout_minio_secret_key" ]] || {
  echo "Layout worker MinIO credentials are invalid" >&2
  exit 1
}
layout_rabbitmq_uri=$(<"$dir/layout_rabbitmq_uri")
[[ "$layout_rabbitmq_uri" =~ ^amqp://layout_parser_worker:[0-9a-f]{64}@rabbitmq:5672/$ ]] || {
  echo "Layout worker RabbitMQ URI is invalid" >&2
  exit 1
}
unset layout_rabbitmq_uri

definitions="$dir/rabbitmq_definitions.json"
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
jq -e '
  any(.users[]?; .name == "layout_parser_worker" and .tags == "") and
  any(.permissions[]?;
    .user == "layout_parser_worker" and .vhost == "/" and
    .configure == "^$" and
    .write == "^raglibrarian\\.ingestion\\.content-selection-results\\.v1$" and
    .read == "^ingestion\\.content-selection-requests\\.v1$"
  ) and
  any(.permissions[]?;
    .user == "ingestion_worker" and
    .read == "^(ingestion\\.book-uploaded\\.v1|ingestion\\.content-selection-results\\.v1)$"
  )
' "$definitions" >/dev/null || {
  echo "Dedicated layout worker RabbitMQ permissions are missing or overbroad" >&2
  exit 1
}
jq -e '
  any(.bindings[]?; .source == "raglibrarian.events.v1" and .destination == "ingestion.book-uploaded.v1" and .routing_key == "catalog.book.deletion-requested.v1") and
  any(.bindings[]?; .source == "raglibrarian.ingestion.events.v1" and .destination == "catalog.book-processing.v1" and .routing_key == "ingestion.book.artifacts-deleted.v1") and
  any(.bindings[]?; .source == "raglibrarian.ingestion.events.v1" and .destination == "ingestion.content-selection-requests.v1" and .routing_key == "ingestion.book.content-selection-requested.v1") and
  any(.bindings[]?; .source == "raglibrarian.ingestion.content-selection-results.v1" and .destination == "ingestion.content-selection-results.v1" and .routing_key == "ingestion.book.content-selection-completed.v1") and
  all(.bindings[]?; .source != "raglibrarian.ingestion.events.v1" or .destination != "ingestion.content-selection-results.v1" or .routing_key != "ingestion.book.content-selection-completed.v1")
' "$definitions" >/dev/null || {
  echo "Ingestion lifecycle or content-selection bindings are missing" >&2
  exit 1
}

jq -e '
  def retry_queue($name; $routing_key; $exchange):
    any(.queues[]?;
      .name == $name and
      .arguments["x-dead-letter-exchange"] == $exchange and
      .arguments["x-dead-letter-routing-key"] == $routing_key
    );
  def retry_binding($name):
    any(.bindings[]?;
      .source == "raglibrarian.ingestion.retry.v1" and
      .destination == $name and
      .destination_type == "queue" and
      .routing_key == $name
    );
  retry_queue("ingestion.retry.5s"; "catalog.book.uploaded.v1"; "raglibrarian.events.v1") and
  retry_queue("ingestion.retry.30s"; "catalog.book.uploaded.v1"; "raglibrarian.events.v1") and
  retry_queue("ingestion.retry.2m"; "catalog.book.uploaded.v1"; "raglibrarian.events.v1") and
  retry_queue("ingestion.deletion.retry.5s"; "catalog.book.deletion-requested.v1"; "raglibrarian.events.v1") and
  retry_queue("ingestion.deletion.retry.30s"; "catalog.book.deletion-requested.v1"; "raglibrarian.events.v1") and
  retry_queue("ingestion.deletion.retry.2m"; "catalog.book.deletion-requested.v1"; "raglibrarian.events.v1") and
  retry_binding("ingestion.retry.5s") and
  retry_binding("ingestion.retry.30s") and
  retry_binding("ingestion.retry.2m") and
  retry_binding("ingestion.deletion.retry.5s") and
  retry_binding("ingestion.deletion.retry.30s") and
  retry_binding("ingestion.deletion.retry.2m") and
  retry_queue("ingestion.content-selection.retry.5s"; "ingestion.book.content-selection-completed.v1"; "raglibrarian.ingestion.content-selection-results.v1") and
  retry_queue("ingestion.content-selection.retry.30s"; "ingestion.book.content-selection-completed.v1"; "raglibrarian.ingestion.content-selection-results.v1") and
  retry_queue("ingestion.content-selection.retry.2m"; "ingestion.book.content-selection-completed.v1"; "raglibrarian.ingestion.content-selection-results.v1") and
  retry_binding("ingestion.content-selection.retry.5s") and
  retry_binding("ingestion.content-selection.retry.30s") and
  retry_binding("ingestion.content-selection.retry.2m")
' "$definitions" >/dev/null || {
  echo "M4 Ingestion retry queues are not isolated by source route" >&2
  exit 1
}
