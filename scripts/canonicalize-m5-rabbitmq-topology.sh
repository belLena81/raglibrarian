#!/usr/bin/env bash
set -euo pipefail

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"

[[ -d "$dir" && ! -L "$dir" && -f "$definitions" && ! -L "$definitions" ]] || {
  echo "Canonicalization requires a regular secret directory and definitions file" >&2
  exit 1
}

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT

jq '
  .queues |= (
    [ .[] | select(.name == "retrieval.book-uploaded.v1.retry.5s") ] +
    [ .[] | select(.name == "retrieval.book-uploaded.v1.retry.30s") ] +
    [ .[] | select(.name == "retrieval.chunks-ready.v1.retry.5s") ] +
    [ .[] | select(.name == "retrieval.chunks-ready.v1.retry.30s") ] +
    [ .[] | select(.name == "retrieval.book-lifecycle.v1.retry.5s") ] +
    [ .[] | select(.name == "retrieval.book-lifecycle.v1.retry.30s") ] +
    [
      .[] |
      select(
        .name != "retrieval.book-uploaded.v1.retry.5s" and
        .name != "retrieval.book-uploaded.v1.retry.30s" and
        .name != "retrieval.chunks-ready.v1.retry.5s" and
        .name != "retrieval.chunks-ready.v1.retry.30s" and
        .name != "retrieval.book-lifecycle.v1.retry.5s" and
        .name != "retrieval.book-lifecycle.v1.retry.30s"
      )
    ]
  ) |
  .bindings |= (
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.source-return.v1" and
        .destination == "retrieval.book-uploaded.v1" and
        .destination_type == "queue" and
        .routing_key == "catalog.book.uploaded.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.source-return.v1" and
        .destination == "retrieval.chunks-ready.v1" and
        .destination_type == "queue" and
        .routing_key == "ingestion.book.chunks-ready.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.source-return.v1" and
        .destination == "retrieval.book-lifecycle.v1" and
        .destination_type == "queue" and
        .routing_key == "retrieval.book-lifecycle.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1" and
        .routing_key == "catalog.book.uploaded.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1" and
        .routing_key == "ingestion.book.chunks-ready.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1" and
        .routing_key == "catalog.book.reindex-requested.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1" and
        .routing_key == "catalog.book.deletion-requested.v1"
      )
    ] +
    [
      .[] |
      select(
        .source == "raglibrarian.retrieval.events.dlx.v1" and
        .destination == "retrieval.source.dlq.v1" and
        .routing_key == "retrieval.book-lifecycle.v1"
      )
    ] +
    [
      .[] |
      select(
        .source != "raglibrarian.retrieval.source-return.v1" and
        (
          .source != "raglibrarian.retrieval.events.dlx.v1" or
          .destination != "retrieval.source.dlq.v1"
        )
      )
    ]
  )
' "$definitions" > "$updated"

mv -f "$updated" "$definitions"
chmod 400 "$definitions"
trap - EXIT
