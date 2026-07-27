#!/usr/bin/env bash
set -euo pipefail
umask 077

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
[[ -d "$dir" && ! -L "$dir" && -f "$definitions" && ! -L "$definitions" && -r "$definitions" ]] || {
  echo "runtime development secrets and RabbitMQ definitions are required first" >&2
  exit 1
}

files=(
  ingestion_e2e_password
  ingestion_e2e_dsn
  ingestion_e2e_container_dsn
  ingestion_e2e_minio_access_key
  ingestion_e2e_minio_secret_key
  ingestion_e2e_rabbitmq_uri
  ingestion_e2e_rabbitmq_container_uri
  retrieval_e2e_password
  retrieval_e2e_dsn
  retrieval_e2e_container_dsn
  retrieval_e2e_rabbitmq_uri
  retrieval_e2e_rabbitmq_container_uri
  answer_llm_test_api_key
)

for file in "${files[@]}"; do
  [[ ! -e "$dir/$file" ]] || { echo "refusing to overwrite existing test secret: $dir/$file" >&2; exit 1; }
done

ingestion_e2e_password=$(openssl rand -hex 32)
ingestion_e2e_minio_secret_key=$(openssl rand -hex 32)
ingestion_e2e_rabbitmq_password=$(openssl rand -hex 32)
retrieval_e2e_password=$(openssl rand -hex 32)
retrieval_e2e_rabbitmq_password=$(openssl rand -hex 32)

printf '%s\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_password"
printf 'postgres://ingestion_e2e:%s@127.0.0.1:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_dsn"
printf 'postgres://ingestion_e2e:%s@postgres:5432/ingestion?sslmode=disable\n' "$ingestion_e2e_password" > "$dir/ingestion_e2e_container_dsn"
printf '%s\n' ingestion-e2e > "$dir/ingestion_e2e_minio_access_key"
printf '%s\n' "$ingestion_e2e_minio_secret_key" > "$dir/ingestion_e2e_minio_secret_key"
printf 'amqp://ingestion_e2e:%s@127.0.0.1:5672/\n' "$ingestion_e2e_rabbitmq_password" > "$dir/ingestion_e2e_rabbitmq_uri"
printf 'amqp://ingestion_e2e:%s@rabbitmq:5672/\n' "$ingestion_e2e_rabbitmq_password" > "$dir/ingestion_e2e_rabbitmq_container_uri"

printf '%s\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_password"
printf 'postgres://retrieval_e2e:%s@127.0.0.1:5432/retrieval?sslmode=disable\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_dsn"
printf 'postgres://retrieval_e2e:%s@postgres:5432/retrieval?sslmode=disable\n' "$retrieval_e2e_password" > "$dir/retrieval_e2e_container_dsn"
printf 'amqp://retrieval_e2e:%s@127.0.0.1:5672/\n' "$retrieval_e2e_rabbitmq_password" > "$dir/retrieval_e2e_rabbitmq_uri"
printf 'amqp://retrieval_e2e:%s@rabbitmq:5672/\n' "$retrieval_e2e_rabbitmq_password" > "$dir/retrieval_e2e_rabbitmq_container_uri"

bash ./scripts/ensure-m6-dev-secret.sh "$dir"

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq \
  --arg ingestion_e2e "$ingestion_e2e_rabbitmq_password" \
  --arg retrieval_e2e "$retrieval_e2e_rabbitmq_password" '
  def add_user($name; $password):
    if any(.users[]?; .name == $name) then . else .users += [{name:$name,password:$password,tags:""}] end;
  def add_perm($user; $vhost; $configure; $write; $read):
    if any(.permissions[]?; .user == $user and .vhost == $vhost and .configure == $configure and .write == $write and .read == $read) then .
    else .permissions += [{user:$user,vhost:$vhost,configure:$configure,write:$write,read:$read}] end;
  add_user("ingestion_e2e"; $ingestion_e2e) |
  add_user("retrieval_e2e"; $retrieval_e2e) |
  add_perm("ingestion_e2e"; "/"; "^$"; "^raglibrarian\\.events\\.v1$"; "^ingestion\\.book-uploaded\\.dlq\\.v1$") |
  add_perm("retrieval_e2e"; "/"; "^$"; "^(raglibrarian\\.events\\.v1|raglibrarian\\.ingestion\\.events\\.v1)$"; "^(retrieval\\..*|catalog\\.retrieval-terminal)\\.dlq\\.v1$")
' "$definitions" > "$updated"
chmod 400 "$updated"
mv -f "$updated" "$definitions"
trap - EXIT

chmod 400 \
  "$dir/ingestion_e2e_password" \
  "$dir/ingestion_e2e_dsn" \
  "$dir/ingestion_e2e_container_dsn" \
  "$dir/ingestion_e2e_minio_access_key" \
  "$dir/ingestion_e2e_minio_secret_key" \
  "$dir/ingestion_e2e_rabbitmq_uri" \
  "$dir/ingestion_e2e_rabbitmq_container_uri" \
  "$dir/retrieval_e2e_password" \
  "$dir/retrieval_e2e_dsn" \
  "$dir/retrieval_e2e_container_dsn" \
  "$dir/retrieval_e2e_rabbitmq_uri" \
  "$dir/retrieval_e2e_rabbitmq_container_uri" \
  "$dir/answer_llm_test_api_key"

echo "Generated test-only development credentials in $dir"
