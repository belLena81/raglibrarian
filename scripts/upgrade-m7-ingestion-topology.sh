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
  def add_queue($queue):
    if any(.queues[]?; .name == $queue.name and .vhost == $queue.vhost) then . else .queues += [$queue] end;
  def add_binding($binding):
    if any(.bindings[]?;
      .source == $binding.source and .vhost == $binding.vhost and
      .destination == $binding.destination and .destination_type == $binding.destination_type and
      .routing_key == $binding.routing_key
    ) then . else .bindings += [$binding] end;
  add_queue({name:"ingestion.deletion.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.deletion-requested.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.deletion.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.deletion-requested.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.deletion.retry.2m",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":120000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.deletion-requested.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_binding({source:"raglibrarian.events.v1",vhost:"/",destination:"ingestion.book-uploaded.v1",destination_type:"queue",routing_key:"catalog.book.deletion-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.deletion.retry.5s",destination_type:"queue",routing_key:"ingestion.deletion.retry.5s",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.deletion.retry.30s",destination_type:"queue",routing_key:"ingestion.deletion.retry.30s",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.deletion.retry.2m",destination_type:"queue",routing_key:"ingestion.deletion.retry.2m",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.artifacts-deleted.v1",arguments:{}})
' "$definitions" > "$updated"
chmod 400 "$updated"
mv "$updated" "$definitions"
trap - EXIT
