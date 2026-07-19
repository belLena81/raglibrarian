#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }
dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -r "$definitions" ]] || { echo "M3 RabbitMQ definitions are required first" >&2; exit 1; }
files=(ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password ingestion_migration_pgpass ingestion_runtime_dsn ingestion_cleanup_dsn ingestion_minio_access_key ingestion_minio_secret_key ingestion_cleanup_minio_access_key ingestion_cleanup_minio_secret_key catalog_ingestion_rabbitmq_uri ingestion_rabbitmq_uri edge_status_rabbitmq_uri_1 edge_status_rabbitmq_uri_2)
for file in "${files[@]}"; do
  [[ ! -e "$dir/$file" ]] || { echo "refusing to overwrite existing development secret: $dir/$file" >&2; exit 1; }
done

ingestion_migration_password=$(openssl rand -hex 32)
ingestion_runtime_password=$(openssl rand -hex 32)
ingestion_cleanup_password=$(openssl rand -hex 32)
ingestion_minio_secret_key=$(openssl rand -hex 32)
ingestion_cleanup_minio_secret_key=$(openssl rand -hex 32)
catalog_consume_password=$(openssl rand -hex 32)
ingestion_password=$(openssl rand -hex 32)
edge_password_1=$(openssl rand -hex 32)
edge_password_2=$(openssl rand -hex 32)
printf '%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_password"
printf '%s\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_password"
printf '%s\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_password"
printf 'postgres:5432:ingestion:ingestion_migrator:%s\n' "$ingestion_migration_password" > "$dir/ingestion_migration_pgpass"
printf 'postgres://ingestion_runtime:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_runtime_password" > "$dir/ingestion_runtime_dsn"
printf 'postgres://ingestion_cleanup:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_cleanup_password" > "$dir/ingestion_cleanup_dsn"
printf '%s\n' ingestion-service > "$dir/ingestion_minio_access_key"
printf '%s\n' "$ingestion_minio_secret_key" > "$dir/ingestion_minio_secret_key"
printf '%s\n' ingestion-cleanup > "$dir/ingestion_cleanup_minio_access_key"
printf '%s\n' "$ingestion_cleanup_minio_secret_key" > "$dir/ingestion_cleanup_minio_secret_key"
printf 'amqp://catalog_ingestion:%s@rabbitmq:5672/\n' "$catalog_consume_password" > "$dir/catalog_ingestion_rabbitmq_uri"
printf 'amqp://ingestion_worker:%s@rabbitmq:5672/\n' "$ingestion_password" > "$dir/ingestion_rabbitmq_uri"
printf 'amqp://edge_status_1:%s@rabbitmq:5672/\n' "$edge_password_1" > "$dir/edge_status_rabbitmq_uri_1"
printf 'amqp://edge_status_2:%s@rabbitmq:5672/\n' "$edge_password_2" > "$dir/edge_status_rabbitmq_uri_2"

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq --arg catalog_consume "$catalog_consume_password" --arg ingestion "$ingestion_password" --arg edge1 "$edge_password_1" --arg edge2 "$edge_password_2" '
  .users += [
    {name:"catalog_ingestion",password:$catalog_consume,tags:""},
    {name:"ingestion_worker",password:$ingestion,tags:""},
    {name:"edge_status_1",password:$edge1,tags:""},
    {name:"edge_status_2",password:$edge2,tags:""}
  ] |
  .permissions = ([.permissions[] | if .user == "catalog_publisher" then .write = "^(raglibrarian\\.events\\.v1|raglibrarian\\.edge-status\\.v1)$" else . end] + [
    {user:"catalog_ingestion",vhost:"/",configure:"^$",write:"^$",read:"^catalog\\.book-processing\\.v1$"},
    {user:"ingestion_worker",vhost:"/",configure:"^$",write:"^(raglibrarian\\.ingestion\\.events\\.v1|raglibrarian\\.ingestion\\.retry\\.v1)$",read:"^ingestion\\.book-uploaded\\.v1$"},
    {user:"edge_status_1",vhost:"/",configure:"^edge\\.book-status\\.local\\.1$",write:"^edge\\.book-status\\.local\\.1$",read:"^(raglibrarian\\.edge-status\\.v1|edge\\.book-status\\.local\\.1)$"},
    {user:"edge_status_2",vhost:"/",configure:"^edge\\.book-status\\.local\\.2$",write:"^edge\\.book-status\\.local\\.2$",read:"^(raglibrarian\\.edge-status\\.v1|edge\\.book-status\\.local\\.2)$"}
  ]) |
  .exchanges += [
    {name:"raglibrarian.ingestion.retry.v1",vhost:"/",type:"direct",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.ingestion.events.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.ingestion.events.dlx.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.edge-status.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.edge-status.dlx.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}}
  ] |
  .queues += [
    {name:"ingestion.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"ingestion.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"ingestion.retry.2m",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":120000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"catalog.book-processing.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.ingestion.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"catalog.book-processing.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"edge.book-status.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":67108864,"x-overflow":"reject-publish"}}
  ] |
  .bindings += [
    {source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.retry.5s",destination_type:"queue",routing_key:"ingestion.retry.5s",arguments:{}},
    {source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.retry.30s",destination_type:"queue",routing_key:"ingestion.retry.30s",arguments:{}},
    {source:"raglibrarian.ingestion.retry.v1",vhost:"/",destination:"ingestion.retry.2m",destination_type:"queue",routing_key:"ingestion.retry.2m",arguments:{}},
    {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.processing-started.v1",arguments:{}},
    {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.chunks-ready.v1",arguments:{}},
    {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.processing-failed.v1",arguments:{}},
    {source:"raglibrarian.ingestion.events.dlx.v1",vhost:"/",destination:"catalog.book-processing.dlq.v1",destination_type:"queue",routing_key:"#",arguments:{}},
    {source:"raglibrarian.edge-status.dlx.v1",vhost:"/",destination:"edge.book-status.dlq.v1",destination_type:"queue",routing_key:"#",arguments:{}}
  ]
' "$definitions" > "$updated"
chmod 400 "$dir"/ingestion_* "$dir"/catalog_ingestion_rabbitmq_uri "$dir"/edge_status_rabbitmq_uri_1 "$dir"/edge_status_rabbitmq_uri_2 "$updated"
mv "$updated" "$definitions"
trap - EXIT
unset ingestion_migration_password ingestion_runtime_password ingestion_cleanup_password ingestion_minio_secret_key ingestion_cleanup_minio_secret_key catalog_consume_password ingestion_password edge_password_1 edge_password_2
echo "Generated additive M4 development credentials in $dir"
