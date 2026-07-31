#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -d "$dir" && ! -L "$dir" && -f "$definitions" && ! -L "$definitions" ]] || {
  echo "Content-selection topology requires regular RabbitMQ definitions" >&2
  exit 1
}
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq '
  def add_exchange($exchange):
    if any(.exchanges[]?; .name == $exchange.name and .vhost == $exchange.vhost) then . else .exchanges += [$exchange] end;
  def add_queue($queue):
    if any(.queues[]?; .name == $queue.name and .vhost == $queue.vhost) then . else .queues += [$queue] end;
  def add_binding($binding):
    if any(.bindings[]?;
      .source == $binding.source and .vhost == $binding.vhost and
      .destination == $binding.destination and .destination_type == $binding.destination_type and
      .routing_key == $binding.routing_key
    ) then . else .bindings += [$binding] end;
  .permissions = [.permissions[] |
    if .user == "ingestion_worker" then
      .read = "^(ingestion\\.book-uploaded\\.v1|ingestion\\.content-selection-results\\.v1)$"
    else . end
  ] |
  .bindings = [.bindings[]? | select(
    .source != "raglibrarian.ingestion.events.v1" or
    .destination != "ingestion.content-selection-results.v1" or
    .routing_key != "ingestion.book.content-selection-completed.v1"
  )] |
  add_exchange({name:"raglibrarian.ingestion.selection.dlx.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}}) |
  add_exchange({name:"raglibrarian.ingestion.content-selection-results.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}}) |
  add_queue({name:"ingestion.content-selection-requests.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.ingestion.selection.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.content-selection-results.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.ingestion.selection.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.content-selection.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.ingestion.content-selection-results.v1","x-dead-letter-routing-key":"ingestion.book.content-selection-completed.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.content-selection.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.ingestion.content-selection-results.v1","x-dead-letter-routing-key":"ingestion.book.content-selection-completed.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.content-selection.retry.2m",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":120000,"x-dead-letter-exchange":"raglibrarian.ingestion.content-selection-results.v1","x-dead-letter-routing-key":"ingestion.book.content-selection-completed.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  .queues = [.queues[] | if (.name | startswith("ingestion.content-selection.retry.")) then .arguments["x-dead-letter-exchange"] = "raglibrarian.ingestion.content-selection-results.v1" else . end] |
  add_queue({name:"ingestion.content-selection-requests.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_queue({name:"ingestion.content-selection-results.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}) |
  add_binding({source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"ingestion.content-selection-requests.v1",destination_type:"queue",routing_key:"ingestion.book.content-selection-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.content-selection-results.v1",vhost:"/",destination:"ingestion.content-selection-results.v1",destination_type:"queue",routing_key:"ingestion.book.content-selection-completed.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.content-selection.retry.5s",destination_type:"queue",routing_key:"ingestion.content-selection.retry.5s",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.content-selection.retry.30s",destination_type:"queue",routing_key:"ingestion.content-selection.retry.30s",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.content-selection.retry.2m",destination_type:"queue",routing_key:"ingestion.content-selection.retry.2m",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.selection.dlx.v1",vhost:"/",destination:"ingestion.content-selection-requests.dlq.v1",destination_type:"queue",routing_key:"ingestion.book.content-selection-requested.v1",arguments:{}}) |
  add_binding({source:"raglibrarian.ingestion.selection.dlx.v1",vhost:"/",destination:"ingestion.content-selection-results.dlq.v1",destination_type:"queue",routing_key:"ingestion.book.content-selection-completed.v1",arguments:{}})
' "$definitions" > "$updated"
chmod 400 "$updated"
mv -f "$updated" "$definitions"
trap - EXIT
