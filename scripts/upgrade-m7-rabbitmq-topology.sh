#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -d "$dir" && ! -L "$dir" && -f "$definitions" && ! -L "$definitions" ]] || {
  echo "M7 RabbitMQ definitions require a regular existing secret directory" >&2
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

  .permissions |= map(
    if .user == "retrieval_consumer" then
      .read = "^(retrieval\\.(book-uploaded|chunks-ready|book-lifecycle|index-batch)\\.v1)$"
    else . end
  ) |
  add_queue({name:"retrieval.book-lifecycle.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.retrieval.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":67108864,"x-overflow":"reject-publish"}}) |
  add_queue({name:"retrieval.book-lifecycle.v1.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"retrieval.book-lifecycle.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":67108864,"x-overflow":"reject-publish"}}) |
  add_queue({name:"retrieval.book-lifecycle.v1.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"retrieval.book-lifecycle.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":67108864,"x-overflow":"reject-publish"}}) |
  add_binding({source:"raglibrarian.events.v1",vhost:"/",destination:"ingestion.book-uploaded.v1",destination_type:"queue",routing_key:"catalog.book.deletion-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.artifacts-deleted.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.events.v1",vhost:"/",destination:"retrieval.book-lifecycle.v1",destination_type:"queue",routing_key:"catalog.book.reindex-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.events.v1",vhost:"/",destination:"retrieval.book-lifecycle.v1",destination_type:"queue",routing_key:"catalog.book.deletion-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.source-return.v1",vhost:"/",destination:"retrieval.book-lifecycle.v1",destination_type:"queue",routing_key:"retrieval.book-lifecycle.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.source.dlq.v1",destination_type:"queue",routing_key:"catalog.book.reindex-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.source.dlq.v1",destination_type:"queue",routing_key:"catalog.book.deletion-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.source.dlq.v1",destination_type:"queue",routing_key:"retrieval.book-lifecycle.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.book-lifecycle.v1.retry.5s",destination_type:"queue",routing_key:"retrieval.book-lifecycle.v1.retry.5s",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.book-lifecycle.v1.retry.30s",destination_type:"queue",routing_key:"retrieval.book-lifecycle.v1.retry.30s",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.events.v1",vhost:"/",destination:"catalog.retrieval-terminal.v1",destination_type:"queue",routing_key:"retrieval.book.index-deleted.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"catalog.retrieval-terminal.dlq.v1",destination_type:"queue",routing_key:"retrieval.book.index-deleted.v1",arguments:{}})
' "$definitions" > "$updated"
chmod 400 "$updated"
mv "$updated" "$definitions"
trap - EXIT
