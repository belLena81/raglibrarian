#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v jq >/dev/null || { echo 'jq is required' >&2; exit 1; }
command -v openssl >/dev/null || { echo 'openssl is required' >&2; exit 1; }

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -d "$dir" && ! -L "$dir" && -r "$definitions" ]] || {
  echo 'M4 development secrets and RabbitMQ definitions are required first' >&2
  exit 1
}

files=(
  retrieval_migration_password retrieval_runtime_password retrieval_search_password retrieval_planner_password retrieval_indexer_password retrieval_dispatcher_password retrieval_cleanup_password retrieval_e2e_password
  retrieval_migration_pgpass retrieval_runtime_dsn retrieval_runtime_host_dsn retrieval_search_dsn retrieval_cleanup_dsn retrieval_e2e_dsn retrieval_e2e_container_dsn
  retrieval_minio_access_key retrieval_minio_secret_key retrieval_consumer_rabbitmq_uri
  retrieval_publisher_rabbitmq_uri catalog_retrieval_rabbitmq_uri retrieval_e2e_rabbitmq_uri
  retrieval_e2e_rabbitmq_container_uri retrieval_qdrant_api_key retrieval_qdrant_read_api_key
)
for file in "${files[@]}"; do
  [[ ! -e "$dir/$file" ]] || { echo "refusing to overwrite existing development secret: $dir/$file" >&2; exit 1; }
done

retrieval_migration_password=$(openssl rand -hex 32)
retrieval_runtime_password=$(openssl rand -hex 32)
retrieval_search_password=$(openssl rand -hex 32)
retrieval_planner_password=$(openssl rand -hex 32)
retrieval_indexer_password=$(openssl rand -hex 32)
retrieval_dispatcher_password=$(openssl rand -hex 32)
retrieval_cleanup_password=$(openssl rand -hex 32)
retrieval_e2e_password=$(openssl rand -hex 32)
retrieval_consumer_password=$(openssl rand -hex 32)
retrieval_publisher_password=$(openssl rand -hex 32)
catalog_retrieval_password=$(openssl rand -hex 32)
retrieval_e2e_rabbitmq_password=$(openssl rand -hex 32)
retrieval_minio_secret_key=$(openssl rand -hex 32)

printf '%s\n' "$retrieval_migration_password" > "$dir/retrieval_migration_password"
printf '%s\n' "$retrieval_runtime_password" > "$dir/retrieval_runtime_password"
printf '%s\n' "$retrieval_search_password" > "$dir/retrieval_search_password"
printf '%s\n' "$retrieval_planner_password" > "$dir/retrieval_planner_password"
printf '%s\n' "$retrieval_indexer_password" > "$dir/retrieval_indexer_password"
printf '%s\n' "$retrieval_dispatcher_password" > "$dir/retrieval_dispatcher_password"
printf '%s\n' "$retrieval_cleanup_password" > "$dir/retrieval_cleanup_password"
printf '%s\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_password"
printf 'postgres:5432:retrieval:retrieval_migrator:%s\n' "$retrieval_migration_password" > "$dir/retrieval_migration_pgpass"
printf 'postgres://retrieval_runtime:%s@postgres:5432/retrieval?sslmode=disable\n' "$retrieval_runtime_password" > "$dir/retrieval_runtime_dsn"
printf 'postgres://retrieval_runtime:%s@127.0.0.1:5432/retrieval?sslmode=disable\n' "$retrieval_runtime_password" > "$dir/retrieval_runtime_host_dsn"
printf 'postgres://retrieval_search:%s@postgres:5432/retrieval?sslmode=disable\n' "$retrieval_search_password" > "$dir/retrieval_search_dsn"
printf 'postgres://retrieval_cleanup:%s@postgres:5432/retrieval?sslmode=disable\n' "$retrieval_cleanup_password" > "$dir/retrieval_cleanup_dsn"
printf 'postgres://retrieval_e2e:%s@127.0.0.1:5432/retrieval?sslmode=disable\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_dsn"
printf 'postgres://retrieval_e2e:%s@postgres:5432/retrieval?sslmode=disable\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_container_dsn"
printf '%s\n' retrieval-service > "$dir/retrieval_minio_access_key"
printf '%s\n' "$retrieval_minio_secret_key" > "$dir/retrieval_minio_secret_key"
printf 'amqp://retrieval_consumer:%s@rabbitmq:5672/\n' "$retrieval_consumer_password" > "$dir/retrieval_consumer_rabbitmq_uri"
printf 'amqp://retrieval_publisher:%s@rabbitmq:5672/\n' "$retrieval_publisher_password" > "$dir/retrieval_publisher_rabbitmq_uri"
printf 'amqp://catalog_retrieval:%s@rabbitmq:5672/\n' "$catalog_retrieval_password" > "$dir/catalog_retrieval_rabbitmq_uri"
printf 'amqp://retrieval_e2e:%s@127.0.0.1:5672/\n' "$retrieval_e2e_rabbitmq_password" > "$dir/retrieval_e2e_rabbitmq_uri"
printf 'amqp://retrieval_e2e:%s@rabbitmq:5672/\n' "$retrieval_e2e_rabbitmq_password" > "$dir/retrieval_e2e_rabbitmq_container_uri"
openssl rand -hex 32 > "$dir/retrieval_qdrant_api_key"
openssl rand -hex 32 > "$dir/retrieval_qdrant_read_api_key"

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq \
  --arg consumer "$retrieval_consumer_password" \
  --arg publisher "$retrieval_publisher_password" \
  --arg catalog "$catalog_retrieval_password" \
  --arg e2e "$retrieval_e2e_rabbitmq_password" '
  .users += [
    {name:"retrieval_consumer",password:$consumer,tags:""},
    {name:"retrieval_publisher",password:$publisher,tags:""},
    {name:"catalog_retrieval",password:$catalog,tags:""},
    {name:"retrieval_e2e",password:$e2e,tags:""}
  ] |
  .permissions += [
    {user:"retrieval_consumer",vhost:"/",configure:"^$",write:"^$",read:"^(retrieval\\.(book-uploaded|chunks-ready|index-batch)\\.v1)$"},
    {user:"retrieval_publisher",vhost:"/",configure:"^$",write:"^(raglibrarian\\.retrieval\\.(events|retry|source-return)\\.v1)$",read:"^$"},
    {user:"catalog_retrieval",vhost:"/",configure:"^$",write:"^$",read:"^catalog\\.retrieval-terminal\\.v1$"},
    {user:"retrieval_e2e",vhost:"/",configure:"^$",write:"^(raglibrarian\\.events\\.v1|raglibrarian\\.ingestion\\.events\\.v1)$",read:"^(retrieval\\..*|catalog\\.retrieval-terminal)\\.dlq\\.v1$"}
  ] |
  .exchanges += [
    {name:"raglibrarian.retrieval.events.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",type:"topic",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.retrieval.source-return.v1",vhost:"/",type:"direct",durable:true,auto_delete:false,internal:false,arguments:{}},
    {name:"raglibrarian.retrieval.retry.v1",vhost:"/",type:"direct",durable:true,auto_delete:false,internal:false,arguments:{}}
  ] |
  .queues += [
    {name:"retrieval.book-uploaded.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.retrieval.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.chunks-ready.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.retrieval.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.index-batch.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.retrieval.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"catalog.retrieval-terminal.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.retrieval.events.dlx.v1","x-delivery-limit":5,"x-max-length-bytes":67108864,"x-overflow":"reject-publish"}},
    {name:"retrieval.source.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.index-batch.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"catalog.retrieval-terminal.dlq.v1",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":604800000,"x-max-length-bytes":67108864,"x-overflow":"reject-publish"}},
    {name:"retrieval.book-uploaded.v1.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.book-uploaded.v1.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.chunks-ready.v1.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"ingestion.book.chunks-ready.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.chunks-ready.v1.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.retrieval.source-return.v1","x-dead-letter-routing-key":"ingestion.book.chunks-ready.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.index-batch.v1.retry.5s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.retrieval.events.v1","x-dead-letter-routing-key":"retrieval.index-batch.requested.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}},
    {name:"retrieval.index-batch.v1.retry.30s",vhost:"/",durable:true,auto_delete:false,arguments:{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.retrieval.events.v1","x-dead-letter-routing-key":"retrieval.index-batch.requested.v1","x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}}
  ] |
  .bindings += [
    {source:"raglibrarian.events.v1",vhost:"/",destination:"retrieval.book-uploaded.v1",destination_type:"queue",routing_key:"catalog.book.uploaded.v1",arguments:{}},
    {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"retrieval.chunks-ready.v1",destination_type:"queue",routing_key:"ingestion.book.chunks-ready.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.v1",vhost:"/",destination:"retrieval.index-batch.v1",destination_type:"queue",routing_key:"retrieval.index-batch.requested.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.v1",vhost:"/",destination:"catalog.retrieval-terminal.v1",destination_type:"queue",routing_key:"retrieval.book.indexed.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.v1",vhost:"/",destination:"catalog.retrieval-terminal.v1",destination_type:"queue",routing_key:"retrieval.book.indexing-failed.v1",arguments:{}},
    {source:"raglibrarian.retrieval.source-return.v1",vhost:"/",destination:"retrieval.book-uploaded.v1",destination_type:"queue",routing_key:"catalog.book.uploaded.v1",arguments:{}},
    {source:"raglibrarian.retrieval.source-return.v1",vhost:"/",destination:"retrieval.chunks-ready.v1",destination_type:"queue",routing_key:"ingestion.book.chunks-ready.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.source.dlq.v1",destination_type:"queue",routing_key:"catalog.book.uploaded.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.source.dlq.v1",destination_type:"queue",routing_key:"ingestion.book.chunks-ready.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"retrieval.index-batch.dlq.v1",destination_type:"queue",routing_key:"retrieval.index-batch.requested.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"catalog.retrieval-terminal.dlq.v1",destination_type:"queue",routing_key:"retrieval.book.indexed.v1",arguments:{}},
    {source:"raglibrarian.retrieval.events.dlx.v1",vhost:"/",destination:"catalog.retrieval-terminal.dlq.v1",destination_type:"queue",routing_key:"retrieval.book.indexing-failed.v1",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.book-uploaded.v1.retry.5s",destination_type:"queue",routing_key:"retrieval.book-uploaded.v1.retry.5s",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.book-uploaded.v1.retry.30s",destination_type:"queue",routing_key:"retrieval.book-uploaded.v1.retry.30s",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.chunks-ready.v1.retry.5s",destination_type:"queue",routing_key:"retrieval.chunks-ready.v1.retry.5s",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.chunks-ready.v1.retry.30s",destination_type:"queue",routing_key:"retrieval.chunks-ready.v1.retry.30s",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.index-batch.v1.retry.5s",destination_type:"queue",routing_key:"retrieval.index-batch.v1.retry.5s",arguments:{}},
    {source:"raglibrarian.retrieval.retry.v1",vhost:"/",destination:"retrieval.index-batch.v1.retry.30s",destination_type:"queue",routing_key:"retrieval.index-batch.v1.retry.30s",arguments:{}}
  ]
' "$definitions" > "$updated"

chmod 400 "${files[@]/#/$dir/}" "$updated"
mv "$updated" "$definitions"
trap - EXIT
unset retrieval_migration_password retrieval_runtime_password retrieval_search_password retrieval_planner_password retrieval_indexer_password
unset retrieval_dispatcher_password retrieval_cleanup_password retrieval_e2e_password
unset retrieval_consumer_password retrieval_publisher_password catalog_retrieval_password
unset retrieval_e2e_rabbitmq_password retrieval_minio_secret_key
echo "Generated additive M5 development credentials in $dir"
