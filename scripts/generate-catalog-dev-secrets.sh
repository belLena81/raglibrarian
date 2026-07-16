#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-.dev/secrets}"
mkdir -p "$dir"
chmod 700 "$dir"
for file in minio_root_user minio_root_password catalog_minio_access_key catalog_minio_secret_key catalog_rabbitmq_uri rabbitmq_definitions.json rabbitmq.conf; do
  if [[ -e "$dir/$file" ]]; then
    echo "refusing to overwrite existing development secret: $dir/$file" >&2
    exit 1
  fi
done

rabbit_password=$(openssl rand -hex 32)
minio_root_user=raglibrarian_root
minio_root_password=$(openssl rand -hex 32)
minio_access_key=catalog-service
minio_secret_key=$(openssl rand -hex 32)
printf '%s\n' "$minio_root_user" > "$dir/minio_root_user"
printf '%s\n' "$minio_root_password" > "$dir/minio_root_password"
printf '%s\n' "$minio_access_key" > "$dir/catalog_minio_access_key"
printf '%s\n' "$minio_secret_key" > "$dir/catalog_minio_secret_key"
printf 'amqp://catalog_publisher:%s@rabbitmq:5672/\n' "$rabbit_password" > "$dir/catalog_rabbitmq_uri"
printf '{"users":[{"name":"catalog_publisher","password":"%s","tags":""}],"vhosts":[{"name":"/"}],"permissions":[{"user":"catalog_publisher","vhost":"/","configure":"^$","write":"^raglibrarian\\\\.events\\\\.v1$","read":"^$"}],"exchanges":[{"name":"raglibrarian.events.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}},{"name":"raglibrarian.events.dlx.v1","vhost":"/","type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}}],"queues":[{"name":"ingestion.book-uploaded.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum","x-dead-letter-exchange":"raglibrarian.events.dlx.v1"}},{"name":"ingestion.book-uploaded.dlq.v1","vhost":"/","durable":true,"auto_delete":false,"arguments":{"x-queue-type":"quorum"}}],"bindings":[{"source":"raglibrarian.events.v1","vhost":"/","destination":"ingestion.book-uploaded.v1","destination_type":"queue","routing_key":"catalog.book.uploaded.v1","arguments":{}},{"source":"raglibrarian.events.dlx.v1","vhost":"/","destination":"ingestion.book-uploaded.dlq.v1","destination_type":"queue","routing_key":"#","arguments":{}}]}' "$rabbit_password" > "$dir/rabbitmq_definitions.json"
printf 'management.load_definitions = /etc/rabbitmq/definitions.json\n' > "$dir/rabbitmq.conf"
chmod 400 "$dir"/catalog_* "$dir"/minio_* "$dir"/rabbitmq_definitions.json "$dir"/rabbitmq.conf
unset rabbit_password minio_root_user minio_root_password minio_access_key minio_secret_key
echo "Generated Catalog development credentials in $dir"
