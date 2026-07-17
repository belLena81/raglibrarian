#!/bin/sh
set -eu

install -d -m 0700 -o rabbitmq -g rabbitmq /tmp/rabbitmq
install -m 0600 -o rabbitmq -g rabbitmq /bootstrap/rabbitmq_definitions.json /tmp/rabbitmq/definitions.json
printf 'management.load_definitions = /tmp/rabbitmq/definitions.json\n' > /tmp/rabbitmq/rabbitmq.conf
chown rabbitmq:rabbitmq /tmp/rabbitmq/rabbitmq.conf
chmod 0600 /tmp/rabbitmq/rabbitmq.conf

export RABBITMQ_CONFIG_FILE=/tmp/rabbitmq/rabbitmq
exec /usr/local/bin/docker-entrypoint.sh rabbitmq-server
