#!/usr/bin/env bash
set -euo pipefail
umask 077

dir="${1:-${SECRET_DIR:-.dev/secrets}}"
definitions="$dir/rabbitmq_definitions.json"
uri_file="$dir/layout_rabbitmq_uri"

if [[ ! -d "$dir" || -L "$dir" || "$(stat -c '%a' "$dir")" != 700 ||
      ! -f "$definitions" || -L "$definitions" ]]; then
  echo "Layout worker RabbitMQ upgrade requires a secure secret directory and regular definitions" >&2
  exit 1
fi
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

if [[ ! -e "$uri_file" ]]; then
  command -v openssl >/dev/null || { echo "openssl is required" >&2; exit 1; }
  password=$(openssl rand -hex 32)
  printf 'amqp://layout_parser_worker:%s@rabbitmq:5672/\n' "$password" > "$uri_file"
  chmod 400 "$uri_file"
fi
if [[ ! -f "$uri_file" || -L "$uri_file" || ! -r "$uri_file" ||
      "$(stat -c '%a' "$uri_file")" != 400 ]]; then
  echo "Layout worker RabbitMQ URI must be a readable non-symlink regular file with mode 0400" >&2
  exit 1
fi

uri=$(<"$uri_file")
if [[ ! "$uri" =~ ^amqp://layout_parser_worker:([0-9a-f]{64})@rabbitmq:5672/$ ]]; then
  echo "Layout worker RabbitMQ URI is invalid" >&2
  exit 1
fi
password=${BASH_REMATCH[1]}

updated=$(mktemp "$dir/rabbitmq_definitions.XXXXXX")
trap 'rm -f "$updated"' EXIT
jq --arg password "$password" '
  .users = ([.users[]? | select(.name != "layout_parser_worker")] + [
    {name:"layout_parser_worker",password:$password,tags:""}
  ]) |
  .permissions = ([.permissions[]? | select(.user != "layout_parser_worker")] + [
    {
      user:"layout_parser_worker",
      vhost:"/",
      configure:"^$",
      write:"^raglibrarian\\.ingestion\\.content-selection-results\\.v1$",
      read:"^ingestion\\.content-selection-requests\\.v1$"
    }
  ])
' "$definitions" > "$updated"
chmod 400 "$updated"
mv -f "$updated" "$definitions"
trap - EXIT
unset password uri
