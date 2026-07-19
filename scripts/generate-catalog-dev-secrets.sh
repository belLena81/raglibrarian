#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

dir="${1:-.dev/secrets}"
mkdir -p "$dir"
chmod 700 "$dir"
for file in minio_root_user minio_root_password catalog_minio_access_key catalog_minio_secret_key ingestion_minio_access_key ingestion_minio_secret_key ingestion_cleanup_minio_access_key ingestion_cleanup_minio_secret_key ingestion_e2e_minio_access_key ingestion_e2e_minio_secret_key catalog_rabbitmq_uri catalog_ingestion_rabbitmq_uri ingestion_rabbitmq_uri ingestion_e2e_rabbitmq_uri ingestion_e2e_rabbitmq_container_uri edge_status_rabbitmq_uri_1 edge_status_rabbitmq_uri_2 rabbitmq_definitions.json rabbitmq.conf; do
  if [[ -e "$dir/$file" ]]; then
    echo "refusing to overwrite existing development secret: $dir/$file" >&2
    exit 1
  fi
done

catalog_publish_password=$(openssl rand -hex 32)
catalog_consume_password=$(openssl rand -hex 32)
ingestion_password=$(openssl rand -hex 32)
ingestion_e2e_password=$(openssl rand -hex 32)
edge_password=$(openssl rand -hex 32)
edge_password_2=$(openssl rand -hex 32)
minio_root_user=raglibrarian_root
minio_root_password=$(openssl rand -hex 32)
minio_access_key=catalog-service
minio_secret_key=$(openssl rand -hex 32)
ingestion_minio_access_key=ingestion-service
ingestion_minio_secret_key=$(openssl rand -hex 32)
ingestion_cleanup_minio_access_key=ingestion-cleanup
ingestion_cleanup_minio_secret_key=$(openssl rand -hex 32)
ingestion_e2e_minio_access_key=ingestion-e2e
ingestion_e2e_minio_secret_key=$(openssl rand -hex 32)
printf '%s\n' "$minio_root_user" > "$dir/minio_root_user"
printf '%s\n' "$minio_root_password" > "$dir/minio_root_password"
printf '%s\n' "$minio_access_key" > "$dir/catalog_minio_access_key"
printf '%s\n' "$minio_secret_key" > "$dir/catalog_minio_secret_key"
printf '%s\n' "$ingestion_minio_access_key" > "$dir/ingestion_minio_access_key"
printf '%s\n' "$ingestion_minio_secret_key" > "$dir/ingestion_minio_secret_key"
printf '%s\n' "$ingestion_cleanup_minio_access_key" > "$dir/ingestion_cleanup_minio_access_key"
printf '%s\n' "$ingestion_cleanup_minio_secret_key" > "$dir/ingestion_cleanup_minio_secret_key"
printf '%s\n' "$ingestion_e2e_minio_access_key" > "$dir/ingestion_e2e_minio_access_key"
printf '%s\n' "$ingestion_e2e_minio_secret_key" > "$dir/ingestion_e2e_minio_secret_key"
printf 'amqp://catalog_publisher:%s@rabbitmq:5672/\n' "$catalog_publish_password" > "$dir/catalog_rabbitmq_uri"
printf 'amqp://catalog_ingestion:%s@rabbitmq:5672/\n' "$catalog_consume_password" > "$dir/catalog_ingestion_rabbitmq_uri"
printf 'amqp://ingestion_worker:%s@rabbitmq:5672/\n' "$ingestion_password" > "$dir/ingestion_rabbitmq_uri"
printf 'amqp://ingestion_e2e:%s@127.0.0.1:5672/\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_rabbitmq_uri"
printf 'amqp://ingestion_e2e:%s@rabbitmq:5672/\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_rabbitmq_container_uri"
printf 'amqp://edge_status_1:%s@rabbitmq:5672/\n' "$edge_password" > "$dir/edge_status_rabbitmq_uri_1"
printf 'amqp://edge_status_2:%s@rabbitmq:5672/\n' "$edge_password_2" > "$dir/edge_status_rabbitmq_uri_2"
printf '{"users":[{"name":"catalog_publisher","password":"%s","tags":""},{"name":"catalog_ingestion","password":"%s","tags":""},{"name":"ingestion_worker","password":"%s","tags":""},{"name":"edge_status","password":"%s","tags":""}],"vhosts":[{"name":"/"}],"permissions":[{"user":"catalog_publisher","vhost":"/","configure":"^$","write":"^(raglibrarian\\\\.events\\\\.v1|raglibrarian\\\\.edge-status\\\\.v1)$","read":"^$"},{"user":"catalog_ingestion","vhost":"/","configure":"^$","write":"^$","read":"^catalog\\\\.book-processing\\\\.v1$"},{"user":"ingestion_worker","vhost":"/","configure":"^$","write":"^(raglibrarian\\\\.ingestion\\\\.events\\\\.v1|raglibrarian\\\\.ingestion\\\\.retry\\\\.v1)$","read":"^ingestion\\\\.book-uploaded\\\\.v1$"},{"user":"edge_status","vhost":"/","configure":"^$","write":"^$","read":"^edge\\\\.book-status\\\\.local$"}],"exchanges":[{"name":"raglibrarian.events.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.events.dlx.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.ingestion.retry.v1","vhost":"/","type":"direct","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.ingestion.events.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.ingestion.events.dlx.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.edge-status.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.edge-status.dlx.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}}],"queues":[{"name":"ingestion.book-uploaded.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.events.dlx.v1"}},{"name":"ingestion.book-uploaded.dlq.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum"}},{"name":"ingestion.retry.5s","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-message-ttl":5000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1"}},{"name":"ingestion.retry.30s","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-message-ttl":30000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1"}},{"name":"ingestion.retry.2m","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-message-ttl":120000,"x-dead-letter-exchange":"raglibrarian.events.v1","x-dead-letter-routing-key":"catalog.book.uploaded.v1"}},{"name":"catalog.book-processing.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.ingestion.events.dlx.v1"}},{"name":"catalog.book-processing.dlq.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum"}},{"name":"edge.book-status.local","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.edge-status.dlx.v1"}},{"name":"edge.book-status.dlq.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum"}}],"bindings":[{"source":"raglibrarian.events.v1","vhost":"/","destination":"ingestion.book-uploaded.v1","destination_type":"queue","routing_key":"catalog.book.uploaded.v1","arguments":{}},{"source":"raglibrarian.events.dlx.v1","vhost":"/","destination":"ingestion.book-uploaded.dlq.v1","destination_type":"queue","routing_key":"#","arguments":{}},{"source":"raglibrarian.ingestion.retry.v1","vhost":"/","destination":"ingestion.retry.5s","destination_type":"queue","routing_key":"ingestion.retry.5s","arguments":{}},{"source":"raglibrarian.ingestion.retry.v1","vhost":"/","destination":"ingestion.retry.30s","destination_type":"queue","routing_key":"ingestion.retry.30s","arguments":{}},{"source":"raglibrarian.ingestion.retry.v1","vhost":"/","destination":"ingestion.retry.2m","destination_type":"queue","routing_key":"ingestion.retry.2m","arguments":{}},{"source":"raglibrarian.ingestion.events.v1","vhost":"/","destination":"catalog.book-processing.v1","destination_type":"queue","routing_key":"ingestion.book.processing.*.v1","arguments":{}},{"source":"raglibrarian.ingestion.events.dlx.v1","vhost":"/","destination":"catalog.book-processing.dlq.v1","destination_type":"queue","routing_key":"#","arguments":{}},{"source":"raglibrarian.edge-status.v1","vhost":"/","destination":"edge.book-status.local","destination_type":"queue","routing_key":"catalog.book.processing.status-changed.v1","arguments":{}},{"source":"raglibrarian.edge-status.dlx.v1","vhost":"/","destination":"edge.book-status.dlq.v1","destination_type":"queue","routing_key":"#","arguments":{}}]}' "$catalog_publish_password" "$catalog_consume_password" "$ingestion_password" "$edge_password" > "$dir/rabbitmq_definitions.json"
printf 'management.load_definitions = /etc/rabbitmq/definitions.json\n' > "$dir/rabbitmq.conf"
sed -i 's/catalog\.book\.processing\.status-changed\.v1/catalog.book.processing-status-changed.v1/g' "$dir/rabbitmq_definitions.json"
definitions_tmp=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
jq --arg edge2 "$edge_password_2" --arg ingestion_e2e "$ingestion_e2e_password" '
  .users = ([.users[] | if .name == "edge_status" then .name = "edge_status_1" else . end] + [{name:"edge_status_2",password:$edge2,tags:""},{name:"ingestion_e2e",password:$ingestion_e2e,tags:""}]) |
  .permissions = ([.permissions[] | if .user == "edge_status" then .user = "edge_status_1" | .configure = "^edge\\.book-status\\.local\\.1$" | .write = "^edge\\.book-status\\.local\\.1$" | .read = "^(raglibrarian\\.edge-status\\.v1|edge\\.book-status\\.local\\.1)$" else . end] + [
    {user:"edge_status_2",vhost:"/",configure:"^edge\\.book-status\\.local\\.2$",write:"^edge\\.book-status\\.local\\.2$",read:"^(raglibrarian\\.edge-status\\.v1|edge\\.book-status\\.local\\.2)$"}
  ] + [{user:"ingestion_e2e",vhost:"/",configure:"^$",write:"^raglibrarian\\.events\\.v1$",read:"^ingestion\\.book-uploaded\\.dlq\\.v1$"}]) |
  .queues = ([.queues[] | select(.name != "edge.book-status.local") | .arguments +=
    (if (.name | endswith("dlq.v1")) then {"x-message-ttl":604800000,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}
     elif (.name == "ingestion.book-uploaded.v1" or .name == "catalog.book-processing.v1") then {"x-delivery-limit":5,"x-max-length-bytes":268435456,"x-overflow":"reject-publish"}
     elif (.name | startswith("ingestion.retry.")) then {"x-dead-letter-strategy":"at-least-once","x-max-length-bytes":268435456,"x-overflow":"reject-publish"}
     else {} end)]) |
  .bindings = ([.bindings[] | select(.routing_key != "ingestion.book.processing.*.v1" and .destination != "edge.book-status.local")] + [
  {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.processing-started.v1",arguments:{}},
  {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.chunks-ready.v1",arguments:{}},
  {source:"raglibrarian.ingestion.events.v1",vhost:"/",destination:"catalog.book-processing.v1",destination_type:"queue",routing_key:"ingestion.book.processing-failed.v1",arguments:{}}
])' "$dir/rabbitmq_definitions.json" > "$definitions_tmp"
mv "$definitions_tmp" "$dir/rabbitmq_definitions.json"
chmod 400 "$dir"/catalog_* "$dir"/ingestion_* "$dir"/edge_status_* "$dir"/minio_* "$dir"/rabbitmq_definitions.json "$dir"/rabbitmq.conf
unset catalog_publish_password catalog_consume_password ingestion_password ingestion_e2e_password edge_password edge_password_2 minio_root_user minio_root_password minio_access_key minio_secret_key ingestion_minio_access_key ingestion_minio_secret_key ingestion_cleanup_minio_access_key ingestion_cleanup_minio_secret_key ingestion_e2e_minio_access_key ingestion_e2e_minio_secret_key
echo "Generated Catalog development credentials in $dir"
