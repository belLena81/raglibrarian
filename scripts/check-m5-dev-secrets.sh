#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
[[ -d "$dir" && ! -L "$dir" && "$(stat -c '%a' "$dir")" == 700 ]] || {
  echo "M5 secret directory must be a non-symlink directory with mode 0700: $dir" >&2
  exit 1
}

files=(
  retrieval_migration_password retrieval_runtime_password retrieval_search_password retrieval_planner_password retrieval_indexer_password retrieval_dispatcher_password retrieval_cleanup_password retrieval_e2e_password
  retrieval_migration_pgpass retrieval_runtime_dsn retrieval_runtime_host_dsn retrieval_search_dsn retrieval_cleanup_dsn retrieval_e2e_dsn retrieval_e2e_container_dsn
  retrieval_minio_access_key retrieval_minio_secret_key retrieval_consumer_rabbitmq_uri
  retrieval_publisher_rabbitmq_uri catalog_retrieval_rabbitmq_uri retrieval_e2e_rabbitmq_uri
  retrieval_e2e_rabbitmq_container_uri retrieval_qdrant_api_key retrieval_qdrant_read_api_key rabbitmq_definitions.json rabbitmq.conf
)
for file in "${files[@]}"; do
  path="$dir/$file"
  [[ -f "$path" && ! -L "$path" && -r "$path" && "$(stat -c '%a' "$path")" == 400 ]] || {
    echo "M5 secret must be a readable non-symlink regular file with mode 0400: $path" >&2
    exit 1
  }
done

definitions="$dir/rabbitmq_definitions.json"
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

jq -e '
  any(.exchanges[]?; .name == "raglibrarian.retrieval.source-return.v1")
' "$definitions" >/dev/null || {
  echo "Missing Retrieval source return exchange" >&2
  exit 1
}

jq -e '
  [
    .queues[]?
    | select(
        .name == "retrieval.book-uploaded.v1.retry.5s" or
        .name == "retrieval.book-uploaded.v1.retry.30s" or
        .name == "retrieval.chunks-ready.v1.retry.5s" or
        .name == "retrieval.chunks-ready.v1.retry.30s"
      )
    | {
        name,
        exchange: .arguments["x-dead-letter-exchange"],
        routing_key: .arguments["x-dead-letter-routing-key"]
      }
  ]
  == [
    {name:"retrieval.book-uploaded.v1.retry.5s", exchange:"raglibrarian.retrieval.source-return.v1", routing_key:"catalog.book.uploaded.v1"},
    {name:"retrieval.book-uploaded.v1.retry.30s", exchange:"raglibrarian.retrieval.source-return.v1", routing_key:"catalog.book.uploaded.v1"},
    {name:"retrieval.chunks-ready.v1.retry.5s", exchange:"raglibrarian.retrieval.source-return.v1", routing_key:"ingestion.book.chunks-ready.v1"},
    {name:"retrieval.chunks-ready.v1.retry.30s", exchange:"raglibrarian.retrieval.source-return.v1", routing_key:"ingestion.book.chunks-ready.v1"}
  ]
' "$definitions" >/dev/null || {
  echo "Retrieval source retry queues are not isolated to Retrieval-only return paths" >&2
  exit 1
}

jq -e '
  [
    .bindings[]?
    | select(.source == "raglibrarian.retrieval.source-return.v1")
    | {
        destination,
        destination_type,
        routing_key
      }
  ]
  == [
    {destination:"retrieval.book-uploaded.v1", destination_type:"queue", routing_key:"catalog.book.uploaded.v1"},
    {destination:"retrieval.chunks-ready.v1", destination_type:"queue", routing_key:"ingestion.book.chunks-ready.v1"}
  ]
' "$definitions" >/dev/null || {
  echo "Retrieval source return exchange bindings are incorrect" >&2
  exit 1
}

jq -e '
  [
    .bindings[]?
    | select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1"
      )
    | .routing_key
  ]
  == [
    "catalog.book.uploaded.v1",
    "ingestion.book.chunks-ready.v1"
  ]
' "$definitions" >/dev/null || {
  echo "Retrieval source DLQ bindings do not preserve upstream routing keys" >&2
  exit 1
}

jq -e '
  all(
    .queues[]?;
    (
      .name != "retrieval.book-uploaded.v1.retry.5s" and
      .name != "retrieval.book-uploaded.v1.retry.30s" and
      .name != "retrieval.chunks-ready.v1.retry.5s" and
      .name != "retrieval.chunks-ready.v1.retry.30s"
    ) or (
      .arguments["x-dead-letter-exchange"] != "raglibrarian.events.v1" and
      .arguments["x-dead-letter-exchange"] != "raglibrarian.ingestion.events.v1"
    )
  )
' "$definitions" >/dev/null || {
  echo "Retrieval source retry queues still dead-letter through shared exchanges" >&2
  exit 1
}
