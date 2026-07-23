#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -d "$dir" && ! -L "$dir" && -f "$definitions" && ! -L "$definitions" ]] || {
  echo "M7 Ingestion topology requires regular RabbitMQ definitions" >&2
  exit 1
}
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq '
  def add_binding($binding):
    if any(.bindings[]?;
      .source == $binding.source and .vhost == $binding.vhost and
      .destination == $binding.destination and .destination_type == $binding.destination_type and
      .routing_key == $binding.routing_key
    ) then . else .bindings += [$binding] end;
  add_binding({source:"raglibrarian.events.v1",vhost:"/",destination:"ingestion.book-uploaded.v1",destination_type:"queue",routing_key:"catalog.book.deletion-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.artifacts-deleted.v1",arguments:{}})
' "$definitions" > "$updated"
chmod 400 "$updated"
mv "$updated" "$definitions"
trap - EXIT
